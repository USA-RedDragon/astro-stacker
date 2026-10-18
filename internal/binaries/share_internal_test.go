package binaries

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/testdb"
)

func TestLayoutKeepsTheStarClear(t *testing.T) {
	t.Parallel()
	const w, h = 1600.0, 1000.0
	spots := map[string][2]float64{
		"top left": {6, 6}, "top": {800, 6}, "top right": {1594, 6},
		"left": {6, 500}, "centre": {800, 500}, "right": {1594, 500},
		"bottom left": {6, 994}, "bottom": {800, 994}, "bottom right": {1594, 994},
		"near the top left inset spot": {300, 260}, "near the bottom right inset spot": {1300, 760},
	}
	for name, p := range spots {
		for _, track := range []bool{false, true} {
			m := Mark{X: p[0], Y: p[1]}
			if track {
				m = Mark{X: p[0], Y: p[1], X1: clamp(p[0]+40, 0, w), Y1: clamp(p[1]+25, 0, h), Track: true}
			}
			l, err := layoutFor(w, h, m, 380, 520)
			if err != nil {
				t.Errorf("%s (track %v): %v", name, track, err)
				continue
			}
			cx, cy := m.centre()
			if grow(l.Inset, l.R).contains(cx, cy) || l.Inset.overlaps(l.Mark) {
				t.Errorf("%s (track %v): the inset %+v covers the mark %+v", name, track, l.Inset, l.Mark)
			}
			for _, b := range []box{l.Label, l.Name} {
				if !b.inside(w, h) {
					t.Errorf("%s (track %v): text %+v runs off the picture", name, track, b)
				}
				if b.overlaps(l.Mark) || b.contains(cx, cy) {
					t.Errorf("%s (track %v): text %+v covers the mark %+v", name, track, b, l.Mark)
				}
				if b.overlaps(l.Inset) {
					t.Errorf("%s (track %v): text %+v covers the inset %+v", name, track, b, l.Inset)
				}
			}
			if l.Label.overlaps(l.Name) {
				t.Errorf("%s (track %v): the label and the name overlap", name, track)
			}
			if !l.Inset.inside(w, h) || !l.Source.inside(w, h) {
				t.Errorf("%s (track %v): inset %+v or its source %+v off the picture", name, track, l.Inset, l.Source)
			}
		}
	}
}

func clamp(v, lo, hi float64) float64 { return max(lo, min(hi, v)) }

func noisy(w, h int, seed uint64) *image.RGBA {
	rng := rand.New(rand.NewPCG(seed, 2))
	greys := []uint8{20, 26, 32, 38, 44, 50}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		v := greys[rng.IntN(len(greys))]
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = v, v, v+5, 255
	}
	for range 300 {
		x, y := rng.IntN(w), rng.IntN(h)
		img.Set(x, y, color.RGBA{255, 250, 240, 255})
	}
	return img
}

func TestSharePictureIsASmallJPEG(t *testing.T) {
	t.Parallel()
	b, err := SharePicture(noisy(1600, 1000, 1), Mark{X: 1200, Y: 700}, "the new variable star", "Gaia DR3 123456789")
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != 1600 || len(b) > 1500<<10 {
		t.Errorf("%dx%d, %d bytes", img.Bounds().Dx(), img.Bounds().Dy(), len(b))
	}
	big, err := SharePicture(noisy(3000, 2000, 2), Mark{X: 100, Y: 100}, "the new variable star", "x")
	if err != nil {
		t.Fatal(err)
	}
	if img, _ := jpeg.Decode(bytes.NewReader(big)); img.Bounds().Dx() != maxShareWidth {
		t.Errorf("a 3000 px picture comes out %d wide", img.Bounds().Dx())
	}
}

type memObjects struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (o *memObjects) Get(_ context.Context, key string) ([]byte, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	b, ok := o.m[key]
	if !ok {
		return nil, errors.New("not found: " + key)
	}
	return b, nil
}

