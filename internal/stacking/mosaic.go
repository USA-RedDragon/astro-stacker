package stacking

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"log/slog"
	"math"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/events"
	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/indexer"
	"github.com/USA-RedDragon/astro-stacker/internal/metrics"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/minio/minio-go/v7"
	"gorm.io/gorm"
)

// mosaicMethod is part of every mosaic's signature, so a change to how
// they are assembled builds them all again. 2: panels flattened first.
// 3: panels matched on their overlaps; the FITS marked bottom-up, and an
// XISF copy for PixInsight.
const mosaicMethod = 3

// StageAssembling is reported while a mosaic is being built.
const StageAssembling = "assembling"

// panelName matches a mosaic panel target: "Cygnis Loop Panel 2".
var panelName = regexp.MustCompile(`(?i)\bpanel\s*(\d+)\s*$`)

type panel struct {
	Object   string
	Number   int
	RA       float64 // degrees
	Dec      float64 // degrees
	Rotation float64 // degrees, as Target Scheduler frames it
}

// mosaicGroup is a Target Scheduler project whose targets are panels.
type mosaicGroup struct {
	Project string
	Panels  []panel
}

// mosaicGroups reads the projects with two or more panel targets.
func mosaicGroups(ctx context.Context, sched *gorm.DB) ([]mosaicGroup, error) {
	var rows []struct {
		Project  string
		Name     string
		RA       float64
		Dec      float64
		Rotation float64
	}
	if err := sched.WithContext(ctx).Table("target").
		Select(`project.name AS project, target.name AS name, target.ra AS ra, target.dec AS dec, target.rotation AS rotation`).
		Joins(`JOIN project ON target.projectid = project."Id"`).
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load targets: %w", err)
	}
	byProject := map[string][]panel{}
	for _, r := range rows {
		m := panelName.FindStringSubmatch(r.Name)
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[1])
		// Target Scheduler stores right ascension in hours.
		byProject[r.Project] = append(byProject[r.Project], panel{Object: r.Name, Number: n, RA: r.RA * 15, Dec: r.Dec, Rotation: r.Rotation})
	}
	var out []mosaicGroup
	for project, panels := range byProject {
		if len(panels) < 2 {
			continue
		}
		slices.SortFunc(panels, func(a, b panel) int { return a.Number - b.Number })
		out = append(out, mosaicGroup{Project: project, Panels: panels})
	}
	slices.SortFunc(out, func(a, b mosaicGroup) int { return strings.Compare(a.Project, b.Project) })
	return out, nil
}

// MosaicPanels names the targets that are panels of a mosaic project, as
// the mosaics are built from.
func MosaicPanels(ctx context.Context, sched *gorm.DB) (map[string]bool, error) {
	groups, err := mosaicGroups(ctx, sched)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, g := range groups {
		for _, p := range g.Panels {
			out[p.Object] = true
		}
	}
	return out, nil
}

// runMosaics builds mosaics and linear fits that are due every interval
// until ctx ends.
func (p *Pipeline) runMosaics(ctx context.Context, interval time.Duration) {
	for !p.stopping(ctx) {
		if err := p.MosaicsOnce(ctx); err != nil && ctx.Err() == nil {
			slog.Error("Building mosaics failed", "error", err)
		}
		if p.stopping(ctx) {
			return
		}
		if err := p.linearFitsOnce(ctx); err != nil && ctx.Err() == nil {
			slog.Error("LinearFit failed", "error", err)
		}
		if p.stopping(ctx) {
			return
		}
		if err := p.cometsOnce(ctx); err != nil && ctx.Err() == nil {
			slog.Error("Comet masters failed", "error", err)
		}
		if p.stopping(ctx) {
			return
		}
		if err := p.recalibrateDarks(ctx); err != nil && ctx.Err() == nil {
			slog.Error("Checking for new darks failed", "error", err)
		}
		if !p.pause(ctx, interval) {
			return
		}
	}
}

