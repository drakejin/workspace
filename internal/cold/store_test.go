package cold

import (
	"bytes"
	"context"
	"io"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
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

func TestFirstChunk(t *testing.T) {
	head := firstChunk(`{"team":"t","source_path":"docs/a.md","content_sha":"x","event_time":"2026-01-01T00:00:00Z","memory_kind":"episodic"}` + "\n{}")
	if head == nil || head.sourcePath != "docs/a.md" || head.kind != "episodic" {
		t.Errorf("head = %+v", head)
	}
	if firstChunk("not json") != nil {
		t.Error("깨진 줄은 nil")
	}
}
