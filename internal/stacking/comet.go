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
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
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

// cometOptions are the options for a comet master stacked against the
// median. Rejections aren't grown: with the comet moving a few pixels between
// subs every star's trail is dense, and the grown rejections of neighbouring
// trails would cover whole areas.
func cometOptions(opts Options) Options {
	opts.RejectGrow = 0
	opts.KeepMajority = false
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
	// Horizons turns requests away with 503 when busy; a failure here is
	// kept until the master changes, so it is worth waiting for.
	var body []byte
	var status string
	for attempt := range 5 {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ephemeris{}, ctx.Err()
			case <-time.After(time.Duration(attempt*attempt) * 5 * time.Second):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, horizonsURL+"?"+q.Encode(), nil)
		if err != nil {
			return ephemeris{}, err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return ephemeris{}, fmt.Errorf("horizons: %w", err)
		}
		body, err = io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		resp.Body.Close()
		if err != nil {
			return ephemeris{}, err
		}
		status = resp.Status
		if resp.StatusCode < 500 {
			break
		}
	}
	var r struct {
		Result string `json:"result"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return ephemeris{}, fmt.Errorf("horizons: %s: %w", status, err)
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
		if p.stopping(ctx) {
			return nil
		}
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

// cometMethod is part of every comet master's signature, so a change to how
// they are made stacks them all again.
const cometMethod = 2 // stars taken out before stacking on the comet

func cometSignature(s *app.Stack) string {
	h := sha256.Sum256(fmt.Appendf(nil, "%d\x00%d\x00%d", s.Subs, s.UpdatedAt.UnixNano(), cometMethod))
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
		Gain          *float64
	}
	if err := p.db.WithContext(ctx).Table("stack_frames sf").
		Select("sf.registered_key, sf.exposure, sf.weight, f.date_obs, f.gain").
		Joins("JOIN frames f ON f.id = sf.frame_id").
		Where("sf.stack_id = ? AND sf.status = ? AND sf.registered_key IS NOT NULL AND f.date_obs IS NOT NULL", stack.ID, app.StackStatusAdded).
		Order("f.date_obs").Scan(&rows).Error; err != nil {
		return err
	}
	if len(rows) < 3 {
		return fmt.Errorf("%d subs, need at least 3", len(rows))
	}
	subs := make([]cometSub, len(rows))
	scales := decodeGainTable(stack.GainScales)
	from, to := refTime, refTime
	for i, r := range rows {
		mid := r.DateObs.Add(time.Duration(r.Exposure / 2 * float64(time.Second)))
		// On the star master's gain (see gainScales).
		exp := r.Exposure * scales.scale(r.Gain)
		subs[i] = cometSub{stored: storedSub{key: r.RegisteredKey, exposure: exp, weight: r.Weight}, mid: mid}
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
	track := make([][2]float64, len(subs))
	for i, s := range subs {
		x, y, err := cometAt(s.mid)
		if err != nil {
			return err
		}
		shifts[i], track[i] = [2]float64{x0 - x, y0 - y}, [2]float64{x, y}
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
	defer p.finished(stack.Object)
	stored := make([]storedSub, len(subs))
	for i, s := range subs {
		stored[i] = s.stored
	}
	// Each sub is read by several passes, so it is fetched once.
	files := make([]string, len(stored))
	for i, s := range stored {
		p.progress(stack.Object, stack.Filter, StageComet, i, len(stored))
		files[i] = filepath.Join(dir, fmt.Sprintf("s%04d.fit", i))
		if err := p.download(ctx, p.dest, s.key, files[i]); err != nil {
			return err
		}
	}
	master, _, err := p.loadMaster(ctx, dir, stack)
	if err != nil {
		return fmt.Errorf("star-aligned master: %w", err)
	}
	stars := starMask(master.Data, master.W, master.H)
	master = nil
	layers, err := separateComet(stored, shifts, track, stars, p.opts.Stack, func(i int) ([]float32, int, int, error) {
		return readSub(files[i])
	}, func(done, total int) {
		p.progress(stack.Object, stack.Filter, StageComet, done, total)
	})
	if err != nil {
		return err
	}
	return p.publishComet(ctx, stack, layers, refTime, len(subs))
}

// cometLayers are a comet master's two layers, on the reference sub's
// pixels: the comet alone, with the stars and everything else fixed on the
// sky taken out, and those stars without the comet.
type cometLayers struct {
	comet, stars *Accumulator
	// add is the stars' signal per second to add back to the comet.
	add []float32
}

// starsToAdd is the stars layer to add back to the comet: only what is as
// small as a star, over the layer's median level, the glow of stars too
// faint to resolve. Wider structure in the layer is what's left of the
// comet's head, which would show round it as a disc.
func starsToAdd(layer *Accumulator) []float32 {
	local := medianFilter(layer.Mean, layer.W, layer.H, 7)
	stride := max(1, len(local)/200_000)
	var sample []float64
	for i := 0; i < len(local); i += stride {
		if layer.Mean[i] != 0 {
			sample = append(sample, float64(local[i]))
		}
	}
	var glow float32
	if len(sample) > 0 {
		glow = float32(median(sample))
	}
	add := make([]float32, len(local))
	for i, v := range layer.Mean {
		switch {
		case v != 0:
			add[i] = v - local[i] + glow
		case layer.Weight[i] > 0: // under the comet's head (thinTrack)
			add[i] = glow
		}
	}
	return add
}

// separateComet stacks the comet and the stars apart. Stacked on the comet,
// the stars trail through the frame and rejection removes them only where a
// sub shows them well above its noise: faint stars, and the wings of bright
// ones, add up to dashed trails. So there are three stacks: (1) on the
// comet, with rejection; (2) on the stars, with that comet taken out of
// every sub, which leaves the stars and nebulosity without the comet; (3)
// on the comet again, with those stars taken out of every sub at its
// transparency, which leaves rejection only noise and seeing to deal with.
// read returns sub i as registered on the stars; shifts move it onto the
// comet; track is where the comet was in each. stars marks the pixels of
// the star-aligned master with stars on them (starMask), which the first
// stack leaves out but for the comet itself, so its comet holds no trails
// for the second to take out of the stars.
func separateComet(subs []storedSub, shifts, track [][2]float64, stars []bool, opts Options, read func(int) ([]float32, int, int, error), progress func(done, total int)) (cometLayers, error) {
	reads := 1 // per sub and stack
	if len(subs) > cometMedianMax {
		reads = 2
	}
	done, total := 0, 3*reads*len(subs)
	sat := opts.SaturationLevel
	stackAll := func(opts Options, prep func(i int, sub []float32, w, h int) []float32) (*Accumulator, error) {
		load := func(i int) ([]float32, int, int, error) {
			progress(done, total)
			done++
			sub, w, h, err := read(i)
			if err != nil {
				return nil, 0, 0, err
			}
			return prep(i, sub, w, h), w, h, nil
		}
		if len(subs) > cometMedianMax {
			// Long sessions: each star crosses a pixel in about one sub,
			// and growing rejections catches the faint edges of its trail.
			o := opts
			o.KeepMajority = false
			return streamStack(subs, 2, o, func(i int, _ storedSub) ([]float32, int, int, error) { return load(i) })
		}
		// When the comet moves little between subs, each star lands on the
		// same pixels in a few of them, which hold each other up against
		// the mean; the per-pixel median ignores them.
		all := make([]memSub, 0, len(subs))
		var w, h int
		for i, s := range subs {
			sub, sw, sh, err := load(i)
			if err != nil {
				return nil, err
			}
			if i > 0 && (sw != w || sh != h) {
				return nil, fmt.Errorf("sub %s is %dx%d, expected %dx%d", s.key, sw, sh, w, h)
			}
			w, h = sw, sh
			all = append(all, toMemSub(sub, s.exposure, s.weight, sat))
		}
		return medianAnchored(all, w, h, cometOptions(opts)), nil
	}
	onComet := func(i int, sub []float32, w, h int) []float32 {
		return shiftImage(sub, w, h, shifts[i][0], shifts[i][1])
	}

	first, err := stackAll(opts, func(i int, sub []float32, w, h int) []float32 {
		if len(stars) == len(sub) {
			cx, cy := track[i][0], track[i][1]
			for j, star := range stars {
				if star && math.Hypot(float64(j%w)-cx, float64(j/w)-cy) > cometKeep {
					sub[j] = 0
				}
			}
		}
		return onComet(i, sub, w, h)
	})
	if err != nil {
		return cometLayers{}, err
	}
	comet := first.Mean // per second, without the sky
	first = nil
	layer, err := stackAll(opts, func(i int, sub []float32, w, h int) []float32 {
		back := shiftImage(comet, w, h, -shifts[i][0], -shifts[i][1])
		subtractSignal(sub, back, subs[i].exposure, sat)
		if track != nil {
			emptyDisc(sub, w, h, track[i], cometClear)
		}
		return sub
	})
	if err != nil {
		return cometLayers{}, err
	}
	comet = nil
	thinTrack(layer, track, len(subs))
	apertures := starApertures(layer)
	starless := func(i int, sub []float32, w int) {
		subtractSignal(sub, layer.Mean, starScale(sub, layer.Mean, w, apertures, subs[i].exposure, sat), sat)
	}
	final, err := stackAll(opts, func(i int, sub []float32, w, h int) []float32 {
		starless(i, sub, w)
		maskSpikes(sub, w, h, sat)
		return onComet(i, sub, w, h)
	})
	if err != nil {
		return cometLayers{}, err
	}
	progress(total, total)
	return cometLayers{comet: final, stars: layer, add: starsToAdd(layer)}, nil
}

// maskSpikes empties a sub's isolated spikes of at least 5σ, and the
// pixels around them: hot pixels the dark missed and cosmic rays. Hot pixels
// stay put on the sensor, so on the comet they trail like stars, and
// registration and the comet's shift spread each over a few pixels too
// faint for rejection. The stars must be out of the sub already: the pixels
// two away from a spike are at the sky, which around a star they aren't.
func maskSpikes(sub []float32, w, h int, sat float32) int {
	bg, noise := float32(background(sub, sat)), float32(noiseLevel(sub, sat))
	var found []int
	for y := 2; y < h-2; y++ {
		for x := 2; x < w-2; x++ {
			i := y*w + x
			v := sub[i]
			if v == 0 || v-bg < 5*noise {
				continue
			}
			spike := true
			for dy := -2; dy <= 2 && spike; dy++ {
				for dx := -2; dx <= 2; dx++ {
					u := sub[i+dy*w+dx]
					if max(abs(dx), abs(dy)) == 2 {
						spike = u-bg <= 3*noise
					} else if dx != 0 || dy != 0 {
						spike = u <= v
					}
					if !spike {
						break
					}
				}
			}
			if spike {
				found = append(found, i)
			}
		}
	}
	for _, i := range found {
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				sub[i+dy*w+dx] = 0
			}
		}
	}
	return len(found)
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// starMask marks the stars of a star-aligned master: pixels standing 3σ
// above the median of the 7×7 around them, and those within 3 pixels of
// one. The comet's path shows up too; separateComet keeps the comet's place
// in each sub.
func starMask(master []float32, w, h int) []bool {
	local := medianFilter(master, w, h, 3)
	diff := make([]float32, len(master))
	for i, v := range master {
		if v != 0 {
			diff[i] = v - local[i]
		}
	}
	sigma := float32(noiseLevel(diff, math.MaxFloat32))
	const grow = 3
	mask := make([]bool, len(master))
	for y := range h {
		for x := range w {
			if diff[y*w+x] <= 3*sigma {
				continue
			}
			for yy := max(0, y-grow); yy <= min(h-1, y+grow); yy++ {
				for xx := max(0, x-grow); xx <= min(w-1, x+grow); xx++ {
					mask[yy*w+xx] = true
				}
			}
		}
	}
	return mask
}

// cometClear is the radius around the comet, in pixels, left out of each
// sub for the stars layer. No comet model matches every sub's head
// (transparency, seeing, the ephemeris to a fraction of a pixel); what's
// left of it in the layer would come back on the comet as a band through
// its head. The subs taken with the comet elsewhere fill the stars in.
const cometClear = 100

// emptyDisc empties the pixels within r of c.
func emptyDisc(sub []float32, w, h int, c [2]float64, r int) {
	for y := max(0, int(c[1])-r); y <= min(h-1, int(c[1])+r); y++ {
		for x := max(0, int(c[0])-r); x <= min(w-1, int(c[0])+r); x++ {
			if math.Hypot(float64(x)-c[0], float64(y)-c[1]) <= float64(r) {
				sub[y*w+x] = 0
			}
		}
	}
}

// thinTrack empties the stars layer on the comet's path where fewer than a
// third of the subs had the comet elsewhere. There too few samples hold it,
// without rejection, to take out of the subs. It keeps a token weight there:
// with none the layer's master would take those pixels for saturated star
// cores and fill them white. A comet that moved less than its head leaves a
// patch without stars, under the head.
func thinTrack(layer *Accumulator, track [][2]float64, subs int) {
	w, h := layer.W, layer.H
	least := float32(max(3, subs/3))
	for k, c := range track {
		if k > 0 && math.Hypot(c[0]-track[k-1][0], c[1]-track[k-1][1]) < 2 {
			continue
		}
		for y := max(0, int(c[1])-cometClear); y <= min(h-1, int(c[1])+cometClear); y++ {
			for x := max(0, int(c[0])-cometClear); x <= min(w-1, int(c[0])+cometClear); x++ {
				i := y*w + x
				if layer.Count[i] < least && math.Hypot(float64(x)-c[0], float64(y)-c[1]) <= cometClear {
					layer.Mean[i] = 0
					layer.Weight[i] = math.SmallestNonzeroFloat32
				}
			}
		}
	}
}

// cometKeep is the radius around the comet, in pixels, that the star mask
// leaves in each sub.
const cometKeep = 40

// medianFilter replaces each pixel with the median of the (2r+1)² around
// it that have data; pixels without data stay 0.
func medianFilter(p []float32, w, h, r int) []float32 {
	out := make([]float32, len(p))
	rows := make(chan int, h)
	for y := range h {
		rows <- y
	}
	close(rows)
	var wg sync.WaitGroup
	for range runtime.GOMAXPROCS(0) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			win := make([]float32, 0, (2*r+1)*(2*r+1))
			for y := range rows {
				for x := range w {
					i := y*w + x
					if p[i] == 0 {
						continue
					}
					win = win[:0]
					for yy := max(0, y-r); yy <= min(h-1, y+r); yy++ {
						for xx := max(0, x-r); xx <= min(w-1, x+r); xx++ {
							if v := p[yy*w+xx]; v != 0 {
								win = append(win, v)
							}
						}
					}
					slices.Sort(win)
					out[i] = win[len(win)/2]
				}
			}
		}()
	}
	wg.Wait()
	return out
}

// subtractSignal takes scale × model out of a sub in place. Empty and
// saturated pixels, and those the model has nothing for, stay as they are.
func subtractSignal(sub, model []float32, scale float64, sat float32) {
	k := float32(scale)
	for i, v := range sub {
		m := model[i]
		if v == 0 || v >= sat || m == 0 {
			continue
		}
		if r := v - k*m; r != 0 {
			sub[i] = r
		} else {
			sub[i] = 1e-9 // 0 would mark it empty
		}
	}
}

// starApertures are the brightest isolated stars of a stars layer (per
// second, sky-free), at least starBox from the edges, whose flux measures
// each sub's share of the layer.
func starApertures(stars *Accumulator) []int {
	const box = starBox
	w, h := stars.W, stars.H
	var sigma float64
	{
		stride := max(1, len(stars.Mean)/200_000)
		var dev []float64
		for i := 0; i < len(stars.Mean); i += stride {
			if stars.Weight[i] > 0 {
				dev = append(dev, math.Abs(float64(stars.Mean[i])))
			}
		}
		if len(dev) == 0 {
			return nil
		}
		sigma = median(dev) * madToSigma
	}
	type peak struct {
		i int
		v float32
	}
	var peaks []peak
	for y := box; y < h-box; y++ {
		for x := box; x < w-box; x++ {
			i := y*w + x
			v := stars.Mean[i]
			if float64(v) < 30*sigma {
				continue
			}
			top, clean := true, true
			for dy := -box; dy <= box && top && clean; dy++ {
				for dx := -box; dx <= box; dx++ {
					j := i + dy*w + dx
					if stars.Weight[j] == 0 {
						clean = false // saturated core or border
						break
					}
					if (dx != 0 || dy != 0) && stars.Mean[j] >= v {
						top = false
						break
					}
				}
			}
			if top && clean {
				peaks = append(peaks, peak{i, v})
			}
		}
	}
	slices.SortFunc(peaks, func(a, b peak) int { return cmpFloat32(b.v, a.v) })
	out := make([]int, 0, min(len(peaks), 500))
	for _, p := range peaks[:min(len(peaks), 500)] {
		out = append(out, p.i)
	}
	return out
}

func cmpFloat32(a, b float32) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// starBox is the half-width of the box a star's flux is measured in: a disc
// of radius starAperture with the sky from the box's corners.
const (
	starBox      = 12
	starAperture = 6
)

// starScale is how much of stars, a stack's signal per second, a sub holds:
// its exposure times its transparency against the stack's. It is the median
// ratio of the stars' fluxes, which unlike their peaks doesn't depend on the
// seeing, or the exposure when too few stars can be measured.
func starScale(sub, stars []float32, w int, apertures []int, exposure float64, sat float32) float64 {
	var ratios []float64
	var ringS, ringM []float64
	for _, c := range apertures {
		var fs, fm float64
		n := 0
		ringS, ringM = ringS[:0], ringM[:0]
		ok := true
		for dy := -starBox; dy <= starBox && ok; dy++ {
			for dx := -starBox; dx <= starBox; dx++ {
				j := c + dy*w + dx
				v := sub[j]
				if v == 0 || v >= sat {
					ok = false
					break
				}
				switch r2 := dx*dx + dy*dy; {
				case r2 <= starAperture*starAperture:
					fs += float64(v)
					fm += float64(stars[j])
					n++
				case r2 > (starAperture+2)*(starAperture+2):
					ringS = append(ringS, float64(v))
					ringM = append(ringM, float64(stars[j]))
				}
			}
		}
		if !ok {
			continue
		}
		fs -= float64(n) * median(ringS)
		fm -= float64(n) * median(ringM)
		if fm > 0 {
			ratios = append(ratios, fs/fm)
		}
	}
	if len(ratios) < 20 {
		return exposure
	}
	if k := median(ratios); k > 0 {
		return k
	}
	return exposure
}

// addStars adds the stars' layer, scaled to one sub of scaleExposure
// seconds, to a comet master. Cores saturated in every sub come out at full
// scale; pixels either layer has no data for are left as the comet's.
func addStars(comet []float32, stars *Accumulator, add []float32, scaleExposure float64) []float32 {
	out := slices.Clone(comet)
	full := stars.Master(scaleExposure) // 1 in saturated cores
	s := float32(scaleExposure)
	for i, c := range comet {
		switch {
		case c == 0:
		case stars.Weight[i] > 0:
			out[i] = min(1, max(1e-7, c+add[i]*s))
		case full[i] == 1:
			out[i] = 1
		}
	}
	return out
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
// XISF, a cropped preview and a linear preview for covers. The master is the
// comet with the stars added back sharp; the comet alone is kept beside it
// as comet-starless.{fit,xisf}.
func (p *Pipeline) publishComet(ctx context.Context, stack *app.Stack, layers cometLayers, refTime time.Time, subs int) error {
	dir, err := os.MkdirTemp(p.workDir, "comet-publish-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	acc := layers.comet
	starless := acc.Master(stack.ScaleExposure)
	master := addStars(starless, layers.stars, layers.add, stack.ScaleExposure)
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
	// The stars are where they were in the reference sub, so the plate
	// solution still describes the master; the starless comet has none.
	cards := p.masterHeader(ctx, stack, own, dir, "")
	starlessCards := cards[:0:0]
	for _, c := range cards {
		if !wcsKey.MatchString(c.Key) {
			starlessCards = append(starlessCards, c)
		}
	}
	fitsFile, xisfFile := filepath.Join(dir, "comet.fit"), filepath.Join(dir, "comet.xisf")
	if err := writeFITSFile(fitsFile, acc.W, acc.H, 1, master, cards); err != nil {
		return err
	}
	if err := writeXISFFile(xisfFile, acc.W, acc.H, master, cards); err != nil {
		return err
	}
	starlessFITS, starlessXISF := filepath.Join(dir, "comet-starless.fit"), filepath.Join(dir, "comet-starless.xisf")
	if err := writeFITSFile(starlessFITS, acc.W, acc.H, 1, starless, starlessCards); err != nil {
		return err
	}
	if err := writeXISFFile(starlessXISF, acc.W, acc.H, starless, starlessCards); err != nil {
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
		"comet.fit":          func(k string) error { return p.upload(ctx, fitsFile, k, "application/fits") },
		"comet.xisf":         func(k string) error { return p.upload(ctx, xisfFile, k, "application/octet-stream") },
		"comet-starless.fit": func(k string) error { return p.upload(ctx, starlessFITS, k, "application/fits") },
		"comet-starless.xisf": func(k string) error {
			return p.upload(ctx, starlessXISF, k, "application/octet-stream")
		},
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
		"comet_signature": cometSignature(stack), "comet_error": nil, "comet_method": cometMethod,
	}).Error; err != nil {
		return err
	}
	stack.CometLinearKey = ptrTo(keys["comet-linear.bin"])
	stack.CometMethod = ptrTo(cometMethod)
	stack.CometCropX, stack.CometCropY, stack.CometCropW, stack.CometCropH = r.X, r.Y, r.W, r.H
	p.refreshCover(ctx, stack.Object)
	p.Events.Publish(events.Event{Type: events.TypeMaster, Object: stack.Object, Filter: stack.Filter})
	slog.Info("Comet master published", "object", stack.Object, "filter", stack.Filter, "subs", subs)
	return nil
}

func ptrTo[T any](v T) *T { return &v }
