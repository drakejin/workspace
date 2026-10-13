// Package cold 는 cold 층이다: raw 원본·청크 JSONL 보관소(로컬 또는 S3)와 인덱스 스냅샷(list/restore/tier).
package cold

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/drakejin/workspace/app/internal/config"
)

// Store 는 raw 원본과 청크 JSONL 을 두는 곳이다. 키는 posix 상대 경로.
type Store interface {
	Location() string
	Put(ctx context.Context, key, body string) error
	// Get 은 없으면 ("", false, nil).
	Get(ctx context.Context, key string) (string, bool, error)
	List(ctx context.Context, prefix string) ([]string, error)
	Remove(ctx context.Context, key string) error
}

// RawKey 는 원본 사본 키다.
func RawKey(team, sourcePath string) string { return team + "/raw/" + sourcePath }

// ChunksKey 는 청크 JSONL 키다.
func ChunksKey(team, sourcePath string) string { return team + "/chunks/" + sourcePath + ".jsonl" }

// ChunksPrefix 는 팀 청크 JSONL 전체 접두어다.
func ChunksPrefix(team string) string { return team + "/chunks/" }

// TeamPrefix 는 팀의 보관본(raw·청크·목록) 전체 접두어다.
func TeamPrefix(team string) string { return team + "/" }

func safeKey(key string) (string, error) {
	clean := path.Clean(key)
	if strings.HasPrefix(clean, "..") || path.IsAbs(clean) {
		return "", fmt.Errorf("cold store 키가 잘못됐습니다: %q", key)
	}
	return clean, nil
}

// NewStore 는 설정대로 로컬 디렉토리 또는 S3 보관소를 만든다. 로컬 디렉토리는 meta-repo 마다 따로라
// 그대로 쓰고, 공유 버킷인 S3 는 {prefix}/{workspace}/ 아래로 나눈다.
// 자격 증명은 AWS SDK 기본 체인. S3 호환 저장소는 AWS_ENDPOINT_URL_S3 + COLD_STORE_S3_PATH_STYLE.
// S3 도 목록 읽기-고치기-쓰기는 로컬 디렉토리(cfg.Dir)의 잠금 파일로 직렬화한다 — 같은 meta-repo 의 tier·archive 사이.
func NewStore(ctx context.Context, cfg config.ColdStore, workspace string) (Store, error) {
	if cfg.S3Bucket == "" {
		return NewLocalStore(cfg.Dir), nil
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.Region))
	if err != nil {
		return nil, fmt.Errorf("AWS 설정을 읽지 못했습니다: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.S3Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.S3Endpoint)
		}
		o.UsePathStyle = cfg.S3PathStyle
	})
	store := newS3Store(client, cfg.S3Bucket, path.Join(cfg.S3Prefix, workspace))
	store.locks = keyLocks{dir: filepath.Join(cfg.Dir, lockDirName)}
	return store, nil
}

type localStore struct {
	root  string
	locks keyLocks
}

// NewLocalStore 는 로컬 디렉토리 보관소다.
func NewLocalStore(root string) Store {
	return &localStore{root: root, locks: keyLocks{dir: filepath.Join(root, lockDirName)}}
}

func (s *localStore) lockKey(key string) (func(), error) { return s.locks.lock(key) }

func (s *localStore) Location() string { return s.root }

func (s *localStore) file(key string) (string, error) {
	clean, err := safeKey(key)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.root, filepath.FromSlash(clean)), nil
}

// Put 은 같은 디렉토리의 임시 파일에 다 쓰고 fsync 한 뒤 rename 으로 바꾼다. 도중에 끊겨도 정본(raw·목록)은
// 옛 내용 그대로이거나 새 내용 전체다 — 잘라 내고 덮어쓰면 반쯤 쓴 정본이 남는다.
func (s *localStore) Put(_ context.Context, key, body string) error {
	f, err := s.file(key)
	if err != nil {
		return err
	}
	dir := filepath.Dir(f)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(f)+".tmp-*")
	if err != nil {
		return err
	}
	if err := writeSynced(tmp, body); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("put %s: %w", key, err)
	}
	if err := os.Rename(tmp.Name(), f); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("put %s: %w", key, err)
	}
	return nil
}

