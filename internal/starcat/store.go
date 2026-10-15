package starcat

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"path"
	"sort"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"
	"golang.org/x/sync/errgroup"
)

// ErrUnavailable says the mirror cannot answer: it has no manifest, or a
// piece it lists is missing or damaged. Callers fall back to VizieR.
var ErrUnavailable = errors.New("starcat: mirror unavailable")

const (
	manifestName  = "manifest.json.zst"
	manifestTTL   = time.Hour
	fetchParallel = 8
)

// Bucket is the object store a mirror lives in.
type Bucket interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Put(ctx context.Context, key string, data []byte) error
	List(ctx context.Context, prefix string) ([]string, error)
	Delete(ctx context.Context, key string) error
}

// MinioBucket is a Bucket in one MinIO (S3) bucket.
type MinioBucket struct {
	Client *minio.Client
	Name   string
}

func (b MinioBucket) Get(ctx context.Context, key string) ([]byte, error) {
	o, err := b.Client.GetObject(ctx, b.Name, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer o.Close()
	data, err := io.ReadAll(o)
	if err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return nil, fmt.Errorf("%s: %w", key, ErrNotFound)
		}
		return nil, err
	}
	return data, nil
}

func (b MinioBucket) Put(ctx context.Context, key string, data []byte) error {
	_, err := b.Client.PutObject(ctx, b.Name, key, bytes.NewReader(data), int64(len(data)),
		minio.PutObjectOptions{ContentType: "application/octet-stream", DisableMultipart: len(data) < 256<<20})
	return err
}

func (b MinioBucket) List(ctx context.Context, prefix string) ([]string, error) {
	var out []string
	for o := range b.Client.ListObjects(ctx, b.Name, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
		if o.Err != nil {
			return nil, o.Err
		}
		out = append(out, o.Key)
	}
	return out, nil
}

func (b MinioBucket) Delete(ctx context.Context, key string) error {
	return b.Client.RemoveObject(ctx, b.Name, key, minio.RemoveObjectOptions{})
}

// ErrNotFound says an object does not exist.
var ErrNotFound = errors.New("not found")