// MosaicsOnce builds every mosaic whose panel masters changed and have been
// left alone for MosaicQuiet, so a mosaic isn't rebuilt after every batch
// while its panels are still filling in.
func (p *Pipeline) MosaicsOnce(ctx context.Context) error {
	groups, err := mosaicGroups(ctx, p.sched)
	if err != nil {
		return err
	}
	for _, g := range groups {
		objects := make([]string, len(g.Panels))
		for i, pn := range g.Panels {
			objects[i] = pn.Object
		}
		var stacks []app.Stack
		if err := p.db.WithContext(ctx).Where("object IN ? AND subs > 0 AND master_key IS NOT NULL", objects).
			Find(&stacks).Error; err != nil {
			return err
		}
		byFilter := map[string][]app.Stack{}
		for _, s := range stacks {
			byFilter[s.Filter] = append(byFilter[s.Filter], s)
		}
		// One panel is enough: the layout shows the rest as gaps.
		for filter, masters := range byFilter {
			if p.stopping(ctx) {
				return nil
			}
			if err := p.mosaicIfDue(ctx, g, filter, masters); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				slog.Error("Mosaic failed", "project", g.Project, "filter", filter, "error", err)
			}
		}
	}
	return nil
}

func (p *Pipeline) mosaicIfDue(ctx context.Context, g mosaicGroup, filter string, masters []app.Stack) error {
	slices.SortFunc(masters, func(a, b app.Stack) int { return strings.Compare(a.Object, b.Object) })
	h := sha256.New()
	fmt.Fprintf(h, "%d\x00", mosaicMethod)
	var newest time.Time
	for _, m := range masters {
		fmt.Fprintf(h, "%s\x00%d\x00", m.Object, m.UpdatedAt.UnixNano())
		if m.UpdatedAt.After(newest) {
			newest = m.UpdatedAt
		}
	}
	sig := hex.EncodeToString(h.Sum(nil))[:32]
	var mosaic app.Mosaic
	err := p.db.WithContext(ctx).Where("project = ? AND filter = ?", g.Project, filter).First(&mosaic).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		mosaic = app.Mosaic{Project: g.Project, Filter: filter}
	case err != nil:
		return err
	}
	if mosaic.Signature == sig || time.Since(newest) < p.opts.MosaicQuiet {
		return nil
	}
	name := "Mosaic: " + g.Project
	p.progress(name, filter, StageAssembling, 0, len(masters))
	defer p.finished(name)
	start := time.Now()
	buildErr := p.buildMosaic(ctx, g, filter, masters, &mosaic)
	metrics.MosaicSeconds.Observe(time.Since(start).Seconds())
	metrics.Mosaics.WithLabelValues(map[bool]string{true: "ok", false: "failed"}[buildErr == nil]).Inc()
	mosaic.Signature, mosaic.PanelsTotal = sig, len(g.Panels)
	if buildErr != nil {
		// Recorded so it isn't retried until a panel master changes.
		msg := buildErr.Error()
		mosaic.Error = &msg
	} else {
		mosaic.Error = nil
		mosaic.Panels = len(masters)
		mosaic.EffectiveSeconds = 0
		for _, m := range masters {
			mosaic.EffectiveSeconds += m.EffectiveSeconds
		}
		mosaic.UpdatedAt = time.Now()
	}
	if err := p.db.WithContext(ctx).Save(&mosaic).Error; err != nil {
		return err
	}
	if buildErr != nil {
		return buildErr
	}
	p.refreshMosaicCover(ctx, g.Project)
	p.Events.Publish(events.Event{Type: events.TypeMosaic, Object: g.Project, Filter: filter})
	slog.Info("Updated mosaic", "project", g.Project, "filter", filter, "panels", len(masters),
		"size", fmt.Sprintf("%dx%d", mosaic.Width, mosaic.Height), "duration", time.Since(start).Round(time.Second))
	return nil
}

