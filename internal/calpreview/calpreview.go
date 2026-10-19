// Package calpreview calibrates a light for its quick-look preview with the
// best calibration masters the stacker has already built, chosen by the
// stacker's own matching rules (calmatch). It never builds a master or waits
// for one: when the best set has no master yet, the next best built one is
// used and the caption says so.
//
// The light and the masters are binned to the preview's size first. Bias and
// dark subtraction are linear, so binning before or after gives the same
// result; the flat division differs only below the binned pixel scale.
package calpreview

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/calmatch"
	"github.com/USA-RedDragon/astro-stacker/internal/coverage"
	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/preview"
	"github.com/USA-RedDragon/astro-stacker/internal/stacking"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/minio/minio-go/v7"
	"gorm.io/gorm"
)

// cacheSize is how many binned masters are kept in memory. At a 1600 px
// preview width one is about 7 MB.
const cacheSize = 24

// setsTTL is how long the calibration sets and dust epochs are reused
// between lights.
const setsTTL = time.Minute

// Calibrator calibrates binned lights with built masters.
type Calibrator struct {
	DB     *gorm.DB
	Client *minio.Client
	// Source is the bucket of raw frames and imported masters, Dest the
	// processed bucket the stacker uploads its masters to.
	Source, Dest string
	// WorkDir is the stacker's work directory; a master already there is
	// read instead of downloaded.
	WorkDir string

	mu      sync.Mutex
	masters map[string]*master
	order   []string
	keys    map[string]builtKey
	sets    []calmatch.Set
	epochs  calmatch.Epochs
	setsAt  time.Time
}

type master struct {
	once sync.Once
	im   *imagedata.Image
	err  error
}

type builtKey struct {
	key, object string
}

// Result is a calibrated light, or why it was not calibrated.
type Result struct {
	// Image is the calibrated, binned light; nil when it was not
	// calibrated.
	Image *imagedata.Image
	// Note says in plain words what it was calibrated with, or why not.
	Note string
	// Loaded says, for each master type read from disk or the bucket
	// rather than memory, how long that took.
	Loaded map[string]time.Duration
}

type chosen struct {
	match calmatch.Match
	key   builtKey
	// skipped is the better set that has no master yet, if any.
	skipped *calmatch.Set
}

// Calibrate calibrates a frame's light, already binned by factor.
func (c *Calibrator) Calibrate(ctx context.Context, f app.Frame, light *imagedata.Image, factor int) (Result, error) {
	if strings.Contains(path.Base(f.Key), "_cal.") {
		return Result{Note: "Not calibrated here: the observatory calibrated this sub already"}, nil
	}
	if f.Night == nil || f.Exposure == nil || *f.Exposure <= 0 {
		return Result{Note: "Not calibrated: the sub has no night or exposure in its header"}, nil
	}
	if light.C != 1 {
		return Result{Note: "Not calibrated: the sub is not a single-channel frame"}, nil
	}
	sets, epochs, err := c.loadSets(ctx)
	if err != nil {
		return Result{}, err
	}
	g := calmatch.Group{
		Night: *f.Night, Filter: f.Filter, Exposure: *f.Exposure, Gain: val(f.Gain), Offset: val(f.Offset),
		SetTemp: val(f.SetTemp), BinX: val(f.BinX), Rotator: val(f.Rotator), Epochs: epochs,
	}
	bias, err := c.choose(ctx, g, sets, func(r calmatch.Result) calmatch.Match { return r.Bias })
	if err != nil {
		return Result{}, err
	}
	dark, err := c.choose(ctx, g, sets, func(r calmatch.Result) calmatch.Match { return r.Dark })
	if err != nil {
		return Result{}, err
	}
	flat, err := c.choose(ctx, g, sets, func(r calmatch.Result) calmatch.Match { return r.Flat })
	if err != nil {
		return Result{}, err
	}
	if bias.match.Set == nil {
		return Result{Note: "Not calibrated: no bias master for this gain and offset yet"}, nil
	}
	if dark.match.Set == nil && flat.match.Set == nil {
		return Result{Note: "Not calibrated: no matching dark or flat master yet"}, nil
	}

	loaded := map[string]time.Duration{}
	b, err := c.load(ctx, bias, nil, factor, light, loaded)
	if err != nil {
		return Result{}, fmt.Errorf("bias: %w", err)
	}
	out := &imagedata.Image{W: light.W, H: light.H, C: 1, Data: slices.Clone(light.Data)}
	for i := range out.Data {
		out.Data[i] -= b.Data[i]
	}
	k := 0.0
	if dark.match.Set != nil {
		d, err := c.load(ctx, dark, b, factor, light, loaded)
		if err != nil {
			return Result{}, fmt.Errorf("dark: %w", err)
		}
		k = DarkScale(out.Data, d.Data, light.W, light.H)
		for i := range out.Data {
			out.Data[i] -= float32(k) * d.Data[i]
		}
	}
	if flat.match.Set != nil {
		fl, err := c.load(ctx, flat, nil, factor, light, loaded)
		if err != nil {
			return Result{}, fmt.Errorf("flat: %w", err)
		}
		divideFlat(out.Data, fl.Data)
	}
	return Result{Image: out, Note: caption(bias, dark, flat), Loaded: loaded}, nil
}

