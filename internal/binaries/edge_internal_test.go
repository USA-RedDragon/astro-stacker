package binaries

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"image/jpeg"
	"io"
	"math"
	"math/rand/v2"
	"os"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/stacking"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/testdb"
	"github.com/klauspost/compress/zstd"
)

func ic4604Panel6WCS(t *testing.T) (*discoveries.WCS, string) {
	t.Helper()
	b, err := os.ReadFile("testdata/wcs_ic4604_panel6.json.zst")
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zstd.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	var cards []struct{ Name, Value string }
	if err := json.Unmarshal(raw, &cards); err != nil {
		t.Fatal(err)
	}
	kw := map[string]string{}
	for _, c := range cards {
		if _, dup := kw[c.Name]; !dup {
			kw[c.Name] = c.Value
		}
	}
	w, err := discoveries.ParseWCS(kw, 4176)
	if err != nil {
		t.Fatal(err)
	}
	return w, string(raw)
}

const greenFilter = "Green"

// ic4604Panel6 is find #23991 and the IC 4604 Panel 6 masters' sizes,
// well-covered crops and effective exposures as stored.
const ic4604Panel6 = "IC 4604 Panel 6"

const ic4604Panel6RA, ic4604Panel6Dec = 244.52983049964837, -24.011487317636483

func ic4604Panel6Stacks() []app.Stack {
	rows := []struct {
		filter     string
		x, y, w, h int
		eff        float64
	}{{greenFilter, 32, 48, 6184, 4040, 5283}, {"Blue", 32, 24, 6184, 4056, 6339}, {"Luminance", 24, 48, 6192, 4048, 4515}, {redFilter, 32, 32, 6184, 4056, 6126}}
	out := make([]app.Stack, 0, len(rows))
	for _, r := range rows {
		k := "stacks/" + ic4604Panel6 + "/" + r.filter + "/linear.bin"
		out = append(out, app.Stack{Object: ic4604Panel6, Filter: r.filter, Width: 6248, Height: 4176, CropX: r.x, CropY: r.y, CropW: r.w, CropH: r.h, EffectiveSeconds: r.eff, LinearKey: &k})
	}
	return out
}

const previewW, previewH = 1562, 1044

func linearPreview(seed uint64, starX, starY int) []byte {
	rng := rand.New(rand.NewPCG(seed, 3))
	data := make([]float32, previewW*previewH)
	for i := range data {
		data[i] = 0.002 + 0.0005*rng.Float32()
	}
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			data[(starY+dy)*previewW+starX+dx] = 0.02
		}
	}
	var buf bytes.Buffer
	buf.WriteString(stacking.LinearMagic)
	_ = binary.Write(&buf, binary.LittleEndian, [2]uint32{previewW, previewH})
	_ = binary.Write(&buf, binary.LittleEndian, data)
	return buf.Bytes()
}

func TestFind23991IsJustAboveTheCover(t *testing.T) {
	t.Parallel()
	wcs, _ := ic4604Panel6WCS(t)
	x, y, ok := wcs.PixelOf(ic4604Panel6RA, ic4604Panel6Dec)
	if !ok || math.Abs(x-2590.8) > 0.3 || math.Abs(y-47.6) > 0.3 {
		t.Fatalf("find #23991 lands at master %.1f, %.1f; the detections put it at 2590.1 to 2590.9, 47.3 to 47.9", x, y)
	}
	frame, ok := stacking.CoverFrameOf(ic4604Panel6, ic4604Panel6Stacks())
	if want := (stacking.CoverFrame{Bin: 4, X: 8, Y: 12, W: 1546, H: 1008, Filters: "Blue,Green,Red"}); !ok || frame != want {
		t.Fatalf("cover frame %+v, want %+v", frame, want)
	}
	if cx, cy := frame.Pixel(x, y); math.Abs(cx-639.3) > 0.3 || math.Abs(cy+0.5) > 0.3 {
		t.Fatalf("on the cover at %.1f, %.1f; want 639.3, -0.5, just above its top edge", cx, cy)
	}
}

