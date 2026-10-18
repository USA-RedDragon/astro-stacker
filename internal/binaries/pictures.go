package binaries

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"math"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/stacking"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting"
	"gorm.io/gorm"
)

// pictureWork is one source whose share picture, and for a binary its
// finding chart, is to be made from its target's colour picture.
type pictureWork struct {
	src   app.DiscoverySource
	star  *Star
	stamp string
	cover *app.Cover
	share *app.ShareImage
	chart bool
}

// coverStamp names the colour picture and plate solution a picture is made
// from; it changes whenever the stacker renders the cover again.
func coverStamp(c *app.Cover, tr *app.TargetReference) string {
	if c == nil {
		return "none"
	}
	h := sha256.New()
	fmt.Fprintf(h, "%s|%d|%d", c.PreviewKey, c.UpdatedAt.UnixNano(), c.Version)
	if tr != nil && tr.WCS != nil {
		h.Write([]byte(*tr.WCS))
	}
	return fmt.Sprintf("%x", h.Sum(nil)[:12])
}

// WantShare asks for a source's share picture, for the Discoveries panel:
// it is made at the next look and kept up to date afterwards.
func (s *Service) WantShare(id int) {
	s.mu.Lock()
	if s.want == nil {
		s.want = map[int]bool{}
	}
	s.want[id] = true
	s.mu.Unlock()
	s.Kick()
}

func (s *Service) wanted() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]int, 0, len(s.want))
	for id := range s.want {
		out = append(out, id)
	}
	return out
}

// shareWork lists the pictures to make: binaries' and wanted sources'
// whose colour picture changed since their last picture.
func (s *Service) shareWork(ctx context.Context, stars []Star) ([]pictureWork, error) {
	db := s.DB.WithContext(ctx)
	ids := map[int]*Star{}
	for i := range stars {
		ids[stars[i].Source.ID] = &stars[i]
	}
	var rows []app.ShareImage
	if err := db.Find(&rows).Error; err != nil {
		return nil, err
	}
	shares := map[int]*app.ShareImage{}
	all := map[int]bool{}
	for i := range rows {
		shares[rows[i].SourceID] = &rows[i]
		all[rows[i].SourceID] = true
	}
	for id := range ids {
		all[id] = true
	}
	for _, id := range s.wanted() {
		all[id] = true
	}
	list := make([]int, 0, len(all))
	for id := range all {
		list = append(list, id)
	}
	var srcs []app.DiscoverySource
	if err := db.Where("id IN ?", list).Find(&srcs).Error; err != nil {
		return nil, err
	}
	covers := map[string]*app.Cover{}
	refs := map[string]*app.TargetReference{}
	var out []pictureWork
	for _, src := range srcs {
		if _, ok := covers[src.Object]; !ok {
			var c []app.Cover
			if err := db.Where("subject = ?", src.Object).Limit(1).Find(&c).Error; err != nil {
				return nil, err
			}
			var tr []app.TargetReference
			if err := db.Where("object = ?", src.Object).Limit(1).Find(&tr).Error; err != nil {
				return nil, err
			}
			covers[src.Object], refs[src.Object] = nil, nil
			if len(c) == 1 && c[0].PreviewKey != "" {
				covers[src.Object] = &c[0]
			}
			if len(tr) == 1 {
				refs[src.Object] = &tr[0]
			}
		}
		stamp := coverStamp(covers[src.Object], refs[src.Object])
		w := pictureWork{src: src, star: ids[src.ID], stamp: stamp, cover: covers[src.Object], share: shares[src.ID]}
		need := w.share == nil || w.share.Stamp != stamp
		if st := w.star; st != nil && st.Facts != nil && (st.Row == nil || st.Row.ChartStamp != stamp) {
			w.chart, need = true, true
		}
		if need {
			out = append(out, w)
		}
	}
	return out, nil
}

// skyPos is where to mark a source now: its Gaia star, else the spot.
func (s *Service) skyPos(w pictureWork) (float64, float64) {
	if w.star != nil && w.star.Facts != nil && w.star.Facts.Gaia != nil {
		return w.star.Facts.Gaia.Now(s.now())
	}
	return w.src.RA, w.src.Dec
}