// choose picks the best set of one type that has a master built, by
// calmatch's rules, passing over better sets whose master isn't built yet.
func (c *Calibrator) choose(ctx context.Context, g calmatch.Group, sets []calmatch.Set, pick func(calmatch.Result) calmatch.Match) (chosen, error) {
	cands := slices.Clone(sets)
	var out chosen
	for {
		m := pick(calmatch.Choose(g, cands))
		if m.Set == nil {
			return out, nil
		}
		key, ok, err := c.builtKey(ctx, *m.Set)
		if err != nil {
			return out, err
		}
		if ok {
			out.match, out.key = m, key
			return out, nil
		}
		if out.skipped == nil {
			s := *m.Set
			out.skipped = &s
		}
		id := identity(*m.Set)
		cands = slices.DeleteFunc(cands, func(s calmatch.Set) bool { return identity(s) == id })
	}
}

func (c *Calibrator) loadSets(ctx context.Context) ([]calmatch.Set, calmatch.Epochs, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sets != nil && time.Since(c.setsAt) < setsTTL {
		return c.sets, c.epochs, nil
	}
	sets, err := coverage.Sets(ctx, c.DB)
	if err != nil {
		return nil, nil, fmt.Errorf("load calibration sets: %w", err)
	}
	epochs, err := coverage.Epochs(ctx, c.DB)
	if err != nil {
		return nil, nil, err
	}
	c.sets, c.epochs, c.setsAt = sets, epochs, time.Now()
	return sets, epochs, nil
}

// identity names a set together with what makes its frames change, so a set
// that grows is looked up again.
func identity(s calmatch.Set) string {
	num := func(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }
	return strings.Join([]string{s.Type, s.Night.Format(time.DateOnly), s.Object, s.Filter, num(s.Exposure), num(s.Gain),
		num(s.Offset), num(s.SetTemp), num(s.BinX), num(s.Rotator), strconv.Itoa(s.Count),
		s.Uploaded.UTC().Format(time.RFC3339Nano), s.Master}, "\x00")
}

// builtKey finds the master built from a set: its object key in the processed
// bucket, or the source key of a master made elsewhere.
func (c *Calibrator) builtKey(ctx context.Context, s calmatch.Set) (builtKey, bool, error) {
	if s.Master != "" {
		return builtKey{key: "imported:" + s.Master, object: s.Master}, true, nil
	}
	id := identity(s)
	c.mu.Lock()
	k, ok := c.keys[id]
	c.mu.Unlock()
	if ok {
		return k, k.object != "", nil
	}
	frames, err := coverage.SetFrames(ctx, c.DB, s)
	if err != nil {
		return builtKey{}, false, err
	}
	setKey := stacking.MasterSetKey(s.Type, frames)
	var cm app.CalibrationMaster
	err = c.DB.WithContext(ctx).Select("object_key").Where("set_key = ?", setKey).Take(&cm).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		k = builtKey{key: setKey}
	case err != nil:
		return builtKey{}, false, err
	default:
		k = builtKey{key: setKey, object: cm.ObjectKey}
	}
	c.mu.Lock()
	if c.keys == nil {
		c.keys = map[string]builtKey{}
	}
	c.keys[id] = k
	c.mu.Unlock()
	return k, k.object != "", nil
}

