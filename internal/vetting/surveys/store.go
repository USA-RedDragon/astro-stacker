package surveys

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/minio/minio-go/v7"
)

// ErrNotFound is wrapped by a Store's Get for a key it does not hold.
var ErrNotFound = errors.New("surveys: not in the store")

// MinioStore is a Store on a MinIO bucket.
type MinioStore struct {
	Client *minio.Client
	Bucket string
}

// Get reads one object; a missing key gives an error wrapping ErrNotFound.
func (m MinioStore) Get(ctx context.Context, key string) ([]byte, error) {
	o, err := m.Client.GetObject(ctx, m.Bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, notFound(key, err)
	}
	defer o.Close()
	b, err := io.ReadAll(o)
	if err != nil {
		return nil, notFound(key, err)
	}
	return b, nil
}

func notFound(key string, err error) error {
	r := minio.ToErrorResponse(err)
	if r.Code == "NoSuchKey" || r.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%s: %w", key, ErrNotFound)
	}
	return err
}

// Put writes one object.
func (m MinioStore) Put(ctx context.Context, key string, data []byte, contentType string) error {
	_, err := m.Client.PutObject(ctx, m.Bucket, key, bytes.NewReader(data), int64(len(data)),
		minio.PutObjectOptions{ContentType: contentType, DisableMultipart: len(data) < 256<<20})
	return err
}

// Sizes sums the bytes stored under prefix, per next path segment; an
// object right under prefix counts under its own name.
func (m MinioStore) Sizes(ctx context.Context, prefix string) (map[string]int64, error) {
	out := map[string]int64{}
	for o := range m.Client.ListObjects(ctx, m.Bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
		if o.Err != nil {
			return nil, o.Err
		}
		out[segment(prefix, o.Key)] += o.Size
	}
	return out, nil
}

func segment(prefix, key string) string {
	rest := strings.TrimPrefix(key, prefix)
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		return rest[:i]
	}
	return rest
}

func zstdEncode(raw []byte) ([]byte, error) {
	return zstdEncodeLevel(raw, zstd.SpeedBetterCompression)
}

func zstdEncodeLevel(raw []byte, level zstd.EncoderLevel) ([]byte, error) {
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(level), zstd.WithWindowSize(8<<20))
	if err != nil {
		return nil, err
	}
	defer enc.Close()
	return enc.EncodeAll(raw, nil), nil
}

func zstdDecode(b []byte) ([]byte, error) {
	dec, err := zstd.NewReader(nil, zstd.WithDecoderMaxMemory(4<<30))
	if err != nil {
		return nil, err
	}
	defer dec.Close()
	return dec.DecodeAll(b, nil)
}

func (f *Fetcher) getZ(ctx context.Context, key string) ([]byte, error) {
	b, err := f.Store.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	raw, err := zstdDecode(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", key, err)
	}
	return raw, nil
}

func (f *Fetcher) putZ(ctx context.Context, key string, raw []byte) error {
	b, err := zstdEncode(raw)
	if err != nil {
		return err
	}
	return f.Store.Put(ctx, key, b, "application/zstd")
}
