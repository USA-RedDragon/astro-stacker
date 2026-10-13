package skycutout

import (
	"bytes"
	"container/list"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"sync"
	"time"

	healpix "github.com/hebl/gohealpix"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"
)

const (
	TileWidth       = 512
	tileOrderBits   = 9
	tilePixelBits   = 2 * tileOrderBits
	defaultTileMem  = 100 << 20
	defaultQuality  = 90
	tileLoadWorkers = 8
	defaultRenders  = 4
	tileBytes       = TileWidth * TileWidth * 3
	tileTimeout     = 10 * time.Second
)

var ErrNoMirrorOrder = errors.New("no mirrored survey order is fine enough for this field")

// TileStore returns the encoded HiPS tile Norder{order}/Dir{d}/Npix{npix}.
type TileStore interface {
	Tile(ctx context.Context, order int, npix int64) ([]byte, error)
}

// Renderer draws TAN cutouts from a HiPS survey's JPEG tiles, matching what
// hips2fits returns for the same Params.
type Renderer struct {
	Store      TileStore
	MaxOrder   int
	CacheBytes int64
	Quality    int
	MaxRenders int

	once  sync.Once
	cache *tileCache
	slots chan struct{}
	group singleflight.Group
}

func (r *Renderer) init() {
	r.once.Do(func() {
		limit := r.CacheBytes
		if limit <= 0 {
			limit = defaultTileMem
		}
		r.cache = newTileCache(limit)
		n := r.MaxRenders
		if n <= 0 {
			n = defaultRenders
		}
		r.slots = make(chan struct{}, n)
	})
}

// PixelSize is the mean HEALPix pixel size in degrees of a tile's pixels at
// the given HiPS order.
func PixelSize(order int) float64 {
	nside := math.Ldexp(1, order+tileOrderBits)
	return math.Sqrt(math.Pi/3) / nside * 180 / math.Pi
}

// step is the projection-plane size of one output pixel in radians. Like
// hips2fits, fov spans the longer side as a true angle, not as CDELT × pixels.
func (p Params) step() float64 {
	return 2 * math.Tan(p.FOV*math.Pi/360) / float64(max(p.Width, p.Height))
}

// Order picks the coarsest HiPS order whose pixels are no larger than one
// output pixel, within the mirrored orders.
func (r *Renderer) Order(p Params) (int, bool) {
	s := p.step() * 180 / math.Pi
	for k := 0; k <= r.MaxOrder; k++ {
		if PixelSize(k) <= s {
			return k, true
		}
	}
	return 0, false
}

type projection struct {
	c, e, n [3]float64
	cx, cy  float64
	step    float64
	cosr    float64
	sinr    float64
}

func newProjection(p Params) projection {
	ra := p.RA * math.Pi / 180
	dec := p.Dec * math.Pi / 180
	sa, ca := math.Sincos(ra)
	sd, cd := math.Sincos(dec)
	rot := -p.Rotation * math.Pi / 180
	// WCS defaults LONPOLE to 0 instead of 180 only when the centre is
	// exactly the north pole, which turns a hips2fits pole view half a turn.
	if p.Dec >= 90 {
		rot += math.Pi
	}
	sr, cr := math.Sincos(rot)
	// hips2fits puts CRPIX at (w/2, h/2) in FITS pixels, which sits half a
	// pixel left of and below the image centre.
	return projection{
		c:    [3]float64{cd * ca, cd * sa, sd},
		e:    [3]float64{-sa, ca, 0},
		n:    [3]float64{-sd * ca, -sd * sa, cd},
		cx:   float64(p.Width)/2 - 0.5,
		cy:   float64(p.Height)/2 + 0.5,
		step: p.step(),
		cosr: cr,
		sinr: sr,
	}
}

// dir is the unnormalised direction of the centre of output pixel (i, j), with
// north up and east left before rotation.
func (pr projection) dir(i, j int) [3]float64 {
	x := (pr.cx - (float64(i) + 0.5)) * pr.step
	y := (pr.cy - (float64(j) + 0.5)) * pr.step
	xi := x*pr.cosr - y*pr.sinr
	eta := x*pr.sinr + y*pr.cosr
	return [3]float64{
		pr.c[0] + xi*pr.e[0] + eta*pr.n[0],
		pr.c[1] + xi*pr.e[1] + eta*pr.n[1],
		pr.c[2] + xi*pr.e[2] + eta*pr.n[2],
	}
}

func compact(v int64) int64 {
	v &= 0x5555555555555555
	v = (v | v>>1) & 0x3333333333333333
	v = (v | v>>2) & 0x0f0f0f0f0f0f0f0f
	v = (v | v>>4) & 0x00ff00ff00ff00ff
	v = (v | v>>8) & 0x0000ffff0000ffff
	v = (v | v>>16) & 0x00000000ffffffff
	return v
}

// tilePixel maps the low bits of a nested pixel index to its column and row
// in the tile's JPEG, row 0 at the top: the odd bits give the column and the
// even bits the row.
func tilePixel(sub int64) (col, row int) {
	return int(compact(sub >> 1)), int(compact(sub))
}

type tile struct {
	key tileKey
	rgb []byte
}