// load returns a master binned by factor, from memory when it can. An
// imported dark has bias subtracted, as the stacker's own darks do.
func (c *Calibrator) load(ctx context.Context, ch chosen, bias *imagedata.Image, factor int, light *imagedata.Image, loaded map[string]time.Duration) (*imagedata.Image, error) {
	name := ch.key.key + "@" + strconv.Itoa(factor)
	c.mu.Lock()
	if c.masters == nil {
		c.masters = map[string]*master{}
	}
	m, ok := c.masters[name]
	if !ok {
		m = &master{}
		c.masters[name] = m
		c.order = append(c.order, name)
		for len(c.order) > cacheSize {
			delete(c.masters, c.order[0])
			c.order = c.order[1:]
		}
	} else {
		i := slices.Index(c.order, name)
		if i >= 0 {
			c.order = append(slices.Delete(c.order, i, i+1), name)
		}
	}
	c.mu.Unlock()
	m.once.Do(func() {
		start := time.Now()
		m.im, m.err = c.read(ctx, ch, bias, factor)
		loaded[ch.match.Set.Type] = time.Since(start)
	})
	if m.err != nil {
		c.mu.Lock()
		if c.masters[name] == m {
			delete(c.masters, name)
			c.order = slices.DeleteFunc(c.order, func(s string) bool { return s == name })
		}
		c.mu.Unlock()
		return nil, m.err
	}
	if m.im.W != light.W || m.im.H != light.H || m.im.C != 1 {
		return nil, fmt.Errorf("master is %dx%d, the sub %dx%d", m.im.W, m.im.H, light.W, light.H)
	}
	return m.im, nil
}

func (c *Calibrator) read(ctx context.Context, ch chosen, bias *imagedata.Image, factor int) (*imagedata.Image, error) {
	s := *ch.match.Set
	var local, bucket string
	if s.Master != "" {
		local, bucket = stacking.ImportedMasterFile(c.WorkDir, s), c.Source
	} else {
		local, bucket = stacking.BuiltMasterFile(c.WorkDir, ch.key.key), c.Dest
	}
	b, err := os.ReadFile(local)
	fromSource := false
	if err != nil {
		if b, err = c.get(ctx, bucket, ch.key.object); err != nil {
			return nil, err
		}
		fromSource = s.Master != ""
	}
	im, err := imagedata.Decode(b)
	if err != nil {
		return nil, err
	}
	if im.C != 1 {
		return nil, fmt.Errorf("master has %d channels", im.C)
	}
	binned := preview.Bin(im, factor)
	if binned == im {
		binned = &imagedata.Image{W: im.W, H: im.H, C: 1, Data: slices.Clone(im.Data)}
	}
	if fromSource && s.Type == "DARK" {
		if bias == nil {
			return nil, errors.New("no bias to take out of the imported dark")
		}
		if bias.W != binned.W || bias.H != binned.H {
			return nil, fmt.Errorf("bias is %dx%d, dark %dx%d", bias.W, bias.H, binned.W, binned.H)
		}
		for i := range binned.Data {
			binned.Data[i] -= bias.Data[i]
		}
	}
	return binned, nil
}

func (c *Calibrator) get(ctx context.Context, bucket, key string) ([]byte, error) {
	obj, err := c.Client.GetObject(ctx, bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer obj.Close()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, obj); err != nil {
		return nil, fmt.Errorf("download %s: %w", key, err)
	}
	return buf.Bytes(), nil
}

// DarkScale is the factor for a bias-subtracted dark that leaves the least
// noise in a bias-subtracted light, found by golden-section search over
// [0, 2] on the central quarter, the way Siril's dark optimization (-opt)
// does when the stacker calibrates.
func DarkScale(light, dark []float32, w, h int) float64 {
	x0, x1, y0, y1 := w/4, w-w/4, h/4, h-h/4
	n := (x1 - x0) * (y1 - y0)
	if n <= 0 {
		return 1
	}
	l, d := make([]float32, 0, n), make([]float32, 0, n)
	for y := y0; y < y1; y++ {
		l = append(l, light[y*w+x0:y*w+x1]...)
		d = append(d, dark[y*w+x0:y*w+x1]...)
	}
	buf := make([]float32, n)
	noise := func(k float64) float64 {
		for i := range l {
			buf[i] = l[i] - float32(k)*d[i]
		}
		return madSigma(buf)
	}
	const phi = 0.6180339887498949
	a, b := 0.0, 2.0
	x1k, x2k := b-phi*(b-a), a+phi*(b-a)
	f1, f2 := noise(x1k), noise(x2k)
	for b-a > 1e-3 {
		if f1 < f2 {
			b, x2k, f2 = x2k, x1k, f1
			x1k = b - phi*(b-a)
			f1 = noise(x1k)
		} else {
			a, x1k, f1 = x1k, x2k, f2
			x2k = a + phi*(b-a)
			f2 = noise(x2k)
		}
	}
	return (a + b) / 2
}