// makePictures makes a source's share picture and, for a binary, its
// finding chart, from its target's colour picture.
func (s *Service) makePictures(ctx context.Context, w pictureWork) error {
	db := s.DB.WithContext(ctx)
	sh := app.ShareImage{SourceID: w.src.ID, Stamp: w.stamp, UpdatedAt: s.now()}
	if w.share != nil {
		sh = *w.share
	}
	same := w.share != nil && w.share.Stamp == w.stamp && w.share.State == ShareReady
	sh.Stamp, sh.UpdatedAt = w.stamp, s.now()
	chart := FileFact{State: FileNone}
	img, frame, wcs, err := s.coverOf(ctx, w)
	var mark Mark
	if err == nil {
		mark, err = s.markOf(ctx, w, frame, wcs)
	}
	if err != nil {
		sh.State, sh.Note = ShareNone, err.Error()
		chart.Note = "Not made yet: " + err.Error()
	} else {
		label, name := s.words(ctx, w)
		var b []byte
		var perr error
		if !same {
			b, perr = SharePicture(img, mark, label, name)
		}
		switch {
		case same:
		case perr != nil:
			sh.State, sh.Note = ShareFailed, perr.Error()
		default:
			old := sh.Key
			sh.Version++
			sh.Key = fmt.Sprintf("binaries/share/%d/v%d.jpg", w.src.ID, sh.Version)
			if err := s.Objects.Put(ctx, sh.Key, b, "image/jpeg"); err != nil {
				return err
			}
			sh.State, sh.Note, sh.Bytes = ShareReady, "", len(b)
			if old != "" && old != sh.Key {
				_ = s.Objects.RemovePrefix(ctx, old)
			}
		}
		if w.chart && !mark.Track {
			ra, dec := s.skyPos(w)
			chart = s.putPNG(ctx, w.src.ID, FileChart, func() ([]byte, string, error) {
				return FindingChart(img, chartGeometry(frame, wcs, ra, dec, mark), s.labelName(ctx, w))
			})
		}
	}
	if err := db.Save(&sh).Error; err != nil {
		return err
	}
	if w.chart && w.star != nil && w.star.Row != nil {
		files := Files(w.star.Row)
		files[FileChart] = chart
		raw, err := json.Marshal(files)
		if err != nil {
			return err
		}
		return db.Model(&app.BinaryFacts{}).Where("source_id = ?", w.src.ID).Updates(map[string]any{"files": string(raw), "chart_stamp": w.stamp}).Error
	}
	return nil
}

// coverOf reads the target's colour picture and where it sits on the
// masters.
func (s *Service) coverOf(ctx context.Context, w pictureWork) (image.Image, stacking.CoverFrame, *discoveries.WCS, error) {
	if w.cover == nil {
		return nil, stacking.CoverFrame{}, nil, fmt.Errorf("no colour picture of %s yet", w.src.Object)
	}
	if s.Frame == nil {
		return nil, stacking.CoverFrame{}, nil, errors.New("no plate solutions to place the star")
	}
	wcs, _, _, err := s.Frame(ctx, w.src.Object)
	if err != nil {
		return nil, stacking.CoverFrame{}, nil, fmt.Errorf("the target's plate solution: %w", err)
	}
	var stacks []app.Stack
	if err := s.DB.WithContext(ctx).Where("object = ?", w.src.Object).Find(&stacks).Error; err != nil {
		return nil, stacking.CoverFrame{}, nil, err
	}
	frame, ok := stacking.CoverFrameOf(w.src.Object, stacks)
	if !ok {
		return nil, stacking.CoverFrame{}, nil, errors.New("the colour picture's frame could not be worked out from the masters")
	}
	b, err := s.Objects.Get(ctx, w.cover.PreviewKey)
	if err != nil {
		return nil, stacking.CoverFrame{}, nil, fmt.Errorf("read the colour picture: %w", err)
	}
	img, err := decodeImage(b)
	if err != nil {
		return nil, stacking.CoverFrame{}, nil, fmt.Errorf("decode the colour picture: %w", err)
	}
	if bb := img.Bounds(); bb.Dx() != frame.W || bb.Dy() != frame.H {
		return nil, stacking.CoverFrame{}, nil, fmt.Errorf("the colour picture is %dx%d, its masters give %dx%d; it is made again soon", bb.Dx(), bb.Dy(), frame.W, frame.H)
	}
	return img, frame, wcs, nil
}