// Input is one source file a mirror was built from.
type Input struct {
	File   string `json:"file"`
	MD5    string `json:"md5,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	Bytes  int64  `json:"bytes"`
	Rows   int64  `json:"rows"`
}

// Piece is one stored file: a partition's rows from one input.
type Piece struct {
	Partition int64  `json:"p"`
	Key       string `json:"k"`
	Rows      int64  `json:"n"`
	Bytes     int64  `json:"b"`
	SHA256    string `json:"h"`
}

// Manifest lists a complete, verified mirror.
type Manifest struct {
	Catalog  string    `json:"catalog"`
	Schema   string    `json:"schema"`
	Order    int       `json:"order"`
	Epoch    float64   `json:"epoch,omitempty"`
	Version  string    `json:"version"`
	Source   string    `json:"source"`
	Created  time.Time `json:"created"`
	Verified time.Time `json:"verified"`
	Rows     int64     `json:"rows"`
	Bytes    int64     `json:"bytes"`
	Inputs   []Input   `json:"inputs"`
	Pieces   []Piece   `json:"pieces"`
}

func encodeManifest(m *Manifest) ([]byte, error) {
	j, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return zenc().EncodeAll(j, nil), nil
}

func decodeManifest(b []byte) (*Manifest, error) {
	j, err := zdec().DecodeAll(b, nil)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(j, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// table reads one catalogue's mirror under a prefix.
type table[T any] struct {
	bucket Bucket
	prefix string
	schema schema[T]
	now    func() time.Time

	mu     sync.Mutex
	man    *Manifest
	byPart map[int64][]Piece
	loaded time.Time
}

func (t *table[T]) manifest(ctx context.Context) (*Manifest, map[int64][]Piece, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.man != nil && t.now().Sub(t.loaded) < manifestTTL {
		return t.man, t.byPart, nil
	}
	b, err := t.bucket.Get(ctx, path.Join(t.prefix, manifestName))
	if err != nil {
		if t.man != nil {
			return t.man, t.byPart, nil
		}
		return nil, nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	m, err := decodeManifest(b)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: manifest: %w", ErrUnavailable, err)
	}
	if m.Schema != t.schema.name {
		return nil, nil, fmt.Errorf("%w: manifest is %s, not %s", ErrUnavailable, m.Schema, t.schema.name)
	}
	by := map[int64][]Piece{}
	for _, p := range m.Pieces {
		by[p.Partition] = append(by[p.Partition], p)
	}
	t.man, t.byPart, t.loaded = m, by, t.now()
	return m, by, nil
}

// cone returns the rows within radius degrees of (ra, dec) that keep
// accepts (nil keeps all), in no particular order.
func (t *table[T]) cone(ctx context.Context, ra, dec, radius float64, keep func(*T) bool) ([]T, error) {
	m, by, err := t.manifest(ctx)
	if err != nil {
		return nil, err
	}
	var pieces []Piece
	for _, p := range Cover(m.Order, ra, dec, radius) {
		pieces = append(pieces, by[p]...)
	}
	v := Vec(ra, dec)
	r := radius * deg
	var mu sync.Mutex
	var out []T
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(fetchParallel)
	for _, p := range pieces {
		g.Go(func() error {
			b, err := t.bucket.Get(gctx, p.Key)
			if err != nil {
				return fmt.Errorf("%w: %w", ErrUnavailable, err)
			}
			if h := sha256.Sum256(b); hex.EncodeToString(h[:]) != p.SHA256 {
				return fmt.Errorf("%w: %s fails its checksum", ErrUnavailable, p.Key)
			}
			var mine []T
			_, err = decodePiece(t.schema, b, v, r, func(rows []T) {
				for i := range rows {
					a, d := t.schema.pos(&rows[i])
					if Angle(Vec(a, d), v) <= r && (keep == nil || keep(&rows[i])) {
						mine = append(mine, rows[i])
					}
				}
			})
			if err != nil {
				return fmt.Errorf("%w: %s: %w", ErrUnavailable, p.Key, err)
			}
			mu.Lock()
			out = append(out, mine...)
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return out, nil
}

// box returns the rows with RA in [raMin, raMax] (wrapping through 0 when
// raMin > raMax) and Dec in [decMin, decMax], all in degrees.
func (t *table[T]) box(ctx context.Context, raMin, raMax, decMin, decMax float64, keep func(*T) bool) ([]T, error) {
	inRA := func(a float64) bool {
		if raMin <= raMax {
			return a >= raMin && a <= raMax
		}
		return a >= raMin || a <= raMax
	}
	width := raMax - raMin
	if width < 0 {
		width += 360
	}
	ra, dec, radius := boxCap(raMin, width, decMin, decMax)
	return t.cone(ctx, ra, dec, radius, func(s *T) bool {
		a, d := t.schema.pos(s)
		return inRA(a) && d >= decMin && d <= decMax && (keep == nil || keep(s))
	})
}

// boxCap returns a cone holding an RA/Dec box: its edges are sampled, and
// the radius grows by the sample spacing.
func boxCap(raMin, width, decMin, decMax float64) (ra, dec, radius float64) {
	const n = 256
	ra = math.Mod(raMin+width/2, 360)
	dec = (decMin + decMax) / 2
	c := Vec(ra, dec)
	step := 0.0
	var prev [3]float64
	for e := range 4 {
		for i := 0; i <= n; i++ {
			f := float64(i) / n
			var a, d float64
			switch e {
			case 0:
				a, d = raMin+f*width, decMin
			case 1:
				a, d = raMin+f*width, decMax
			case 2:
				a, d = raMin, decMin+f*(decMax-decMin)
			default:
				a, d = raMin+width, decMin+f*(decMax-decMin)
			}
			v := Vec(a, d)
			radius = math.Max(radius, Angle(c, v))
			if i > 0 {
				step = math.Max(step, Angle(prev, v))
			}
			prev = v
		}
	}
	return ra, dec, (radius + step) / deg
}

// Gaia reads the Gaia DR3 mirror.
type Gaia struct{ t *table[GaiaStar] }

// NewGaia reads the Gaia DR3 mirror under prefix in a bucket.
func NewGaia(b Bucket, prefix string) *Gaia {
	return &Gaia{t: &table[GaiaStar]{bucket: b, prefix: prefix, schema: gaiaSchema(), now: time.Now}}
}

// Cone returns the stars within radius degrees of (ra, dec) at their
// 2016.0 positions that keep accepts (nil keeps all), by source_id.
func (g *Gaia) Cone(ctx context.Context, ra, dec, radius float64, keep func(*GaiaStar) bool) ([]GaiaStar, error) {
	out, err := g.t.cone(ctx, ra, dec, radius, keep)
	sort.Slice(out, func(i, j int) bool { return out[i].SourceID < out[j].SourceID })
	return out, err
}

// Box returns the stars in an RA/Dec box at their 2016.0 positions.
func (g *Gaia) Box(ctx context.Context, raMin, raMax, decMin, decMax float64, keep func(*GaiaStar) bool) ([]GaiaStar, error) {
	out, err := g.t.box(ctx, raMin, raMax, decMin, decMax, keep)
	sort.Slice(out, func(i, j int) bool { return out[i].SourceID < out[j].SourceID })
	return out, err
}

// Manifest returns the mirror's manifest.
func (g *Gaia) Manifest(ctx context.Context) (*Manifest, error) {
	m, _, err := g.t.manifest(ctx)
	return m, err
}

// VSX reads the VSX mirror.
type VSX struct{ t *table[VSXStar] }

// NewVSX reads the VSX mirror under prefix in a bucket.
func NewVSX(b Bucket, prefix string) *VSX {
	return &VSX{t: &table[VSXStar]{bucket: b, prefix: prefix, schema: vsxSchema(), now: time.Now}}
}

// Cone returns the VSX stars within radius degrees of (ra, dec), by OID.
func (v *VSX) Cone(ctx context.Context, ra, dec, radius float64, keep func(*VSXStar) bool) ([]VSXStar, error) {
	out, err := v.t.cone(ctx, ra, dec, radius, keep)
	sort.Slice(out, func(i, j int) bool { return out[i].OID < out[j].OID })
	return out, err
}

// Box returns the VSX stars in an RA/Dec box.
func (v *VSX) Box(ctx context.Context, raMin, raMax, decMin, decMax float64, keep func(*VSXStar) bool) ([]VSXStar, error) {
	out, err := v.t.box(ctx, raMin, raMax, decMin, decMax, keep)
	sort.Slice(out, func(i, j int) bool { return out[i].OID < out[j].OID })
	return out, err
}

// Manifest returns the mirror's manifest.
func (v *VSX) Manifest(ctx context.Context) (*Manifest, error) {
	m, _, err := v.t.manifest(ctx)
	return m, err
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
