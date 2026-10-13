package cold

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/drakejin/workspace/app/internal/model"
)

func exercise(t *testing.T, store Store) {
	t.Helper()
	ctx := context.Background()
	if err := store.Put(ctx, ChunksKey("ads", "docs/a.md"), "{}\n"); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, RawKey("ads", "docs/a.md"), "# a"); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, ChunksKey("ads", "docs/b.md"), "{}\n"); err != nil {
		t.Fatal(err)
	}
	if body, ok, err := store.Get(ctx, "ads/raw/docs/a.md"); err != nil || !ok || body != "# a" {
		t.Errorf("Get = %q %v %v", body, ok, err)
	}
	if _, ok, err := store.Get(ctx, "missing"); ok || err != nil {
		t.Errorf("missing = %v %v", ok, err)
	}
	keys, err := store.List(ctx, ChunksPrefix("ads"))
	if err != nil || !reflect.DeepEqual(keys, []string{"ads/chunks/docs/a.md.jsonl", "ads/chunks/docs/b.md.jsonl"}) {
		t.Errorf("List = %v %v", keys, err)
	}
	if keys, _ := store.List(ctx, "nope/"); len(keys) != 0 {
		t.Errorf("빈 prefix = %v", keys)
	}
	if err := store.Remove(ctx, RawKey("ads", "docs/a.md")); err != nil {
		t.Fatal(err)
	}
	if err := store.Remove(ctx, "never/existed"); err != nil {
		t.Errorf("없는 키 삭제는 성공: %v", err)
	}
	if _, ok, _ := store.Get(ctx, RawKey("ads", "docs/a.md")); ok {
		t.Error("삭제 후 없어야 한다")
	}
	if err := store.Put(ctx, "../escape", ""); err == nil {
		t.Error("밖으로 나가는 키는 거부")
	}
}

func TestLocalStore(t *testing.T) {
	store := NewLocalStore(t.TempDir())
	exercise(t, store)
	if store.Location() == "" {
		t.Error("location")
	}
}

// fakeS3 는 메모리 S3 다. ListObjectsV2 는 한 번에 하나씩 돌려줘 페이지네이션을 태운다.
type fakeS3 struct{ objects map[string]string }

func (f *fakeS3) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	b, _ := io.ReadAll(in.Body)
	f.objects[aws.ToString(in.Key)] = string(b)
	return &s3.PutObjectOutput{}, nil
}

func (f *fakeS3) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	v, ok := f.objects[aws.ToString(in.Key)]
	if !ok {
		return nil, &s3types.NoSuchKey{}
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader([]byte(v)))}, nil
}

func (f *fakeS3) ListObjectsV2(_ context.Context, in *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	var keys []string
	for k := range f.objects {
		if strings.HasPrefix(k, aws.ToString(in.Prefix)) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	start := 0
	if in.ContinuationToken != nil {
		start, _ = strconv.Atoi(*in.ContinuationToken)
	}
	out := &s3.ListObjectsV2Output{IsTruncated: aws.Bool(start+1 < len(keys)), NextContinuationToken: aws.String(strconv.Itoa(start + 1))}
	if start < len(keys) {
		out.Contents = []s3types.Object{{Key: aws.String(keys[start])}}
	}
	return out, nil
}

func (f *fakeS3) DeleteObject(_ context.Context, in *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	delete(f.objects, aws.ToString(in.Key))
	return &s3.DeleteObjectOutput{}, nil
}

func TestS3Store(t *testing.T) {
	fake := &fakeS3{objects: map[string]string{}}
	store := NewS3Store(fake, "bucket", "/memory/ws/")
	exercise(t, store)
	if store.Location() != "s3://bucket/memory/ws" {
		t.Errorf("location = %s", store.Location())
	}
	for k := range fake.objects {
		if !strings.HasPrefix(k, "memory/ws/") {
			t.Errorf("prefix 밖 키: %s", k)
		}
	}
}

func TestChunkCodec(t *testing.T) {
	chunks := []model.Chunk{{ID: "a", Team: "t", SourcePath: "docs/a.md", ChunkIndex: 0}, {ID: "b", Team: "t", SourcePath: "docs/a.md", ChunkIndex: 1}}
	got, bad := DecodeChunks(EncodeChunks(chunks) + "{broken\n\n")
	if !reflect.DeepEqual(got, chunks) || !reflect.DeepEqual(bad, []int{3}) {
		t.Errorf("got=%+v bad=%v", got, bad)
	}
	store := NewLocalStore(t.TempDir())
	ctx := context.Background()
	if _, ok, err := Archived(ctx, store, "t", "docs/a.md"); ok || err != nil {
		t.Errorf("없으면 false: %v %v", ok, err)
	}
	_ = store.Put(ctx, ChunksKey("t", "docs/a.md"), EncodeChunks(chunks))
	if got, ok, _ := Archived(ctx, store, "t", "docs/a.md"); !ok || got[0].ID != "a" {
		t.Errorf("archived = %+v %v", got, ok)
	}
	_ = store.Put(ctx, ChunksKey("t", "docs/empty.md"), "{broken\n")
	if _, ok, _ := Archived(ctx, store, "t", "docs/empty.md"); ok {
		t.Error("읽을 수 있는 청크가 없으면 false")
	}
}

func TestLocalStorePutReplacesTheFileAtomically(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := NewLocalStore(root)
	key := ManifestKey("t", "mem-t-2026.05")
	old := strings.Repeat("{\"snapshot\":\"snap-1\"}\n", 1000)
	if err := store.Put(ctx, key, old); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, filepath.FromSlash(key))
	reader, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	if err := store.Put(ctx, key, "new\n"); err != nil {
		t.Fatal(err)
	}

	// 쓰기 전에 연 독자는 옛 내용을 끝까지 읽는다 — 잘라 내고 덮어쓰면 반쯤 쓴(또는 빈) 정본을 본다
	if got, _ := io.ReadAll(reader); string(got) != old {
		t.Errorf("옛 독자가 본 내용 %d bytes, want %d", len(got), len(old))
	}
	if body, _, _ := store.Get(ctx, key); body != "new\n" {
		t.Errorf("새 내용 = %q", body)
	}
	entries, _ := os.ReadDir(filepath.Dir(file))
	if len(entries) != 1 {
		t.Errorf("임시 파일이 남지 않는다: %v", entries)
	}
	if info, err := os.Stat(file); err != nil || info.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v %v", info.Mode(), err)
	}
}
