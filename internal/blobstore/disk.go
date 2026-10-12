package blobstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/digest"
)

type DiskStore struct {
	dir string
}

func NewDiskStore(dir string) (*DiskStore, error) {
	if dir == "" {
		return nil, fmt.Errorf("disk blobstore dir required")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create blobstore dir %s: %w", dir, err)
	}
	return &DiskStore{dir: dir}, nil
}

func (s *DiskStore) Put(ctx context.Context, r io.Reader) (key string, size int64, sha256hex string, err error) {
	if err := ctx.Err(); err != nil {
		return "", 0, "", fmt.Errorf("blobstore put canceled: %w", err)
	}
	tmp, err := os.CreateTemp(s.dir, "put-*")
	if err != nil {
		return "", 0, "", fmt.Errorf("create blob temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			err = errors.Join(err, os.Remove(tmpName))
		}
	}()

	h := sha256.New()
	size, err = io.Copy(tmp, io.TeeReader(&contextReader{ctx: ctx, r: r}, h))
	if err != nil {
		_ = tmp.Close()
		return "", 0, "", fmt.Errorf("write blob temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", 0, "", fmt.Errorf("close blob temp file: %w", err)
	}

	sha256hex = hex.EncodeToString(h.Sum(nil))
	key = digest.Key(sha256hex)
	path := s.pathForHash(sha256hex)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", 0, "", fmt.Errorf("create blob shard dir: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return key, size, sha256hex, nil
		}
		if _, statErr := os.Stat(path); statErr == nil {
			return key, size, sha256hex, nil
		}
		return "", 0, "", fmt.Errorf("commit blob %s: %w", key, err)
	}
	return key, size, sha256hex, nil
}

func (s *DiskStore) Get(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, fmt.Errorf("blobstore get canceled: %w", err)
	}
	hexPart, err := validateKey(key)
	if err != nil {
		return nil, 0, err
	}
	f, err := os.Open(s.pathForHash(hexPart))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, 0, fmt.Errorf("blob %s: %w", key, ErrBlobNotFound)
		}
		return nil, 0, fmt.Errorf("open blob %s: %w", key, err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, fmt.Errorf("stat blob %s: %w", key, err)
	}
	return f, info.Size(), nil
}

func (s *DiskStore) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("blobstore delete canceled: %w", err)
	}
	hexPart, err := validateKey(key)
	if err != nil {
		return err
	}
	if err := os.Remove(s.pathForHash(hexPart)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete blob %s: %w", key, err)
	}
	return nil
}

// Walk visits every committed blob (<dir>/<2 hex>/<64 hex>); in-flight
// temp files ("put-*" in the root) are not blobs and are skipped.
func (s *DiskStore) Walk(ctx context.Context, fn func(Info) error) error {
	shards, err := os.ReadDir(s.dir)
	if err != nil {
		return fmt.Errorf("list blob dir: %w", err)
	}
	for _, shard := range shards {
		if !shard.IsDir() || len(shard.Name()) != 2 {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(s.dir, shard.Name()))
		if err != nil {
			return fmt.Errorf("list blob shard %s: %w", shard.Name(), err)
		}
		for _, e := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			key := digest.Key(e.Name())
			if e.IsDir() || !strings.HasPrefix(e.Name(), shard.Name()) {
				continue
			}
			if _, err := validateKey(key); err != nil {
				continue
			}
			info, err := e.Info()
			if errors.Is(err, os.ErrNotExist) {
				continue // deleted meanwhile
			}
			if err != nil {
				return fmt.Errorf("stat blob %s: %w", key, err)
			}
			if err := fn(Info{Key: key, Size: info.Size(), ModTime: info.ModTime()}); err != nil {
				return err
			}
		}
	}
	return nil
}

// Stat reports a blob's size and modification time.
func (s *DiskStore) Stat(ctx context.Context, key string) (Info, error) {
	if err := ctx.Err(); err != nil {
		return Info{}, err
	}
	hexPart, err := validateKey(key)
	if err != nil {
		return Info{}, err
	}
	info, err := os.Stat(s.pathForHash(hexPart))
	if errors.Is(err, os.ErrNotExist) {
		return Info{}, fmt.Errorf("blob %s: %w", key, ErrBlobNotFound)
	}
	if err != nil {
		return Info{}, fmt.Errorf("stat blob %s: %w", key, err)
	}
	return Info{Key: key, Size: info.Size(), ModTime: info.ModTime()}, nil
}

func (s *DiskStore) pathForHash(sha256hex string) string {
	return filepath.Join(s.dir, sha256hex[:2], sha256hex)
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
