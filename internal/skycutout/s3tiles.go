package skycutout

import (
	"context"
	"fmt"
	"io"
	"path"

	"github.com/minio/minio-go/v7"
)

// S3Tiles reads HiPS tiles from a mirror in an S3 bucket.
type S3Tiles struct {
	Client *minio.Client
	Bucket string
	Prefix string
}

func (s S3Tiles) Tile(ctx context.Context, order int, npix int64) ([]byte, error) {
	obj, err := s.Client.GetObject(ctx, s.Bucket, path.Join(s.Prefix, TilePath(order, npix)), minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer obj.Close()
	b, err := io.ReadAll(io.LimitReader(obj, maxBody+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxBody {
		return nil, fmt.Errorf("tile larger than %d bytes", maxBody)
	}
	return b, nil
}
