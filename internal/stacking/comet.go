package stacking

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/events"
	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/indexer"
	"github.com/USA-RedDragon/astro-stacker/internal/preview"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/minio/minio-go/v7"
)

// A comet moves against the stars, so a master aligned on the stars smears
// it. The comet master shifts each star-aligned sub so the comet stays
// where it was in the target's reference sub, from JPL Horizons' positions
// and the master's plate solution, and stacks them with rejection, which
// removes the stars now trailing through the frame.

// StageComet is reported while a comet master is being stacked.
const StageComet = "comet"

// cometMedianMax is the most subs a comet master is stacked from in memory,
// against the per-pixel median: about 3 GB of 16-bit samples.
const cometMedianMax = 60

// cometAccumulator stacks comet-aligned subs against the per-pixel median.
func cometAccumulator(subs [][]float32, w, h int, exposure, weight float64, opts Options) *Accumulator {
	all := make([]memSub, len(subs))
	for i, s := range subs {
		all[i] = toMemSub(s, exposure, weight, opts.SaturationLevel)
	}
	return medianAnchored(all, w, h, cometOptions(opts))
}

// cometOptions are the stacking options for a comet master. Rejections
// aren't grown: in comet-aligned subs every star trails, and the grown
// rejections of neighbouring trails would cover whole areas.
func cometOptions(opts Options) Options {
	opts.RejectGrow = 0
	return opts
}

// cometName matches a comet's designation at the start of a target name:
// C/2025 R2 (SWAN), 12P/Pons-Brooks.
var cometName = regexp.MustCompile(`^(?:[CPDXI]/\d{4} [A-Z]{1,2}\d*|\d+[PDI])\b`)

// cometDesignation is the designation Horizons knows a comet target by.
func cometDesignation(object string) (string, bool) {
	d := cometName.FindString(object)
	return d, d != ""
}

// horizonsURL is JPL's Horizons API.
var horizonsURL = "https://ssd.jpl.nasa.gov/api/horizons.api"

// ephemeris is a comet's astrometric (ICRF) position by time.
type ephemeris struct {
	times   []time.Time
	ra, dec []float64
}

// at interpolates the position at t, which must be within the table.
func (e ephemeris) at(t time.Time) (ra, dec float64, ok bool) {
	for i := 1; i < len(e.times); i++ {
		if t.After(e.times[i]) {
			continue
		}
		t0, t1 := e.times[i-1], e.times[i]
		if t.Before(t0) {
			return 0, 0, false
		}
		f := t.Sub(t0).Seconds() / t1.Sub(t0).Seconds()
		ra0, ra1 := e.ra[i-1], e.ra[i]
		if ra1-ra0 > 180 { // across RA 0
			ra1 -= 360
		} else if ra0-ra1 > 180 {
			ra1 += 360
		}
		return math.Mod(ra0+f*(ra1-ra0)+360, 360), e.dec[i-1] + f*(e.dec[i]-e.dec[i-1]), true
	}
	return 0, 0, false
}