// madSigma is the MAD-based standard deviation; it reorders p.
func madSigma(p []float32) float64 {
	med := median(p)
	for i := range p {
		p[i] = float32(math.Abs(float64(p[i]) - med))
	}
	return 1.4826 * median(p)
}

func median(p []float32) float64 {
	k := len(p) / 2
	return float64(nth(p, k))
}

func nth(p []float32, k int) float32 {
	lo, hi := 0, len(p)-1
	for lo < hi {
		pivot := p[(lo+hi)/2]
		i, j := lo, hi
		for i <= j {
			for p[i] < pivot {
				i++
			}
			for p[j] > pivot {
				j--
			}
			if i <= j {
				p[i], p[j] = p[j], p[i]
				i++
				j--
			}
		}
		switch {
		case k <= j:
			hi = j
		case k >= i:
			lo = i
		default:
			return p[k]
		}
	}
	return p[k]
}

// divideFlat divides by the flat normalized to its mean, as Siril does.
func divideFlat(data, flat []float32) {
	var sum float64
	n := 0
	for _, v := range flat {
		if v > 0 {
			sum += float64(v)
			n++
		}
	}
	if n == 0 {
		return
	}
	mean := float32(sum / float64(n))
	for i, v := range flat {
		if v > 0 {
			data[i] /= v / mean
		}
	}
}

func val(p *float64) float64 {
	if p == nil {
		return math.NaN()
	}
	return *p
}

func shortDate(t time.Time) string { return t.Format("Jan 2") }

func fmtNum(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// caption says in plain words what the light was calibrated with.
func caption(bias, dark, flat chosen) string {
	var with []string
	var notes []string
	if dark.match.Set != nil {
		s := dark.match.Set
		d := "the "
		if !math.IsNaN(s.SetTemp) {
			d += fmtNum(s.SetTemp) + " C "
		}
		d += fmtNum(s.Exposure) + " s dark"
		if dark.match.Scaled {
			d += " (scaled to fit)"
		}
		with = append(with, d)
	}
	if flat.match.Set != nil {
		f := shortDate(flat.match.Set.Night) + " flats"
		var off []string
		if flat.match.RotationMismatch {
			off = append(off, fmt.Sprintf("rotator %.1f° off", flat.match.RotationOff))
		}
		if flat.match.OtherGain {
			off = append(off, "other gain")
		}
		if flat.match.OtherEpoch {
			off = append(off, "from another dust period")
		}
		if len(off) > 0 {
			f += " (" + strings.Join(off, ", ") + ")"
		}
		with = append(with, f)
	}
	with = append(with, shortDate(bias.match.Set.Night)+" bias")
	if dark.match.Set == nil {
		notes = append(notes, "no matching dark master yet")
	}
	if flat.match.Set == nil {
		notes = append(notes, "no matching flat master yet")
	}
	for _, ch := range []chosen{flat, dark, bias} {
		if ch.skipped != nil && ch.match.Set != nil {
			notes = append(notes, skippedNote(*ch.skipped))
		}
	}
	out := "Calibrated with " + joinAnd(with)
	if len(notes) > 0 {
		out += "; " + strings.Join(notes, "; ")
	}
	return out
}

func skippedNote(s calmatch.Set) string {
	switch s.Type {
	case "FLAT":
		return "the " + shortDate(s.Night) + " flats are not stacked yet"
	case "DARK":
		t := ""
		if !math.IsNaN(s.SetTemp) {
			t = fmtNum(s.SetTemp) + " C "
		}
		return "the " + t + fmtNum(s.Exposure) + " s darks are not stacked yet"
	default:
		return "the " + shortDate(s.Night) + " bias frames are not stacked yet"
	}
}

func joinAnd(p []string) string {
	switch len(p) {
	case 0:
		return ""
	case 1:
		return p[0]
	}
	return strings.Join(p[:len(p)-1], ", ") + " and " + p[len(p)-1]
}