// Render draws p from the tiles of the coarsest fine-enough mirrored order and
// encodes it as JPEG. It returns ErrNoMirrorOrder when p needs a finer order.
func (r *Renderer) Render(ctx context.Context, p Params) ([]byte, error) {
	order, ok := r.Order(p)
	if !ok {
		return nil, ErrNoMirrorOrder
	}
	r.init()
	select {
	case r.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-r.slots }()
	nside := int64(1) << (order + tileOrderBits)
	pr := newProjection(p)
	w, h := p.Width, p.Height
	pix := make([]int64, w*h)
	need := map[int64]struct{}{}
	for j := range h {
		for i := range w {
			ip := healpix.Vec2PixNest(nside, pr.dir(i, j))
			pix[j*w+i] = ip
			need[ip>>tilePixelBits] = struct{}{}
		}
	}
	tiles, err := r.load(ctx, order, need)
	if err != nil {
		return nil, err
	}
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for n, ip := range pix {
		t := tiles[ip>>tilePixelBits]
		col, row := tilePixel(ip & (1<<tilePixelBits - 1))
		src := t.rgb[(row*TileWidth+col)*3:]
		dst := out.Pix[n*4:]
		dst[0], dst[1], dst[2], dst[3] = src[0], src[1], src[2], 0xff
	}
	q := r.Quality
	if q <= 0 {
		q = defaultQuality
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: q}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (r *Renderer) load(ctx context.Context, order int, need map[int64]struct{}) (map[int64]*tile, error) {
	var mu sync.Mutex
	got := make(map[int64]*tile, len(need))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(tileLoadWorkers)
	for npix := range need {
		g.Go(func() error {
			if err := gctx.Err(); err != nil {
				return err
			}
			t, err := r.tile(gctx, tileKey{order: order, npix: npix})
			if err != nil {
				return err
			}
			mu.Lock()
			got[npix] = t
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return got, nil
}

func (r *Renderer) tile(ctx context.Context, k tileKey) (*tile, error) {
	if t := r.cache.get(k); t != nil {
		return t, nil
	}
	flight := r.group.DoChan(k.String(), func() (any, error) {
		if t := r.cache.get(k); t != nil {
			return t, nil
		}
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), tileTimeout)
		defer cancel()
		b, err := r.Store.Tile(fctx, k.order, k.npix)
		if err != nil {
			return nil, fmt.Errorf("survey tile %s: %w", k, err)
		}
		t, err := decodeTile(k, b)
		if err != nil {
			return nil, err
		}
		r.cache.put(t)
		return t, nil
	})
	var res singleflight.Result
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res = <-flight:
	}
	if res.Err != nil {
		return nil, res.Err
	}
	t, ok := res.Val.(*tile)
	if !ok {
		return nil, fmt.Errorf("survey tile %s did not load", k)
	}
	return t, nil
}

func decodeTile(k tileKey, b []byte) (*tile, error) {
	img, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("survey tile %s: %w", k, err)
	}
	bd := img.Bounds()
	if bd.Dx() != TileWidth || bd.Dy() != TileWidth {
		return nil, fmt.Errorf("survey tile %s is %dx%d, not %dx%d", k, bd.Dx(), bd.Dy(), TileWidth, TileWidth)
	}
	rgb := make([]byte, tileBytes)
	ycc, isYCC := img.(*image.YCbCr)
	for y := range TileWidth {
		for x := range TileWidth {
			o := (y*TileWidth + x) * 3
			px, py := bd.Min.X+x, bd.Min.Y+y
			if isYCC {
				ci := ycc.COffset(px, py)
				rgb[o], rgb[o+1], rgb[o+2] = color.YCbCrToRGB(ycc.Y[ycc.YOffset(px, py)], ycc.Cb[ci], ycc.Cr[ci])
				continue
			}
			if c, ok := color.RGBAModel.Convert(img.At(px, py)).(color.RGBA); ok {
				rgb[o], rgb[o+1], rgb[o+2] = c.R, c.G, c.B
			}
		}
	}
	return &tile{key: k, rgb: rgb}, nil
}

type tileKey struct {
	order int
	npix  int64
}

func (k tileKey) String() string { return TilePath(k.order, k.npix) }

// TilePath is a tile's path inside a HiPS survey.
func TilePath(order int, npix int64) string {
	return fmt.Sprintf("Norder%d/Dir%d/Npix%d.jpg", order, npix/10000*10000, npix)
}

type tileCache struct {
	mu    sync.Mutex
	limit int64
	used  int64
	ll    *list.List
	items map[tileKey]*list.Element
}

func newTileCache(limit int64) *tileCache {
	return &tileCache{limit: limit, ll: list.New(), items: map[tileKey]*list.Element{}}
}

func (c *tileCache) get(k tileKey) *tile {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[k]; ok {
		c.ll.MoveToFront(e)
		t, _ := e.Value.(*tile)
		return t
	}
	return nil
}

func (c *tileCache) put(t *tile) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[t.key]; ok {
		c.ll.MoveToFront(e)
		return
	}
	c.items[t.key] = c.ll.PushFront(t)
	c.used += int64(len(t.rgb))
	for c.used > c.limit && c.ll.Len() > 1 {
		e := c.ll.Back()
		c.ll.Remove(e)
		if old, ok := e.Value.(*tile); ok {
			delete(c.items, old.key)
			c.used -= int64(len(old.rgb))
		}
	}
}
