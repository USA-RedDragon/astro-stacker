package stacking

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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
	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/indexer"
	"github.com/USA-RedDragon/astro-stacker/internal/preview"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/minio/minio-go/v7"
	"gorm.io/gorm"
)

// StageAssembling is reported while a mosaic is being built.
const StageAssembling = "assembling"

// panelName matches a mosaic panel target: "Cygnis Loop Panel 2".
var panelName = regexp.MustCompile(`(?i)\bpanel\s*(\d+)\s*$`)

type panel struct {
	Object string
	Number int
	RA     float64 // degrees
	Dec    float64 // degrees
}

// mosaicGroup is a Target Scheduler project whose targets are panels.
type mosaicGroup struct {
	Project string
	Panels  []panel
}

// mosaicGroups reads the projects with two or more panel targets.
func mosaicGroups(ctx context.Context, sched *gorm.DB) ([]mosaicGroup, error) {
	var rows []struct {
		Project string
		Name    string
		RA      float64
		Dec     float64
	}
	if err := sched.WithContext(ctx).Table("target").
		Select(`project.name AS project, target.name AS name, target.ra AS ra, target.dec AS dec`).
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
		byProject[r.Project] = append(byProject[r.Project], panel{Object: r.Name, Number: n, RA: r.RA * 15, Dec: r.Dec})
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

// runMosaics builds mosaics that are due every interval until ctx ends.
func (p *Pipeline) runMosaics(ctx context.Context, interval time.Duration) {
	for {
		if err := p.MosaicsOnce(ctx); err != nil && ctx.Err() == nil {
			slog.Error("Building mosaics failed", "error", err)
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
		for filter, masters := range byFilter {
			if len(masters) < 2 {
				continue
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
	mosaic.Signature, mosaic.PanelsTotal = sig, len(g.Panels)
	if buildErr != nil {
		// Recorded so it isn't retried until a panel master changes.
		msg := buildErr.Error()
		mosaic.Error = &msg
	} else {
		mosaic.Error = nil
		mosaic.Panels = len(masters)
		mosaic.UpdatedAt = time.Now()
	}
	if err := p.db.WithContext(ctx).Save(&mosaic).Error; err != nil {
		return err
	}
	if buildErr != nil {
		return buildErr
	}
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
		focal, pixel, err := p.optics(ctx, m.Object, filter)
		if err != nil {
			return fmt.Errorf("%s: %w", m.Object, err)
		}
		if err := p.solvePanel(ctx, dir, i+1, positions[m.Object], focal, pixel); err != nil {
			return fmt.Errorf("%s: %w", m.Object, err)
		}
	}
	// With every panel solved, seqplatesolve only computes the astrometric
	// registration; the panels are then projected onto one canvas and
	// blended, levels matched on the overlaps and edges feathered.
	p.progress("Mosaic: "+g.Project, filter, StageAssembling, len(masters), len(masters)+1)
	script := p.sirilPreamble(true) +
		"seqplatesolve pan -nocache\n" +
		"seqapplyreg pan -framing=max\n" +
		"stack r_pan rej none -maximize -overlap_norm -feather=300 -32b -out=mosaic\n"
	if _, err := p.siril.Run(ctx, dir, script); err != nil {
		return err
	}

	out := filepath.Join(dir, "mosaic.fit")
	b, err := os.ReadFile(out)
	if err != nil {
		return err
	}
	im, err := imagedata.Decode(b)
	if err != nil {
		return err
	}
	jpg, err := preview.Render(im, preview.DefaultOptions)
	if err != nil {
		return err
	}
	linear, err := linearPreview(im)
	if err != nil {
		return err
	}
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