func (o *memObjects) Put(_ context.Context, key string, b []byte, _ string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.m[key] = b
	return nil
}

func (o *memObjects) RemovePrefix(_ context.Context, prefix string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	for k := range o.m {
		if strings.HasPrefix(k, prefix) {
			delete(o.m, k)
		}
	}
	return nil
}

func TestNewMasterMakesANewShareVersion(t *testing.T) {
	t.Parallel()
	db := testdb.SQLite(t, "share")
	if err := db.AutoMigrate(&app.DiscoverySource{}, &app.DiscoveryVet{}, &app.Cover{}, &app.TargetReference{}, &app.Stack{},
		&app.ShareImage{}, &app.BinaryFacts{}, &app.BinaryBook{}); err != nil {
		t.Fatal(err)
	}
	const object, w, h = "Made-up field", 800, 600
	scale := 2.0 / 3600
	wcs := &discoveries.WCS{CRPIX1: w / 2, CRPIX2: h / 2, CRVAL1: 150, CRVAL2: 40, CD: [2][2]float64{{-scale, 0}, {0, scale}}, H: h}
	ra, dec := wcs.SkyOf(200, 150)
	src := app.DiscoverySource{Object: object, Kind: app.DiscoveryKindChange, RA: ra, Dec: dec, Night: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}
	if err := db.Create(&src).Error; err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"Red", "Green", "Blue"} {
		k := "stacks/x/" + f + "/linear.bin"
		if err := db.Create(&app.Stack{Object: object, Filter: f, Width: w, Height: h, LinearKey: &k, EffectiveSeconds: 3600}).Error; err != nil {
			t.Fatal(err)
		}
	}
	objs := &memObjects{m: map[string][]byte{}}
	cover := func(seed uint64, at time.Time) {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, noisy(w, h, seed), nil); err != nil {
			t.Fatal(err)
		}
		objs.m["stacks/x/color.jpg"] = buf.Bytes()
		c := app.Cover{Subject: object, Palette: "RGB", PreviewKey: "stacks/x/color.jpg", UpdatedAt: at, Version: 1}
		if err := db.Where("subject = ?", object).Assign(c).FirstOrCreate(&app.Cover{}).Error; err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 10, 18, 3, 0, 0, 0, time.UTC)
	svc := &Service{DB: db, Objects: objs, Now: func() time.Time { return now },
		Frame: func(context.Context, string) (*discoveries.WCS, int, int, error) { return wcs, w, h, nil }}
	ctx := context.Background()
	if sh, err := svc.Share(ctx, src.ID); err != nil || sh.State != SharePending {
		t.Fatalf("first ask: %+v %v", sh, err)
	}
	cover(1, now.Add(-time.Hour))
	run := func() app.ShareImage {
		t.Helper()
		work, err := svc.shareWork(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, wk := range work {
			if err := svc.makePictures(ctx, wk); err != nil {
				t.Fatal(err)
			}
		}
		sh, err := svc.Share(ctx, src.ID)
		if err != nil {
			t.Fatal(err)
		}
		return sh
	}
	first := run()
	if first.State != ShareReady || first.Version != 1 {
		t.Fatalf("first picture %+v", first)
	}
	if again := run(); again.Version != 1 {
		t.Errorf("an unchanged master made version %d", again.Version)
	}
	cover(2, now)
	second := run()
	if second.State != ShareReady || second.Version != 2 || second.Key == first.Key {
		t.Errorf("after a new master: %+v, first %+v", second, first)
	}
	if _, ok := objs.m[first.Key]; ok {
		t.Errorf("the old picture %s was kept", first.Key)
	}
	b, sh, err := svc.ShareBytes(ctx, src.ID)
	if err != nil || sh.Version != 2 || len(b) == 0 {
		t.Errorf("bytes %d, %+v, %v", len(b), sh, err)
	}
	if !strings.Contains(fmt.Sprint(sh.Key), "/v2.jpg") {
		t.Errorf("key %s", sh.Key)
	}
}