// markOf is where the source sits on the colour picture: the star, or for
// a moving object its track from first to last position.
func (s *Service) markOf(ctx context.Context, w pictureWork, frame stacking.CoverFrame, wcs *discoveries.WCS) (Mark, error) {
	at := func(ra, dec float64) (float64, float64, bool) {
		x, y, ok := wcs.PixelOf(ra, dec)
		if !ok {
			return 0, 0, false
		}
		cx, cy := frame.Pixel(x, y)
		return cx, cy, finiteAll(cx, cy) && cx >= 0 && cy >= 0 && cx < float64(frame.W) && cy < float64(frame.H)
	}
	if w.src.Kind == app.DiscoveryKindMover {
		var cands []app.DiscoveryCandidate
		if err := s.DB.WithContext(ctx).Where("source_id = ? AND track <> ''", w.src.ID).Order("night").Find(&cands).Error; err != nil {
			return Mark{}, err
		}
		for _, c := range cands {
			var t discoveries.TrackRecord
			if json.Unmarshal([]byte(c.Track), &t) != nil || len(t.Points) < 2 {
				continue
			}
			a, b := t.Points[0], t.Points[len(t.Points)-1]
			x0, y0, ok0 := at(a.RA, a.Dec)
			x1, y1, ok1 := at(b.RA, b.Dec)
			if ok0 && ok1 {
				return Mark{X: x0, Y: y0, X1: x1, Y1: y1, Track: true}, nil
			}
		}
		return Mark{}, errors.New("the moving object's track is not on the colour picture")
	}
	ra, dec := s.skyPos(w)
	x, y, ok := at(ra, dec)
	if !ok {
		return Mark{}, errors.New("the star is outside the colour picture")
	}
	return Mark{X: x, Y: y}, nil
}

// words are the label above the mark and the name below it.
func (s *Service) words(ctx context.Context, w pictureWork) (string, string) {
	return kindWords(w), s.labelName(ctx, w)
}

func kindWords(w pictureWork) string {
	switch {
	case w.star != nil:
		return "the new variable star"
	case w.src.Kind == app.DiscoveryKindMover:
		return "the moving object"
	}
	return "the star that changed"
}

// labelName is the source's name: the binary's, else the star the vet
// named at the spot, else its find number.
func (s *Service) labelName(ctx context.Context, w pictureWork) string {
	if w.star != nil {
		n, _ := w.star.Names()
		return n
	}
	var v []app.DiscoveryVet
	if err := s.DB.WithContext(ctx).Where("source_id = ?", w.src.ID).Limit(1).Find(&v).Error; err == nil && len(v) == 1 && v[0].Report != "" {
		var r vetting.Report
		if json.Unmarshal([]byte(v[0].Report), &r) == nil {
			if n, _, _ := strings.Cut(r.Star, " · "); n != "" && !strings.HasPrefix(n, "No catalogued") {
				return n
			}
		}
	}
	return fmt.Sprintf("find #%d", w.src.ID)
}

// chartGeometry is how north and east lie around the star on the colour
// picture and its pixel size.
func chartGeometry(frame stacking.CoverFrame, wcs *discoveries.WCS, ra, dec float64, m Mark) ChartGeometry {
	g := ChartGeometry{X: m.X, Y: m.Y, Scale: wcs.PixelScale() * float64(frame.Bin)}
	step := 0.01
	dir := func(r, d float64) [2]float64 {
		x, y, ok := wcs.PixelOf(r, d)
		if !ok {
			return [2]float64{}
		}
		cx, cy := frame.Pixel(x, y)
		return [2]float64{cx - m.X, cy - m.Y}
	}
	g.North = dir(ra, dec+step)
	g.East = dir(ra+step/math.Max(math.Cos(dec*math.Pi/180), 1e-6), dec)
	return g
}

// Share is a source's share picture row; gorm.ErrRecordNotFound when it
// has none yet, after which it is asked for.
func (s *Service) Share(ctx context.Context, id int) (app.ShareImage, error) {
	var rows []app.ShareImage
	if err := s.DB.WithContext(ctx).Where("source_id = ?", id).Limit(1).Find(&rows).Error; err != nil {
		return app.ShareImage{}, err
	}
	if len(rows) == 0 {
		var n int64
		if err := s.DB.WithContext(ctx).Model(&app.DiscoverySource{}).Where("id = ?", id).Count(&n).Error; err != nil {
			return app.ShareImage{}, err
		}
		if n == 0 {
			return app.ShareImage{}, gorm.ErrRecordNotFound
		}
		s.WantShare(id)
		return app.ShareImage{SourceID: id, State: SharePending, Note: "Being made from the target's colour picture."}, nil
	}
	return rows[0], nil
}

// ShareBytes is a source's current share picture.
func (s *Service) ShareBytes(ctx context.Context, id int) ([]byte, app.ShareImage, error) {
	sh, err := s.Share(ctx, id)
	if err != nil {
		return nil, sh, err
	}
	if sh.State != ShareReady || sh.Key == "" {
		return nil, sh, gorm.ErrRecordNotFound
	}
	b, err := s.Objects.Get(ctx, sh.Key)
	return b, sh, err
}
