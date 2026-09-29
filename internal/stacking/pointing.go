package stacking

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

// OffTargetDegrees is how far from its target a sub's mount pointing may be
// before the sub is left out: more than half the frame's diagonal, so a
// re-framed target's subs still count, while a mount that parked or lost
// its position (pointing at 0/0, say) doesn't.
const OffTargetDegrees = 2.0

// clusterDegrees is how close two subs' pointings must be to count as the
// same framing when choosing a registration reference.
const clusterDegrees = 0.2

// targetPositions maps Target Scheduler targets to their coordinates.
func targetPositions(ctx context.Context, sched *gorm.DB) (map[string][2]float64, error) {
	var rows []struct {
		Name    string
		RA, Dec float64
	}
	if err := sched.WithContext(ctx).Table("target").Select("name, ra, dec").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load target positions: %w", err)
	}
	out := make(map[string][2]float64, len(rows))
	for _, r := range rows {
		out[r.Name] = [2]float64{r.RA * 15, r.Dec} // RA is stored in hours
	}
	return out, nil
}

// separation is the angle between two sky positions in degrees.
func separation(ra1, dec1, ra2, dec2 float64) float64 {
	d1, d2 := dec1*deg, dec2*deg
	c := math.Sin(d1)*math.Sin(d2) + math.Cos(d1)*math.Cos(d2)*math.Cos((ra1-ra2)*deg)
	return math.Acos(math.Max(-1, math.Min(1, c))) / deg
}

// mainFraming keeps the candidates in the most common framing: those with
// the most others pointed within clusterDegrees. Candidates without a
// recorded pointing are kept only if none has one.
func mainFraming(cands []candidate) []candidate {
	var pointed []candidate
	for _, c := range cands {
		if c.frame.MountRA != nil && c.frame.MountDec != nil {
			pointed = append(pointed, c)
		}
	}
	if len(pointed) == 0 {
		return cands
	}
	neighbours := make([]int, len(pointed))
	most := 0
	for i, a := range pointed {
		for _, b := range pointed {
			if separation(*a.frame.MountRA, *a.frame.MountDec, *b.frame.MountRA, *b.frame.MountDec) < clusterDegrees {
				neighbours[i]++
			}
		}
		most = max(most, neighbours[i])
	}
	var out []candidate
	for i, c := range pointed {
		if neighbours[i] == most {
			out = append(out, c)
		}
	}
	return out
}

// A target's reference is replaced when at least reReferenceFailures of its
// on-target subs, and reReferenceShare of those tried, fail to register to
// it, at most maxReReferences times.
const (
	reReferenceFailures = 10
	reReferenceShare    = 0.3
	maxReReferences     = 2
)

// maybeReReference replaces a target's registration reference when many of
// its subs won't register to it, as when the reference came from a framing
// few other subs share, and restacks the target from scratch against a new
// one. The caller holds the target's claim.
func (p *Pipeline) maybeReReference(ctx context.Context, object string) (bool, error) {
	var counts struct{ Failed, Added int64 }
	if err := p.db.WithContext(ctx).Model(&app.StackFrame{}).
		Joins("JOIN frames f ON f.id = stack_frames.frame_id").
		Where("f.object = ?", object).
		Select("SUM(CASE WHEN stack_frames.status = ? THEN 1 ELSE 0 END) AS failed, "+
			"SUM(CASE WHEN stack_frames.status = ? THEN 1 ELSE 0 END) AS added",
			app.StackStatusRegistration, app.StackStatusAdded).
		Scan(&counts).Error; err != nil {
		return false, err
	}
	if counts.Failed < reReferenceFailures || float64(counts.Failed) < reReferenceShare*float64(counts.Failed+counts.Added) {
		return false, nil
	}
	var reset app.ReferenceReset
	err := p.db.WithContext(ctx).Where("object = ?", object).First(&reset).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return false, err
	}
	if reset.Count >= maxReReferences {
		return false, nil
	}
	slog.Warn("Replacing registration reference; restacking target", "object", object,
		"failed", counts.Failed, "added", counts.Added, "reset", reset.Count+1)
	reset.Object, reset.Count, reset.At = object, reset.Count+1, time.Now()
	if err := p.restack(ctx, object, &reset); err != nil {
		return false, err
	}
	return true, nil
}

// Restack forgets a target's masters, registration reference and every
// decision about its lights, so it is stacked again from scratch with a
// reference chosen afresh. It does nothing while a worker is stacking it.
func (p *Pipeline) Restack(ctx context.Context, object string) error {
	if !p.hold(object) {
		return fmt.Errorf("%s is being stacked; try again shortly", object)
	}
	defer p.release(object)
	slog.Warn("Restacking target on request", "object", object)
	return p.restack(ctx, object, nil)
}

// restack does Restack's work, saving reset (the automatic re-reference
// count) with it when given.
func (p *Pipeline) restack(ctx context.Context, object string, reset *app.ReferenceReset) error {
	err := p.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("frame_id IN (?)", tx.Model(&app.Frame{}).Select("id").Where("object = ?", object)).
			Delete(&app.StackFrame{}).Error; err != nil {
			return err
		}
		if err := tx.Where("object = ?", object).Delete(&app.Stack{}).Error; err != nil {
			return err
		}
		if err := tx.Where("object = ?", object).Delete(&app.TargetReference{}).Error; err != nil {
			return err
		}
		if reset != nil {
			return tx.Save(reset).Error
		}
		return nil
	})
	if err != nil {
		return err
	}
	h := sha256.Sum256([]byte(object))
	_ = os.Remove(filepath.Join(p.workDir, "references", hex.EncodeToString(h[:8])+".fit"))
	return nil
}