func TestStarOnTheMastersEdgeGetsAWholeFramePicture(t *testing.T) {
	t.Parallel()
	wcs, cards := ic4604Panel6WCS(t)
	db := testdb.SQLite(t, "edge")
	if err := db.AutoMigrate(&app.DiscoverySource{}, &app.DiscoveryVet{}, &app.Cover{}, &app.TargetReference{}, &app.Stack{},
		&app.ShareImage{}, &app.BinaryFacts{}, &app.BinaryBook{}); err != nil {
		t.Fatal(err)
	}
	src := app.DiscoverySource{Object: ic4604Panel6, Kind: app.DiscoveryKindChange, RA: ic4604Panel6RA, Dec: ic4604Panel6Dec, Night: time.Date(2025, 5, 21, 0, 0, 0, 0, time.UTC)}
	if err := db.Create(&src).Error; err != nil {
		t.Fatal(err)
	}
	objs := &memObjects{m: map[string][]byte{}}
	for i, st := range ic4604Panel6Stacks() {
		objs.m[*st.LinearKey] = linearPreview(uint64(i+1), 647, 11)
		if err := db.Create(&st).Error; err != nil {
			t.Fatal(err)
		}
	}
	var cover bytes.Buffer
	if err := jpeg.Encode(&cover, noisy(1546, 1008, 1), nil); err != nil {
		t.Fatal(err)
	}
	key := "stacks/" + ic4604Panel6 + "/color.jpg"
	objs.m[key] = cover.Bytes()
	if err := db.Create(&app.Cover{Subject: ic4604Panel6, Palette: "LRGB", PreviewKey: key, UpdatedAt: time.Date(2026, 10, 17, 10, 37, 58, 658336000, time.UTC), Version: 8}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&app.TargetReference{Object: ic4604Panel6, WCS: &cards}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&app.ShareImage{SourceID: src.ID, Stamp: "0fb6a3d6c1bfd86004cc4366", State: ShareNone, Note: "the star is outside the colour picture"}).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 21, 3, 0, 0, 0, time.UTC)
	svc := &Service{DB: db, Objects: objs, Now: func() time.Time { return now },
		Frame: func(context.Context, string) (*discoveries.WCS, int, int, error) { return wcs, 6248, 4176, nil }}
	ctx := context.Background()
	work, err := svc.shareWork(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(work) != 1 {
		t.Fatalf("the picture that failed under the old method is not made again: %d to make", len(work))
	}
	img, whole, mark, err := svc.wholeOf(ctx, work[0], wcs)
	if err != nil {
		t.Fatal(err)
	}
	if want := (stacking.CoverFrame{Bin: 4, W: previewW, H: previewH, Filters: "Blue,Green,Red"}); whole != want || img.Bounds().Dx() != previewW || img.Bounds().Dy() != previewH {
		t.Fatalf("whole frame %+v, picture %v", whole, img.Bounds())
	}
	if math.Abs(mark.X-647.3) > 0.3 || math.Abs(mark.Y-11.5) > 0.3 {
		t.Fatalf("on the whole frame at %.1f, %.1f; want 647.3, 11.5", mark.X, mark.Y)
	}
	if r, _, _, _ := img.At(647, 11).RGBA(); r>>8 < 200 {
		t.Errorf("the star is not bright at its mark on the whole frame: red %d", r>>8)
	}
	if err := svc.makePictures(ctx, work[0]); err != nil {
		t.Fatal(err)
	}
	sh, err := svc.Share(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sh.State != ShareReady || sh.Note != edgeNote(work[0]) || sh.Key == "" {
		t.Fatalf("share %+v", sh)
	}
	if again, _ := svc.shareWork(ctx, nil); len(again) != 0 {
		t.Errorf("a made picture is made again: %d", len(again))
	}
}