// isPutTemp 는 Put 이 쓰는 중인 임시 파일이다 (목록에 키로 내보내지 않는다).
func isPutTemp(name string) bool {
	return strings.HasPrefix(name, ".") && strings.Contains(name, ".tmp-")
}

// writeSynced 는 body 를 쓰고 디스크까지 내린 뒤 닫는다 (권한은 os.WriteFile 과 같은 0644).
func writeSynced(f *os.File, body string) error {
	_, err := f.WriteString(body)
	if err == nil {
		err = f.Chmod(0o644)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func (s *localStore) Get(_ context.Context, key string) (string, bool, error) {
	f, err := s.file(key)
	if err != nil {
		return "", false, err
	}
	b, err := os.ReadFile(f)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return string(b), true, nil
}

func (s *localStore) List(_ context.Context, prefix string) ([]string, error) {
	base, err := s.file(prefix)
	if err != nil {
		return nil, err
	}
	var keys []string
	err = filepath.WalkDir(base, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrNotExist) {
				return filepath.SkipAll
			}
			return walkErr
		}
		if d.Type().IsRegular() && !isPutTemp(d.Name()) {
			rel, _ := filepath.Rel(s.root, p)
			keys = append(keys, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(keys)
	return keys, nil
}

func (s *localStore) Remove(_ context.Context, key string) error {
	f, err := s.file(key)
	if err != nil {
		return err
	}
	if err := os.Remove(f); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// S3API 는 이 패키지가 쓰는 S3 호출만이다 (소비자 측 narrow interface).
type S3API interface {
	PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	ListObjectsV2(ctx context.Context, in *s3.ListObjectsV2Input, opts ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	DeleteObject(ctx context.Context, in *s3.DeleteObjectInput, opts ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
}

type s3Store struct {
	api    S3API
	bucket string
	prefix string
	locks  keyLocks
}

// NewS3Store 는 S3 보관소다. 키는 {prefix}/{key}.
func NewS3Store(api S3API, bucket, prefix string) Store { return newS3Store(api, bucket, prefix) }

func newS3Store(api S3API, bucket, prefix string) *s3Store {
	return &s3Store{api: api, bucket: bucket, prefix: strings.Trim(prefix, "/")}
}

func (s *s3Store) lockKey(key string) (func(), error) { return s.locks.lock(key) }

func (s *s3Store) Location() string { return "s3://" + s.bucket + "/" + s.prefix }

func (s *s3Store) full(key string) (string, error) {
	clean, err := safeKey(key)
	if err != nil {
		return "", err
	}
	return path.Join(s.prefix, clean), nil
}

func (s *s3Store) Put(ctx context.Context, key, body string) error {
	k, err := s.full(key)
	if err != nil {
		return err
	}
	_, err = s.api.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(k), Body: bytes.NewReader([]byte(body))})
	return err
}

func (s *s3Store) Get(ctx context.Context, key string) (string, bool, error) {
	k, err := s.full(key)
	if err != nil {
		return "", false, err
	}
	out, err := s.api.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(k)})
	var noKey *s3types.NoSuchKey
	if errors.As(err, &noKey) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	defer out.Body.Close()
	b, err := io.ReadAll(out.Body)
	if err != nil {
		return "", false, err
	}
	return string(b), true, nil
}

func (s *s3Store) List(ctx context.Context, prefix string) ([]string, error) {
	p, err := s.full(prefix)
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(prefix, "/") {
		p += "/"
	}
	var keys []string
	var token *string
	for {
		out, err := s.api.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(s.bucket), Prefix: aws.String(p), ContinuationToken: token})
		if err != nil {
			return nil, err
		}
		for _, o := range out.Contents {
			keys = append(keys, strings.TrimPrefix(aws.ToString(o.Key), s.prefix+"/"))
		}
		if !aws.ToBool(out.IsTruncated) {
			break
		}
		token = out.NextContinuationToken
	}
	sort.Strings(keys)
	return keys, nil
}

func (s *s3Store) Remove(ctx context.Context, key string) error {
	k, err := s.full(key)
	if err != nil {
		return err
	}
	_, err = s.api.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(k)})
	return err
}
