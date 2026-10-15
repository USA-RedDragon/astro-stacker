package starcat

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// WriteGaia stores stars as a complete, verified mirror under prefix, two
// pieces per partition when it has two stars or more. It is meant for
// tests and small catalogues; MirrorGaia builds the real one.
func WriteGaia(ctx context.Context, b Bucket, prefix string, stars []GaiaStar) error {
	return writeAll(ctx, b, prefix, "gaia-dr3", GaiaOrder, gaiaSchema(), stars)
}

// WriteVSX stores stars as a complete, verified VSX mirror under prefix.
func WriteVSX(ctx context.Context, b Bucket, prefix string, stars []VSXStar) error {
	return writeAll(ctx, b, prefix, "vsx", VSXOrder, vsxSchema(), stars)
}

func writeAll[T any](ctx context.Context, b Bucket, prefix, catalog string, order int, s schema[T], rows []T) error {
	parts := map[int64][]T{}
	for i := range rows {
		ra, dec := s.pos(&rows[i])
		p := Pixel(order, ra, dec)
		parts[p] = append(parts[p], rows[i])
	}
	keys := make([]int64, 0, len(parts))
	for p := range parts {
		keys = append(keys, p)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	m := &Manifest{Catalog: catalog, Schema: s.name, Order: order, Created: time.Now().UTC()}
	for _, p := range keys {
		rs := parts[p]
		half := len(rs) / 2
		for i, chunk := range [][]T{rs[:half], rs[half:]} {
			if len(chunk) == 0 {
				continue
			}
			data, err := encodePiece(s, chunk)
			if err != nil {
				return err
			}
			pc := Piece{Partition: p, Key: path.Join(prefix, "order"+strconv.Itoa(order), strconv.FormatInt(p, 10), strconv.Itoa(i)+".scat"),
				Rows: int64(len(chunk)), Bytes: int64(len(data)), SHA256: sha256Hex(data)}
			if err := b.Put(ctx, pc.Key, data); err != nil {
				return err
			}
			m.Pieces = append(m.Pieces, pc)
			m.Rows += pc.Rows
			m.Bytes += pc.Bytes
		}
	}
	if err := verifyPieces(ctx, b, s, order, m.Pieces); err != nil {
		return err
	}
	m.Verified = time.Now().UTC()
	data, err := encodeManifest(m)
	if err != nil {
		return err
	}
	return b.Put(ctx, path.Join(prefix, manifestName), data)
}

// MemBucket is a Bucket in memory, for tests.
type MemBucket struct {
	mu   sync.Mutex
	objs map[string][]byte
}

func (m *MemBucket) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.objs[key]
	if !ok {
		return nil, fmt.Errorf("%s: %w", key, ErrNotFound)
	}
	return append([]byte(nil), b...), nil
}

func (m *MemBucket) Put(_ context.Context, key string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.objs == nil {
		m.objs = map[string][]byte{}
	}
	m.objs[key] = append([]byte(nil), data...)
	return nil
}

func (m *MemBucket) List(_ context.Context, prefix string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for k := range m.objs {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (m *MemBucket) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objs, key)
	return nil
}
