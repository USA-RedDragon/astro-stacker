package publicframe

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"path"
	"strconv"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/preview"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/minio/minio-go/v7"
	"gorm.io/gorm"
)

// Revision changes whenever a frame would render differently, so stored
// frames are rendered again.
const Revision = 1

// maxFallbacks is how many older accepted lights are tried when a target's
// newest won't render; a light that failed is tried again after retryAfter.
const (
	maxFallbacks = 3
	retryAfter   = time.Hour
)

// Candidate is an accepted light that could be shown: in a master, with its
// registered copy stored.
type Candidate struct {
	FrameID       int
	Object        string
	Filter        string
	DateObs       time.Time
	RegisteredKey string
	StackID       int
}

// Renderer keeps every recently imaged target's public frame current. It
// runs in the background like the previewer; nothing renders on request.
type Renderer struct {
	s3     *minio.Client
	bucket string
	db     *gorm.DB
	opts   Options
	maxAge time.Duration

	// render makes one frame; tests replace it.
	render func(ctx context.Context, c Candidate) (Result, error)

	mu sync.Mutex
	// failed holds when lights failed to render, so a target falls back to
	// its previous good one instead of retrying every pass.
	failed   map[int]time.Time
	patterns map[int]*Pattern // by reference width
}

// NewRenderer renders into bucket (the processed bucket, under public/)
// the frames of targets with an accepted light from the last maxAge.
func NewRenderer(s3 *minio.Client, bucket string, db *gorm.DB, opts Options, maxAge time.Duration) *Renderer {
	r := &Renderer{s3: s3, bucket: bucket, db: db, opts: opts, maxAge: maxAge, failed: map[int]time.Time{}, patterns: map[int]*Pattern{}}
	r.render = r.renderCandidate
	return r
}