// horizons fetches a comet's positions each minute from from to to, seen
// from the site (east longitude and latitude in degrees, elevation in m).
func horizons(ctx context.Context, designation string, from, to time.Time, lon, lat, elev float64) (ephemeris, error) {
	q := url.Values{}
	for k, v := range map[string]string{
		"format": "json", "COMMAND": fmt.Sprintf("'DES=%s;CAP;NOFRAG'", designation),
		"OBJ_DATA": "'NO'", "MAKE_EPHEM": "'YES'", "EPHEM_TYPE": "'OBSERVER'",
		"CENTER": "'coord@399'", "COORD_TYPE": "'GEODETIC'",
		"SITE_COORD": fmt.Sprintf("'%.6f,%.6f,%.4f'", lon, lat, elev/1000),
		"START_TIME": "'" + from.UTC().Add(-time.Minute).Format("2006-01-02 15:04") + "'",
		"STOP_TIME":  "'" + to.UTC().Add(2*time.Minute).Format("2006-01-02 15:04") + "'",
		"STEP_SIZE":  "'1 m'", "QUANTITIES": "'1'", "ANG_FORMAT": "'DEG'", "CSV_FORMAT": "'YES'",
	} {
		q.Set(k, v)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, horizonsURL+"?"+q.Encode(), nil)
	if err != nil {
		return ephemeris{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ephemeris{}, fmt.Errorf("horizons: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return ephemeris{}, err
	}
	var r struct {
		Result string `json:"result"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return ephemeris{}, fmt.Errorf("horizons: %s: %w", resp.Status, err)
	}
	if r.Error != "" {
		return ephemeris{}, fmt.Errorf("horizons: %s", r.Error)
	}
	return parseEphemeris(r.Result)
}

// parseEphemeris reads the CSV rows between $$SOE and $$EOE:
// " 2025-Oct-20 02:50, , ,   283.85858,  -12.85657,".
func parseEphemeris(result string) (ephemeris, error) {
	var e ephemeris
	in := false
	sc := bufio.NewScanner(strings.NewReader(result))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "$$SOE":
			in = true
			continue
		case line == "$$EOE":
			in = false
		}
		if !in {
			continue
		}
		f := strings.Split(line, ",")
		if len(f) < 5 {
			continue
		}
		t, err := time.Parse("2006-Jan-02 15:04", strings.TrimSpace(f[0]))
		if err != nil {
			return e, fmt.Errorf("horizons time %q: %w", f[0], err)
		}
		ra, err1 := strconv.ParseFloat(strings.TrimSpace(f[3]), 64)
		dec, err2 := strconv.ParseFloat(strings.TrimSpace(f[4]), 64)
		if err1 != nil || err2 != nil {
			return e, fmt.Errorf("horizons position %q", line)
		}
		e.times = append(e.times, t)
		e.ra = append(e.ra, ra)
		e.dec = append(e.dec, dec)
	}
	if len(e.times) < 2 {
		if i := strings.Index(result, "No matches"); i >= 0 {
			return e, errors.New("horizons does not know this comet")
		}
		return e, errors.New("horizons returned no positions")
	}
	return e, nil
}

// cometsOnce stacks comet masters for comet targets whose star-aligned
// masters changed and have been left alone for MosaicQuiet.
func (p *Pipeline) cometsOnce(ctx context.Context) error {
	var stacks []app.Stack
	if err := p.db.WithContext(ctx).Where("subs >= 3 AND master_key IS NOT NULL").Find(&stacks).Error; err != nil {
		return err
	}
	for i := range stacks {
		s := &stacks[i]
		des, ok := cometDesignation(s.Object)
		if !ok || s.CometSignature == cometSignature(s) || time.Since(s.UpdatedAt) < p.opts.MosaicQuiet {
			continue
		}
		if !p.hold(s.Object) {
			continue
		}
		err := p.stackComet(ctx, s, des)
		p.release(s.Object)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Recorded so it isn't retried until the master changes.
			slog.Error("Comet master failed", "object", s.Object, "filter", s.Filter, "error", err)
			msg := err.Error()
			p.db.WithContext(ctx).Model(s).UpdateColumns(map[string]any{"comet_signature": cometSignature(s), "comet_error": msg})
		}
	}
	return nil
}

func cometSignature(s *app.Stack) string {
	h := sha256.Sum256(fmt.Appendf(nil, "%d\x00%d", s.Subs, s.UpdatedAt.UnixNano()))
	return hex.EncodeToString(h[:16])
}

// cometSub is a star-aligned sub and where the comet was when it was taken.
type cometSub struct {
	stored storedSub
	mid    time.Time
}

func (p *Pipeline) stackComet(ctx context.Context, stack *app.Stack, designation string) error {
	var tr app.TargetReference
	if err := p.db.WithContext(ctx).Where("object = ?", stack.Object).First(&tr).Error; err != nil {
		return fmt.Errorf("registration reference: %w", err)
	}
	if tr.WCS == nil {
		return errors.New("the target has no plate solution")
	}
	var cards []frameheader.Card
	if err := json.Unmarshal([]byte(*tr.WCS), &cards); err != nil {
		return err
	}
	kw := frameheader.Keywords{}
	for _, c := range cards {
		kw[c.Name] = c.Value
	}
	sky, err := wcsFromHeader(kw, stack.Width, stack.Height)
	if err != nil {
		return err
	}
	var ref app.Frame
	if err := p.db.WithContext(ctx).First(&ref, tr.FrameID).Error; err != nil {
		return fmt.Errorf("reference frame: %w", err)
	}
	refKW, err := indexer.ReadHeader(ctx, p.s3, p.source, minio.ObjectInfo{Key: ref.Key, Size: ref.Size})
	if err != nil {
		return fmt.Errorf("reference header: %w", err)
	}
	lat, lon, elev := refKW.Float("SITELAT"), refKW.Float("SITELONG"), refKW.Float("SITEELEV")
	if math.IsNaN(lat) || math.IsNaN(lon) {
		return errors.New("the reference sub has no site coordinates")
	}
	if math.IsNaN(elev) {
		elev = 0
	}
	if ref.DateObs == nil {
		return errors.New("the reference sub has no time")
	}
	// Every filter's comet master puts the comet where it was in the
	// target's reference sub, so they line up for colour.
	refTime := ref.DateObs.Add(time.Duration(val(ref.Exposure) / 2 * float64(time.Second)))

	var rows []struct {
		RegisteredKey string
		Exposure      float64
		Weight        float64
		DateObs       time.Time
	}
	if err := p.db.WithContext(ctx).Table("stack_frames sf").
		Select("sf.registered_key, sf.exposure, sf.weight, f.date_obs").
		Joins("JOIN frames f ON f.id = sf.frame_id").
		Where("sf.stack_id = ? AND sf.status = ? AND sf.registered_key IS NOT NULL AND f.date_obs IS NOT NULL", stack.ID, app.StackStatusAdded).
		Order("f.date_obs").Scan(&rows).Error; err != nil {
		return err
	}
	if len(rows) < 3 {
		return fmt.Errorf("%d subs, need at least 3", len(rows))
	}
	subs := make([]cometSub, len(rows))
	from, to := refTime, refTime
	for i, r := range rows {
		mid := r.DateObs.Add(time.Duration(r.Exposure / 2 * float64(time.Second)))
		subs[i] = cometSub{stored: storedSub{key: r.RegisteredKey, exposure: r.Exposure, weight: r.Weight}, mid: mid}
		from, to = minTime(from, mid), maxTime(to, mid)
	}
	eph, err := horizons(ctx, designation, from, to, lon, lat, elev)
	if err != nil {
		return err
	}
	cometAt := func(t time.Time) (x, y float64, err error) {
		ra, dec, ok := eph.at(t)
		if !ok {
			return 0, 0, fmt.Errorf("no comet position at %s", t)
		}
		fx, fy, ok := sky.toPixel(ra, dec)
		if !ok {
			return 0, 0, errors.New("the comet is off the plate solution")
		}
		// FITS pixels count rows from the bottom, from 1; subs are held
		// top row first.
		return fx - 1, float64(stack.Height) - fy, nil
	}
	x0, y0, err := cometAt(refTime)
	if err != nil {
		return err
	}
	shifts := make([][2]float64, len(subs))
	for i, s := range subs {
		x, y, err := cometAt(s.mid)
		if err != nil {
			return err
		}
		shifts[i] = [2]float64{x0 - x, y0 - y}
	}
	first, last := shifts[0], shifts[len(shifts)-1]
	slog.Info("Stacking comet master", "object", stack.Object, "filter", stack.Filter, "subs", len(subs),
		"comet_x", math.Round(x0), "comet_y", math.Round(y0),
		"drift_px", math.Round(math.Hypot(last[0]-first[0], last[1]-first[1])))

	dir, err := os.MkdirTemp(p.workDir, "comet-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	stored := make([]storedSub, len(subs))
	for i, s := range subs {
		stored[i] = s.stored
	}
	load := func(i int, s storedSub) ([]float32, int, int, error) {
		local := filepath.Join(dir, fmt.Sprintf("s%04d.fit", i))
		if err := p.download(ctx, p.dest, s.key, local); err != nil {
			return nil, 0, 0, err
		}
		defer os.Remove(local)
		sub, w, h, err := readSub(local)
		if err != nil {
			return nil, 0, 0, err
		}
		return shiftImage(sub, w, h, shifts[i][0], shifts[i][1]), w, h, nil
	}
	var acc *Accumulator
	if len(stored) <= cometMedianMax {
		// When the comet moves little between subs, each star lands on the
		// same pixels in a few of them, which hold each other up against
		// the mean; the per-pixel median ignores them.
		all := make([]memSub, 0, len(stored))
		var w, h int
		for i, s := range stored {
			p.progress(stack.Object, stack.Filter, StageComet, i, len(stored))
			sub, sw, sh, err := load(i, s)
			if err != nil {
				p.finished(stack.Object)
				return err
			}
			w, h = sw, sh
			all = append(all, toMemSub(sub, s.exposure, s.weight, p.opts.Stack.SaturationLevel))
		}
		acc = medianAnchored(all, w, h, cometOptions(p.opts.Stack))
	} else {
		passes := 2
		loaded := 0
		acc, err = streamStack(stored, passes, cometOptions(p.opts.Stack), func(i int, s storedSub) ([]float32, int, int, error) {
			p.progress(stack.Object, stack.Filter, StageComet, loaded, passes*len(stored))
			loaded++
			return load(i, s)
		})
	}
	p.finished(stack.Object)
	if err != nil {
		return err
	}
	return p.publishComet(ctx, stack, acc, refTime, len(subs))
}

// shiftImage moves an image by (dx, dy) pixels with bilinear resampling.
// Pixels coming from outside the image, or next to one without data, are
// left empty (0) like registration borders.
func shiftImage(src []float32, w, h int, dx, dy float64) []float32 {
	out := make([]float32, len(src))
	ix, iy := int(math.Floor(dx)), int(math.Floor(dy))
	fx, fy := float32(dx-float64(ix)), float32(dy-float64(iy))
	for y := range h {
		sy := y - iy - 1 // out(x, y) = src(x - dx, y - dy)
		if sy < 0 || sy+1 >= h {
			continue
		}
		for x := range w {
			sx := x - ix - 1
			if sx < 0 || sx+1 >= w {
				continue
			}
			i := sy*w + sx
			a, b, c, d := src[i], src[i+1], src[i+w], src[i+w+1]
			if a == 0 || b == 0 || c == 0 || d == 0 {
				continue
			}
			// src(x - dx) lies between sx and sx+1, 1-fx of the way along.
			top := a*fx + b*(1-fx)
			bot := c*fx + d*(1-fx)
			out[y*w+x] = top*fy + bot*(1-fy)
		}
	}
	return out
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// publishComet writes the comet master beside the star-aligned one: FITS,
// XISF, a cropped preview and a linear preview for covers.
func (p *Pipeline) publishComet(ctx context.Context, stack *app.Stack, acc *Accumulator, refTime time.Time, subs int) error {
	dir, err := os.MkdirTemp(p.workDir, "comet-publish-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	master := acc.Master(stack.ScaleExposure)
	own := []imagedata.Card{
		imagedata.StringCard("OBJECT", stack.Object, "target"),
		imagedata.StringCard("FILTER", stack.Filter, "filter"),
		imagedata.StringCard("IMAGETYP", "Master Light", "aligned on the comet"),
		imagedata.IntCard("NCOMBINE", subs, "subs stacked"),
		imagedata.FloatCard("EXPTIME", stack.ScaleExposure, "[s] scaled to one sub of this length"),
		imagedata.StringCard("COMETREF", refTime.UTC().Format("2006-01-02T15:04:05"), "comet held where it was at this time (UTC)"),
		imagedata.StringCard("DATE", time.Now().UTC().Format("2006-01-02T15:04:05"), "file written"),
		imagedata.StringCard("SWCREATE", "astro-stacker (Siril 1.4 calibration)", ""),
	}
	// The stars are rejected and the frame follows the comet, so the
	// plate solution no longer describes it.
	header := p.masterHeader(ctx, stack, own, dir, "")
	cards := header[:0:0]
	for _, c := range header {
		if !wcsKey.MatchString(c.Key) {
			cards = append(cards, c)
		}
	}
	fitsFile, xisfFile := filepath.Join(dir, "comet.fit"), filepath.Join(dir, "comet.xisf")
	if err := writeFITSFile(fitsFile, acc.W, acc.H, 1, master, cards); err != nil {
		return err
	}
	if err := writeXISFFile(xisfFile, acc.W, acc.H, master, cards); err != nil {
		return err
	}
	r := coverageCrop(acc)
	jpg, err := preview.Render(&imagedata.Image{W: r.W, H: r.H, C: 1, Data: crop(master, acc.W, r)}, preview.DefaultOptions)
	if err != nil {
		return err
	}
	linear, err := linearPreview(&imagedata.Image{W: acc.W, H: acc.H, C: 1, Data: master})
	if err != nil {
		return err
	}
	prefix := stackPrefix(stack)
	keys := map[string]string{}
	for name, up := range map[string]func(string) error{
		"comet.fit":  func(k string) error { return p.upload(ctx, fitsFile, k, "application/fits") },
		"comet.xisf": func(k string) error { return p.upload(ctx, xisfFile, k, "application/octet-stream") },
		"comet.jpg": func(k string) error {
			return p.putBytes(ctx, k, jpg, minio.PutObjectOptions{ContentType: "image/jpeg"})
		},
		"comet-linear.bin": func(k string) error {
			return p.putBytes(ctx, k, linear, minio.PutObjectOptions{ContentType: "application/octet-stream", ContentEncoding: "gzip"})
		},
	} {
		k := path.Join(prefix, name)
		if err := up(k); err != nil {
			return err
		}
		keys[name] = k
	}
	// UpdateColumns leaves updated_at, which the signature is made of.
	if err := p.db.WithContext(ctx).Model(stack).UpdateColumns(map[string]any{
		"comet_key": keys["comet.fit"], "comet_xisf_key": keys["comet.xisf"], "comet_preview_key": keys["comet.jpg"],
		"comet_linear_key": keys["comet-linear.bin"], "comet_subs": subs,
		"comet_crop_x": r.X, "comet_crop_y": r.Y, "comet_crop_w": r.W, "comet_crop_h": r.H,
		"comet_signature": cometSignature(stack), "comet_error": nil,
	}).Error; err != nil {
		return err
	}
	stack.CometLinearKey = ptrTo(keys["comet-linear.bin"])
	stack.CometCropX, stack.CometCropY, stack.CometCropW, stack.CometCropH = r.X, r.Y, r.W, r.H
	p.refreshCover(ctx, stack.Object)
	p.Events.Publish(events.Event{Type: events.TypeMaster, Object: stack.Object, Filter: stack.Filter})
	slog.Info("Comet master published", "object", stack.Object, "filter", stack.Filter, "subs", subs)
	return nil
}

func ptrTo[T any](v T) *T { return &v }
