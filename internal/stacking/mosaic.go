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

// runMosaics builds mosaics and linear fits that are due every interval
// until ctx ends.
func (p *Pipeline) runMosaics(ctx context.Context, interval time.Duration) {
	for {
		if err := p.MosaicsOnce(ctx); err != nil && ctx.Err() == nil {
			slog.Error("Building mosaics failed", "error", err)
		}
		if err := p.linearFitsOnce(ctx); err != nil && ctx.Err() == nil {
			slog.Error("LinearFit failed", "error", err)
		}
		if err := p.cometsOnce(ctx); err != nil && ctx.Err() == nil {
			slog.Error("Comet masters failed", "error", err)
		}
		if err := p.recalibrateDarks(ctx); err != nil && ctx.Err() == nil {
			slog.Error("Checking for new darks failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
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
	// The download: with two or more panels, Siril's full-resolution stack
	// (seqplatesolve then only computes the astrometric registration from
	// the solutions, and the panels are projected onto one canvas and
	// blended, levels matched on the overlaps and edges feathered); with
	// one, the solved panel itself.
	p.progress("Mosaic: "+g.Project, filter, StageAssembling, len(masters), len(masters)+1)
	out := filepath.Join(dir, "pan_00001.fit")
	if len(masters) >= 2 {
		script := p.sirilPreamble(true) +
			"seqplatesolve pan -nocache\n" +
			"seqapplyreg pan -framing=max\n" +
			"stack r_pan rej none -maximize -overlap_norm -feather=300 -32b -out=mosaic\n"
		if _, err := p.siril.Run(ctx, dir, script); err != nil {
			return err
		}
		out = filepath.Join(dir, "mosaic.fit")
	}
	// The previews: every panel of the project on one canvas, so
	// panels without a master yet show as outlined gaps where they go, and
	// every filter's mosaic lines up for colour.
	canvas, img, cw, ch, r, err := p.layoutPreview(dir, g, masters)
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
		"mosaic.fit": func(k string) error { return p.upload(ctx, out, k, "application/fits") },
		"preview.jpg": func(k string) error {
			return p.putBytes(ctx, k, jpg, minio.PutObjectOptions{ContentType: "image/jpeg"})
		},
		"linear.bin": func(k string) error {
			return p.putBytes(ctx, k, linear, minio.PutObjectOptions{ContentType: "application/octet-stream", ContentEncoding: "gzip"})
		},
	} {
		k := path.Join(prefix, name)
		if err := up(k); err != nil {
			return err
		}
		keys[name] = k
	}
	master, prev, lin := keys["mosaic.fit"], keys["preview.jpg"], keys["linear.bin"]
	mosaic.MasterKey, mosaic.PreviewKey, mosaic.LinearKey = &master, &prev, &lin
	mosaic.Width, mosaic.Height = im.W, im.H
	return nil
}

// layoutPreview puts every panel of the project on one canvas, turned like
// the panels: solved panels
// from their plate solutions, missing ones placed from Target Scheduler,
// framed like the first solved panel. It returns the linear canvas and a
// stretched copy with the missing panels outlined.
func (p *Pipeline) layoutPreview(dir string, g mosaicGroup, masters []app.Stack) (canvas, img []float32, w, h int, r Rect, err error) {
	present := map[string]int{} // object -> pan file number
	for i, m := range masters {
		present[m.Object] = i + 1
	}
	var ref *layoutPanel
	var refRot float64
	panels := make([]layoutPanel, 0, len(g.Panels))
	files := map[int][]byte{}
	for _, pn := range g.Panels {
		n, ok := present[pn.Object]
		if !ok {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("pan_%05d.fit", n)))
		if err != nil {
			return nil, nil, 0, 0, Rect{}, err
		}
		kw, err := frameheader.Parse(b)
		if err != nil {
			return nil, nil, 0, 0, Rect{}, err
		}
		gw, err := wcsFromHeader(kw, int(kw.Float("NAXIS1")), int(kw.Float("NAXIS2")))
		if err != nil {
			return nil, nil, 0, 0, Rect{}, fmt.Errorf("%s: %w", pn.Object, err)
		}
		files[len(panels)] = b
		panels = append(panels, layoutPanel{WCS: gw})
		if ref == nil {
			ref, refRot = &panels[len(panels)-1], pn.Rotation
		}
	}
	if ref == nil {
		return nil, nil, 0, 0, Rect{}, fmt.Errorf("no solved panel")
	}
	refWCS := ref.WCS
	for _, pn := range g.Panels {
		if _, ok := present[pn.Object]; ok {
			continue
		}
		panels = append(panels, layoutPanel{WCS: refWCS.placed(pn.RA, pn.Dec, pn.Rotation-refRot)})
	}
	l, bin := newLayout(panels, refWCS, mosaicPreviewWidth)
	for i, b := range files {
		im, err := imagedata.Decode(b)
		if err != nil {
			return nil, nil, 0, 0, Rect{}, err
		}
		panels[i].Data, panels[i].W, panels[i].H = binImage(im.Data, im.W, im.H, bin)
		panels[i].Bin = bin
	}
	canvas = l.render(panels)
	img = l.stretchedWithOutlines(canvas, panels)
	// The preview is cropped to leave out the wedges around the outside
	// that no panel reaches, counting missing panels' frames as covered so
	// their gaps stay in. The linear canvas stays whole, so every filter's
	// lines up, and carries the crop.
	return canvas, img, l.W, l.H, l.panelCrop(canvas, panels), nil
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

// solvePanel plate solves panel n from its target's position and the
// optics, saving it as pan_n for the mosaic sequence. Deep masters can have
// so many faint stars that matching fails at full size, so a failed solve is
// retried on a downscaled image.
func (p *Pipeline) solvePanel(ctx context.Context, dir string, n int, pos panel, focal, pixel float64) error {
	var err error
	for _, extra := range []string{"", " -downscale"} {
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