// Run brings the frames up to date every interval until ctx ends.
func (r *Renderer) Run(ctx context.Context, interval time.Duration) {
	for {
		if n, err := r.Pass(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("Rendering public frames failed", "error", err)
		} else if n > 0 {
			slog.Info("Rendered public frames", "frames", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// Pass renders the frame of every target whose newest accepted light isn't
// the one shown (or was shown at an older Revision), and returns how many
// it rendered.
func (r *Renderer) Pass(ctx context.Context) (int, error) {
	var shown []app.PublicFrame
	if err := r.db.WithContext(ctx).Find(&shown).Error; err != nil {
		return 0, fmt.Errorf("load public frames: %w", err)
	}
	current := map[string]app.PublicFrame{}
	for _, f := range shown {
		current[f.Object] = f
	}
	cands, err := r.candidates(ctx, "", time.Now().Add(-r.maxAge))
	if err != nil {
		return 0, err
	}
	byObject := map[string][]Candidate{}
	for _, c := range cands {
		byObject[c.Object] = append(byObject[c.Object], c)
	}
	// A target shown but not imaged lately keeps its frame, unless that
	// light has since left its master (rescored, moon, recalibrating):
	// then it falls back to the target's newest accepted light, however
	// old, or to nothing.
	for object, f := range current {
		if _, ok := byObject[object]; ok {
			continue
		}
		added, err := r.stillAdded(ctx, f.FrameID)
		if err != nil {
			return 0, err
		}
		if added {
			continue
		}
		older, err := r.candidates(ctx, object, time.Time{})
		if err != nil {
			return 0, err
		}
		if len(older) == 0 {
			slog.Info("Withdrawing public frame: its light left its master and the target has no other", "object", object, "frame", f.FrameID)
			if err := r.withdraw(ctx, f); err != nil {
				return 0, err
			}
			continue
		}
		byObject[object] = older
	}

	rendered := 0
	for object, cs := range byObject {
		if ctx.Err() != nil {
			return rendered, ctx.Err()
		}
		var cur *app.PublicFrame
		if f, ok := current[object]; ok {
			cur = &f
		}
		r.mu.Lock()
		failed := map[int]bool{}
		for id, at := range r.failed {
			if time.Since(at) < retryAfter {
				failed[id] = true
			} else {
				delete(r.failed, id)
			}
		}
		r.mu.Unlock()
		tries := choose(cs, cur, failed)
		done := false
		for _, c := range tries {
			if err := r.renderAndStore(ctx, c, cur); err != nil {
				if ctx.Err() != nil {
					return rendered, ctx.Err()
				}
				slog.Warn("Could not render public frame; trying the target's previous light", "object", object, "frame", c.FrameID, "error", err)
				r.mu.Lock()
				r.failed[c.FrameID] = time.Now()
				r.mu.Unlock()
				continue
			}
			rendered++
			done = true
			break
		}
		if !done && len(tries) > 0 && cur != nil {
			// Nothing rendered: keep showing the current frame only if
			// its light is still in a master.
			added, err := r.stillAdded(ctx, cur.FrameID)
			if err != nil {
				return rendered, err
			}
			if !added {
				slog.Warn("Withdrawing public frame: its light left its master and no other would render", "object", object, "frame", cur.FrameID)
				if err := r.withdraw(ctx, *cur); err != nil {
					return rendered, err
				}
			}
		}
	}
	return rendered, nil
}

// choose lists the lights to try for a target, best first: its newest
// accepted lights that haven't failed, up to maxFallbacks of them. It is
// empty when the newest is what's shown already, at this Revision; if the
// one shown is among the fallbacks, the list stops before it, since
// rendering it again would change nothing.
func choose(cands []Candidate, cur *app.PublicFrame, failed map[int]bool) []Candidate {
	var out []Candidate
	for _, c := range cands {
		if failed[c.FrameID] {
			continue
		}
		if cur != nil && cur.FrameID == c.FrameID && cur.Revision == Revision {
			break
		}
		out = append(out, c)
		if len(out) == maxFallbacks {
			break
		}
	}
	return out
}

// candidates lists accepted lights newest first, of one object or all, taken
// since since (zero for any time).
func (r *Renderer) candidates(ctx context.Context, object string, since time.Time) ([]Candidate, error) {
	q := r.db.WithContext(ctx).Table("stack_frames sf").
		Select("f.id AS frame_id, f.object, f.filter, f.date_obs, sf.registered_key, sf.stack_id").
		Joins("JOIN frames f ON f.id = sf.frame_id").
		Where("sf.status = ? AND sf.registered_key IS NOT NULL AND sf.stack_id IS NOT NULL AND f.date_obs IS NOT NULL AND f.object <> ''", app.StackStatusAdded)
	if object != "" {
		q = q.Where("f.object = ?", object).Limit(maxFallbacks + 1)
	}
	if !since.IsZero() {
		q = q.Where("f.date_obs >= ?", since)
	}
	var out []Candidate
	if err := q.Order("f.date_obs DESC, f.id DESC").Scan(&out).Error; err != nil {
		return nil, fmt.Errorf("load accepted lights: %w", err)
	}
	return out, nil
}

func (r *Renderer) stillAdded(ctx context.Context, frameID int) (bool, error) {
	var n int64
	err := r.db.WithContext(ctx).Model(&app.StackFrame{}).
		Where("frame_id = ? AND status = ? AND registered_key IS NOT NULL", frameID, app.StackStatusAdded).Count(&n).Error
	return n > 0, err
}

func (r *Renderer) withdraw(ctx context.Context, f app.PublicFrame) error {
	if err := r.db.WithContext(ctx).Delete(&app.PublicFrame{}, f.ID).Error; err != nil {
		return err
	}
	r.remove(ctx, f.Key)
	return nil
}

func (r *Renderer) remove(ctx context.Context, key string) {
	if r.s3 == nil || key == "" {
		return
	}
	if err := r.s3.RemoveObject(ctx, r.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		slog.Warn("Could not remove old public frame", "key", key, "error", err)
	}
}

// Key is where a light's public frame is stored: one object per light and
// Revision, so a request never reads a frame half replaced.
func Key(object string, frameID int) string {
	return path.Join("public", object, strconv.Itoa(frameID)+"-r"+strconv.Itoa(Revision)+".jpg")
}

func (r *Renderer) renderAndStore(ctx context.Context, c Candidate, cur *app.PublicFrame) error {
	start := time.Now()
	res, err := r.render(ctx, c)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(res.JPEG)
	key := Key(c.Object, c.FrameID)
	if r.s3 != nil {
		if _, err := r.s3.PutObject(ctx, r.bucket, key, bytes.NewReader(res.JPEG), int64(len(res.JPEG)),
			minio.PutObjectOptions{ContentType: "image/jpeg", CacheControl: "no-cache"}); err != nil {
			return fmt.Errorf("upload: %w", err)
		}
	}
	date := c.DateObs
	f := app.PublicFrame{Object: c.Object, FrameID: c.FrameID, Filter: c.Filter, DateObs: &date, Key: key,
		ETag: `"` + hex.EncodeToString(sum[:16]) + `"`, Revision: Revision,
		Sigma: res.Sigma, Amplitude: res.Amplitude, RenderedAt: time.Now()}
	if cur != nil {
		f.ID = cur.ID
	}
	if err := r.db.WithContext(ctx).Save(&f).Error; err != nil {
		return fmt.Errorf("save public frame: %w", err)
	}
	if cur != nil && cur.Key != key {
		r.remove(ctx, cur.Key)
	}
	slog.Info("Rendered public frame", "object", c.Object, "filter", c.Filter, "frame", c.FrameID,
		"sigma_dn", math.Round(res.Sigma*100)/100, "watermark_dn", math.Round(res.Amplitude*100)/100,
		"noise_added", res.Added > 0,
		"duration", time.Since(start).Round(time.Millisecond))
	return nil
}

// renderCandidate renders a light from its registered copy, cropped as its
// master is, with the sky mask from its master's linear preview.
func (r *Renderer) renderCandidate(ctx context.Context, c Candidate) (Result, error) {
	var stack app.Stack
	if err := r.db.WithContext(ctx).First(&stack, c.StackID).Error; err != nil {
		return Result{}, fmt.Errorf("master: %w", err)
	}
	b, err := r.get(ctx, c.RegisteredKey)
	if err != nil {
		return Result{}, fmt.Errorf("registered sub: %w", err)
	}
	im, err := imagedata.Decode(b)
	if err != nil {
		return Result{}, err
	}
	if stack.Width > 0 && (im.W != stack.Width || im.H != stack.Height) {
		// Registered to a reference since replaced: not on the grid the
		// watermark is laid on.
		return Result{}, fmt.Errorf("registered sub is %dx%d, master %dx%d", im.W, im.H, stack.Width, stack.Height)
	}
	mask, master := r.mask(ctx, stack, im)
	r.mu.Lock()
	pattern, ok := r.patterns[im.W]
	if !ok {
		pattern = NewPattern(im.W)
		r.patterns[im.W] = pattern
	}
	r.mu.Unlock()
	return Render(Input{Sub: im, Crop: Rect{stack.CropX, stack.CropY, stack.CropW, stack.CropH}, Pattern: pattern, Mask: mask, Master: master}, r.opts)
}

// maskWidth is the width the sky mask is built at when there is no master
// preview to build it from; the master's linear preview is about this wide.
const maskWidth = 1600

// mask builds the sky mask from the master's linear preview, which it also
// returns for the master's structure; for a target's first light (no master
// yet) the mask comes from the light itself, and there is no structure to
// measure.
func (r *Renderer) mask(ctx context.Context, stack app.Stack, im *imagedata.Image) (*SkyMask, *Master) {
	if stack.LinearKey != nil {
		plane, w, h, err := r.linear(ctx, *stack.LinearKey)
		if err == nil {
			scale := math.Round(float64(im.W) / float64(w))
			return NewSkyMask(plane, w, h, scale), &Master{Plane: plane, W: w, H: h, Scale: scale}
		}
		slog.Warn("Could not read the master's linear preview; masking the sky from the light", "object", stack.Object, "filter", stack.Filter, "error", err)
	}
	f := max(1, int(math.Ceil(float64(im.W)/maskWidth)))
	b := preview.Bin(&imagedata.Image{W: im.W, H: im.H, C: 1, Data: im.Plane(0)}, f)
	return NewSkyMask(b.Data, b.W, b.H, float64(f)), nil
}

func (r *Renderer) linear(ctx context.Context, key string) ([]float32, int, int, error) {
	b, err := r.get(ctx, key)
	if err != nil {
		return nil, 0, 0, err
	}
	return DecodeLinear(b)
}

func (r *Renderer) get(ctx context.Context, key string) ([]byte, error) {
	if r.s3 == nil {
		return nil, fmt.Errorf("no object store")
	}
	obj, err := r.s3.GetObject(ctx, r.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer obj.Close()
	return io.ReadAll(obj)
}