// buildMosaic plate solves each panel master from its target's position
// and the camera's optics, reprojects them onto one canvas and blends them,
// matching levels in the overlaps. The online NOMAD catalogue supplies the
// reference stars.
func (p *Pipeline) buildMosaic(ctx context.Context, g mosaicGroup, filter string, masters []app.Stack, mosaic *app.Mosaic) error {
	dir, err := os.MkdirTemp(p.workDir, "mosaic-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	positions := map[string]panel{}
	for _, pn := range g.Panels {
		positions[pn.Object] = pn
	}
	for i, m := range masters {
		p.progress("Mosaic: "+g.Project, filter, StageAssembling, i, len(masters)+1)
		local := filepath.Join(dir, fmt.Sprintf("panel%02d.fit", i+1))
		if err := p.download(ctx, p.dest, *m.MasterKey, local); err != nil {
			return err
		}
		// Masters carry their target's plate solution; only older ones are
		// solved here, from the panel's planned position, which fails more
		// often than solving from where the mount actually pointed.
		if solved(local) {
			if err := os.Rename(local, filepath.Join(dir, fmt.Sprintf("pan_%05d.fit", i+1))); err != nil {
				return err
			}
			continue
		}
		focal, pixel, err := p.optics(ctx, m.Object, filter)
		if err != nil {
			return fmt.Errorf("%s: %w", m.Object, err)
		}
		if err := p.solvePanel(ctx, dir, i+1, positions[m.Object], focal, pixel); err != nil {
			return fmt.Errorf("%s: %w", m.Object, err)
		}
	}
	p.progress("Mosaic: "+g.Project, filter, StageAssembling, len(masters), len(masters)+1)
	fitsOut, xisfOut, err := p.assembleMosaic(ctx, dir, g.Project, filter, len(masters))
	if err != nil {
		return err
	}
	// The previews: the mosaic on a canvas framing every panel of the
	// project, so panels without a master yet show as outlined gaps where
	// they go, and every filter's mosaic lines up for colour.
	canvas, img, cw, ch, r, err := p.layoutPreview(dir, g, masters, fitsOut)
	if err != nil {
		return err
	}
	jpg, err := encodeGray(crop(img, cw, r), r.W, r.H)
	if err != nil {
		return err
	}
	linear, err := linearPreview(&imagedata.Image{W: cw, H: ch, C: 1, Data: canvas})
	if err != nil {
		return err
	}
	// The mosaic's size and crop are the layout's, which the linear preview
	// shows; the downloadable FITS is Siril's own framing.
	mosaic.CropX, mosaic.CropY, mosaic.CropW, mosaic.CropH = r.X, r.Y, r.W, r.H
	im := &imagedata.Image{W: cw, H: ch}
	prefix := mosaicPrefix(g.Project, filter)
	keys := map[string]string{}
	for name, up := range map[string]func(string) error{
		"mosaic.fit":  func(k string) error { return p.upload(ctx, fitsOut, k, "application/fits") },
		"mosaic.xisf": func(k string) error { return p.upload(ctx, xisfOut, k, contentTypeOctetStream) },
		"preview.jpg": func(k string) error {
			return p.putBytes(ctx, k, jpg, minio.PutObjectOptions{ContentType: contentTypeJPEG})
		},
		"linear.bin": func(k string) error {
			return p.putBytes(ctx, k, linear, minio.PutObjectOptions{ContentType: contentTypeOctetStream, ContentEncoding: contentEncodingGzip})
		},
	} {
		k := path.Join(prefix, name)
		if err := up(k); err != nil {
			return err
		}
		keys[name] = k
	}
	master, xisf, prev, lin := keys["mosaic.fit"], keys["mosaic.xisf"], keys["preview.jpg"], keys["linear.bin"]
	mosaic.MasterKey, mosaic.XISFKey, mosaic.PreviewKey, mosaic.LinearKey = &master, &xisf, &prev, &lin
	mosaic.Width, mosaic.Height = im.W, im.H
	return nil
}

// assembleMosaic builds the mosaic from the solved panels pan_00001.fit
// on in dir, returning the downloadable FITS and its XISF copy.
//
// Each panel's own sky gradient (moon, horizon glow, a different one every
// night) would show in the mosaic as a colour cast per panel, so a plane
// fitted to each panel's sky is taken out first. That leaves the panels'
// levels, and whatever of their gradients a plane under nebulosity got
// wrong, differing on the overlaps; Siril blends the panels unnormalized
// (its -overlap_norm does nothing without -norm, and only matches an
// offset with it), so the difference would show as a feathered band. With
// two or more panels, the registered panels are therefore matched on their
// overlaps before Siril stacks them. seqplatesolve only computes the
// astrometric registration from the panels' solutions; seqapplyreg
// projects them onto one canvas. With one panel, it is the solved panel
// itself.
func (p *Pipeline) assembleMosaic(ctx context.Context, dir, project, filter string, n int) (fitsOut, xisfOut string, err error) {
	sat := p.opts.Stack.SaturationLevel
	for i := range n {
		if err := flattenPanel(filepath.Join(dir, fmt.Sprintf("pan_%05d.fit", i+1)), sat); err != nil {
			return "", "", fmt.Errorf("panel %d: flatten: %w", i+1, err)
		}
	}
	out := filepath.Join(dir, "pan_00001.fit")
	if n >= 2 {
		if _, err := p.siril.Run(ctx, dir, p.sirilPreamble(true)+
			"seqplatesolve pan -nocache\n"+
			"seqapplyreg pan -framing=max\n"); err != nil {
			return "", "", err
		}
		registered, err := filepath.Glob(filepath.Join(dir, "r_pan_*.fit"))
		if err != nil {
			return "", "", err
		}
		slices.Sort(registered)
		before, after, err := matchRegistered(registered, sat)
		if err != nil {
			return "", "", fmt.Errorf("match panels: %w", err)
		}
		logPairs("Mosaic overlap before matching", project, filter, before)
		logPairs("Mosaic overlap after matching", project, filter, after)
		if _, err := p.siril.Run(ctx, dir, p.sirilPreamble(true)+
			"stack r_pan rej none -maximize -feather=300 -32b -out=mosaic\n"); err != nil {
			return "", "", err
		}
		out = filepath.Join(dir, "mosaic.fit")
	}
	fitsOut, xisfOut = filepath.Join(dir, "download.fit"), filepath.Join(dir, "download.xisf")
	return fitsOut, xisfOut, publishableMosaic(out, fitsOut, xisfOut)
}

// publishableMosaic writes the mosaic for download: a FITS marked as
// stored bottom row first, as FITS is (PixInsight otherwise reads FITS top
// row first, which turns the image over under its plate solution), and an
// XISF copy that PixInsight opens upright and solved, as masters have.
func publishableMosaic(in, fitsOut, xisfOut string) error {
	b, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	im, err := imagedata.Decode(b)
	if err != nil {
		return err
	}
	if im.C != 1 {
		return fmt.Errorf("mosaic has %d channels", im.C)
	}
	cards, err := copiedCards(b)
	if err != nil {
		return err
	}
	if err := writeFITSFile(fitsOut, im.W, im.H, 1, im.Data, cards); err != nil {
		return err
	}
	return writeXISFFile(xisfOut, im.W, im.H, im.Data, cards)
}

// layoutPreview frames every panel of the project on one canvas, turned
// like the panels: solved panels from their plate solutions, missing ones
// placed from Target Scheduler, framed like the first solved panel. The
// pixels are the assembled mosaic's (mosaicFile, matched on the overlaps
// as the download is), reprojected onto the canvas. It returns the linear
// canvas and a stretched copy with the missing panels outlined.
func (p *Pipeline) layoutPreview(dir string, g mosaicGroup, masters []app.Stack, mosaicFile string) (canvas, img []float32, w, h int, r Rect, err error) {
	present := map[string]int{} // object -> pan file number
	for i, m := range masters {
		present[m.Object] = i + 1
	}
	var refWCS wcs
	var refRot float64
	solved := false
	panels := make([]layoutPanel, 0, len(g.Panels))
	for _, pn := range g.Panels {
		n, ok := present[pn.Object]
		if !ok {
			continue
		}
		kw, err := readKeywords(filepath.Join(dir, fmt.Sprintf("pan_%05d.fit", n)))
		if err != nil {
			return nil, nil, 0, 0, Rect{}, err
		}
		gw, err := wcsFromHeader(kw, int(kw.Float("NAXIS1")), int(kw.Float("NAXIS2")))
		if err != nil {
			return nil, nil, 0, 0, Rect{}, fmt.Errorf("%s: %w", pn.Object, err)
		}
		// Its pixels come from the mosaic; a non-nil Data marks it present.
		panels = append(panels, layoutPanel{WCS: gw, Data: []float32{}})
		if !solved {
			refWCS, refRot, solved = gw, pn.Rotation, true
		}
	}
	if !solved {
		return nil, nil, 0, 0, Rect{}, fmt.Errorf("no solved panel")
	}
	for _, pn := range g.Panels {
		if _, ok := present[pn.Object]; ok {
			continue
		}
		panels = append(panels, layoutPanel{WCS: refWCS.placed(pn.RA, pn.Dec, pn.Rotation-refRot)})
	}
	b, err := os.ReadFile(mosaicFile)
	if err != nil {
		return nil, nil, 0, 0, Rect{}, err
	}
	kw, err := frameheader.Parse(b)
	if err != nil {
		return nil, nil, 0, 0, Rect{}, err
	}
	im, err := imagedata.Decode(b)
	if err != nil {
		return nil, nil, 0, 0, Rect{}, err
	}
	mw, err := wcsFromHeader(kw, im.W, im.H)
	if err != nil {
		return nil, nil, 0, 0, Rect{}, fmt.Errorf("mosaic: %w", err)
	}
	canvas, img, w, h, r = mosaicPreview(panels, refWCS, mw, im.Plane(0))
	return canvas, img, w, h, r, nil
}

// mosaicPreview reprojects the mosaic (top row first, its plate solution
// mw) onto a canvas framing the panels, as layoutPreview describes.
func mosaicPreview(panels []layoutPanel, ref, mw wcs, data []float32) (canvas, img []float32, w, h int, r Rect) {
	l, bin := newLayout(panels, ref, mosaicPreviewWidth)
	m := layoutPanel{WCS: mw, Bin: bin}
	m.Data, m.W, m.H = binImage(data, mw.width, mw.height, bin)
	// One image: render's level matching and feathering leave it as it is.
	canvas = l.render([]layoutPanel{m})
	img = l.stretchedWithOutlines(canvas, panels)
	// The preview is cropped to leave out the wedges around the outside
	// that no panel reaches, counting missing panels' frames as covered so
	// their gaps stay in. The linear canvas stays whole, so every filter's
	// lines up, and carries the crop.
	return canvas, img, l.W, l.H, l.panelCrop(canvas, panels)
}

// mosaicPreviewWidth caps the layout canvas's width in pixels.
const mosaicPreviewWidth = 2400

// encodeGray encodes a 0-1 image as a JPEG.
func encodeGray(v []float32, w, h int) ([]byte, error) {
	img := image.NewGray(image.Rect(0, 0, w, h))
	for i, x := range v {
		img.Pix[i] = to8(x)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// flattenPanel takes a plane fitted to the sky out of a panel master,
// keeping its mean level and its header. The sky is sampled on a grid and
// fitted again without samples more than 2.5σ off, which are stars,
// galaxies and nebulosity. A plane can't follow nebulosity the way a
// higher-order surface would.
func flattenPanel(file string, sat float32) error {
	b, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	im, err := imagedata.Decode(b)
	if err != nil {
		return err
	}
	if im.C != 1 {
		return fmt.Errorf("%d channels", im.C)
	}
	cards, err := copiedCards(b)
	if err != nil {
		return err
	}
	data := im.Plane(0)
	_, bx, by, ok := fitSkyPlane(data, im.W, im.H, sat)
	if !ok {
		return nil
	}
	for y := range im.H {
		ny := float64(y)/float64(im.H) - 0.5
		for x := range im.W {
			i := y*im.W + x
			if v := data[i]; v != 0 && v < sat {
				nx := float64(x)/float64(im.W) - 0.5
				data[i] = v - float32(bx*nx+by*ny)
				if data[i] <= 0 {
					data[i] = 1e-7 // 0 marks no data
				}
			}
		}
	}
	return writeFITSFile(file, im.W, im.H, 1, data, cards)
}

// fitSkyPlane fits a + bx·x + by·y, x and y running -0.5 to 0.5 across the
// frame, to the sky of a frame.
func fitSkyPlane(data []float32, w, h int, sat float32) (a, bx, by float64, ok bool) {
	stride := max(1, int(math.Sqrt(float64(w*h)/40_000)))
	type sample struct{ x, y, v float64 }
	var s []sample
	for y := stride / 2; y < h; y += stride {
		for x := stride / 2; x < w; x += stride {
			if v := data[y*w+x]; v != 0 && v < sat {
				s = append(s, sample{float64(x)/float64(w) - 0.5, float64(y)/float64(h) - 0.5, float64(v)})
			}
		}
	}
	for range 5 {
		if len(s) < 100 {
			return 0, 0, 0, false
		}
		// Least squares for three unknowns, by the normal equations.
		var n, sx, sy, sxx, syy, sxy, sv, sxv, syv float64
		for _, p := range s {
			n++
			sx, sy, sxx, syy, sxy = sx+p.x, sy+p.y, sxx+p.x*p.x, syy+p.y*p.y, sxy+p.x*p.y
			sv, sxv, syv = sv+p.v, sxv+p.x*p.v, syv+p.y*p.v
		}
		m := [3][4]float64{{n, sx, sy, sv}, {sx, sxx, sxy, sxv}, {sy, sxy, syy, syv}}
		for c := range 3 {
			piv := m[c][c]
			if math.Abs(piv) < 1e-12 {
				return 0, 0, 0, false
			}
			for r := range 3 {
				if r == c {
					continue
				}
				f := m[r][c] / piv
				for k := c; k < 4; k++ {
					m[r][k] -= f * m[c][k]
				}
			}
		}
		a, bx, by = m[0][3]/m[0][0], m[1][3]/m[1][1], m[2][3]/m[2][2]
		res := make([]float64, len(s))
		for i, p := range s {
			res[i] = math.Abs(p.v - a - bx*p.x - by*p.y)
		}
		limit := 2.5 * madToSigma * median(res)
		kept := s[:0:0]
		for i, p := range s {
			if res[i] <= limit {
				kept = append(kept, p)
			}
		}
		if len(kept) == len(s) {
			break
		}
		s = kept
	}
	return a, bx, by, true
}

// solvePanel plate solves panel n from its target's position and the
// optics, saving it as pan_n for the mosaic sequence. Deep masters can have
// so many faint stars that matching fails at full size, so a failed solve is
// retried on a downscaled image.
func (p *Pipeline) solvePanel(ctx context.Context, dir string, n int, pos panel, focal, pixel float64) error {
	var err error
	for _, extra := range []string{"", solveDownscale} {
		script := p.sirilPreamble(true) + fmt.Sprintf("load panel%02d\nplatesolve %.6f,%.6f -focal=%.2f -pixelsize=%.3f -force%s\nsave pan_%05d\n",
			n, pos.RA, pos.Dec, focal, pixel, extra, n)
		if _, err = p.siril.Run(ctx, dir, script); err == nil {
			return nil
		}
	}
	return fmt.Errorf("plate solve: %w", err)
}

// solved reports whether a FITS file's header holds a plate solution.
func solved(file string) bool {
	f, err := os.Open(file)
	if err != nil {
		return false
	}
	defer f.Close()
	b := make([]byte, 64*2880)
	n, _ := io.ReadFull(f, b)
	cards, err := frameheader.ParseCards(b[:n])
	return err == nil && hasCard(cards, "CRVAL1") && hasCard(cards, "CRPIX1")
}

func mosaicPrefix(project, filter string) string {
	return path.Join("mosaics", project, filter)
}

// optics reads the focal length and binned pixel size from the header of one
// of the panel's lights.
func (p *Pipeline) optics(ctx context.Context, object, filter string) (focal, pixel float64, err error) {
	var f app.Frame
	if err := p.db.WithContext(ctx).Where("type = ? AND object = ? AND filter = ? AND index_error IS NULL", "LIGHT", object, filter).
		Order("date_obs DESC").First(&f).Error; err != nil {
		return 0, 0, fmt.Errorf("find a light: %w", err)
	}
	kw, err := indexer.ReadHeader(ctx, p.s3, p.source, minio.ObjectInfo{Key: f.Key, Size: f.Size})
	if err != nil {
		return 0, 0, fmt.Errorf("read %s: %w", f.Key, err)
	}
	focal, pixel = kw.Float("FOCALLEN"), kw.Float("XPIXSZ")
	if bin := kw.Float("XBINNING"); bin > 1 {
		pixel *= bin
	}
	if !(focal > 0) || !(pixel > 0) { // also false for NaN, a missing keyword
		return 0, 0, fmt.Errorf("%s has no FOCALLEN or XPIXSZ", f.Key)
	}
	return focal, pixel, nil
}
