package stacking

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/metrics"
	"github.com/USA-RedDragon/astro-stacker/internal/quality"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Verdicts for Target Scheduler.
//
// Target Scheduler (TS) counts a sub toward its exposure plan once its own
// grader accepts it, and stops imaging a target when the plans are full. The
// stacker leaves many of those subs out of its masters (low_score, moon), so
// TS stops early. SendVerdicts tells TS: for each such sub TS still counts,
// it writes a row into stacker_verdict in the scheduler database, which
// SymmetricDS copies to the observatory, where a trigger rejects the image and
// takes one off its plan's accepted count (observatory-verdicts.sql in the
// cluster repo). The trigger acts only while TS is not grading that plan, so
// a verdict may not take at once; the image's grading comes back through
// SymmetricDS, and a verdict TS does not show yet is sent again (its attempt
// counter bumped, which fires the trigger again) with a backoff.
//
// A sub the stacker rejected and later stacks after all (a new reference
// score, MinScore or moon rule) is accepted again: otherwise TS images more
// than the masters need, for good. Only the stacker's own rejects are undone
// (the trigger checks the "stacker:" reason); TS's and anyone's by hand stay.
// Someone putting a rejected sub back in TS is final: the verdict is marked
// overridden and never sent again.
//
// Once a day each plan with applied rejects gets a reconcile request, which
// repairs decrements TS overwrote (its target editor saves the counts it
// loaded).

// VerdictOptions configure SendVerdicts.
type VerdictOptions struct {
	// Mode is config.TSVerdictsOff, DryRun or On.
	Mode string
	// Since, if set, limits verdicts to subs taken from then on.
	Since time.Time
	// Targets, if set, limits verdicts to these targets.
	Targets []string
	// Max is the most new verdicts one sweep sends.
	Max int
}

const (
	verdictModeDryRun = "dry-run"
	verdictModeOn     = "on"
	// verdictBackoff is the wait before a verdict not yet applied is sent
	// again, doubling up to verdictBackoffMax.
	verdictBackoff    = time.Hour
	verdictBackoffMax = 24 * time.Hour
	// reconcileEvery is how often a plan with applied rejects is reconciled.
	reconcileEvery = 24 * time.Hour
	// stackerReasonPrefix marks the stacker's rejects in TS; the trigger
	// undoes only those.
	stackerReasonPrefix = "stacker:"
)

// verdictReason is what TS shows as the reject reason for a stack status.
func verdictReason(status string) string {
	switch status {
	case app.StackStatusMoon:
		return stackerReasonPrefix + " moon"
	default:
		return stackerReasonPrefix + " sky"
	}
}

// tsImage is one of TS's acquired images, as the verdicts need it.
type tsImage struct {
	ID     int
	GUID   string
	PlanID int
	Status int
	Reason string
	Target string
	File   string // base name
	Grader bool   // its project grades images, so accepted counts
}

// verdictFrame is a light with the stacker's decision on it.
type verdictFrame struct {
	FrameID int
	Key     string
	Object  string
	Filter  string
	Status  string
}

// verdictSend is one write to stacker_verdict.
type verdictSend struct {
	Image   tsImage
	FrameID int
	Filter  string
	Verdict int
	Reason  string
	Kind    string // new, retry, undo, redo
}

// verdictPlan is what one sweep does: rows to write, and records to update.
type verdictPlan struct {
	Sends   []verdictSend
	Records []app.TSVerdict // changed or new, as they will be after the sends
	// Skipped counts frames left alone, by why.
	Skipped map[string]int
}

func backoff(attempts int) time.Duration {
	d := verdictBackoff
	for i := 1; i < attempts && d < verdictBackoffMax; i++ {
		d *= 2
	}
	return min(d, verdictBackoffMax)
}

// planVerdicts decides what to send. frames are lights with status
// low_score, moon or added; images TS's acquired images; known the verdicts
// sent before, by acquired image.
func planVerdicts(frames []verdictFrame, images []tsImage, known map[int]app.TSVerdict, now time.Time, maxNew int) verdictPlan {
	type key struct{ target, file string }
	byFile := make(map[key][]tsImage, len(images))
	for _, im := range images {
		if im.File == "" {
			continue
		}
		k := key{im.Target, im.File}
		byFile[k] = append(byFile[k], im)
	}
	out := verdictPlan{Skipped: map[string]int{}}
	news := 0
	for _, f := range frames {
		ims := byFile[key{f.Object, path.Base(f.Key)}]
		if len(ims) != 1 {
			if len(ims) == 0 {
				out.Skipped["no_image"]++
			} else {
				out.Skipped["ambiguous"]++
			}
			continue
		}
		im := ims[0]
		if !im.Grader {
			out.Skipped["no_grader"]++
			continue
		}
		want := 0
		switch f.Status {
		case app.StackStatusLowScore, app.StackStatusMoon:
			want = app.TSVerdictReject
		case app.StackStatusAdded:
			want = app.TSVerdictAccept
		}
		v, seen := known[im.ID]
		if !seen {
			// Only a reject starts a verdict, and only on an image TS counts;
			// a Pending one is waited for.
			if want != app.TSVerdictReject || im.Status != quality.GradingAccepted {
				continue
			}
			if news >= maxNew {
				out.Skipped["over_max"]++
				continue
			}
			news++
			reason := verdictReason(f.Status)
			next := now.Add(backoff(1))
			out.Sends = append(out.Sends, verdictSend{Image: im, FrameID: f.FrameID, Filter: f.Filter, Verdict: app.TSVerdictReject, Reason: reason, Kind: "new"})
			out.Records = append(out.Records, app.TSVerdict{AcquiredImageID: im.ID, FrameID: f.FrameID, ExposurePlanID: im.PlanID,
				Verdict: app.TSVerdictReject, Reason: reason, State: app.TSVerdictSent, Attempts: 1, SentAt: now, NextAttemptAt: &next})
			continue
		}
		if v.State == app.TSVerdictOverridden {
			continue
		}
		before := v
		// What TS shows now.
		ours := im.Status == quality.GradingRejected && strings.HasPrefix(im.Reason, stackerReasonPrefix)
		switch {
		case v.Verdict == app.TSVerdictReject && ours,
			v.Verdict == app.TSVerdictAccept && im.Status == quality.GradingAccepted:
			if v.State != app.TSVerdictApplied {
				v.State = app.TSVerdictApplied
				v.AppliedAt = &now
				v.NextAttemptAt = nil
			}
		case im.Status == quality.GradingRejected && !ours:
			v.State = app.TSVerdictMoot
			v.NextAttemptAt = nil
		case v.Verdict == app.TSVerdictReject && im.Status == quality.GradingAccepted && v.State == app.TSVerdictApplied:
			// It was rejected, and someone accepted it again.
			v.State = app.TSVerdictOverridden
			v.NextAttemptAt = nil
		}
		if v.State == app.TSVerdictMoot || v.State == app.TSVerdictOverridden {
			if v != before {
				out.Records = append(out.Records, v)
			}
			continue
		}
		reason := v.Reason
		if want == app.TSVerdictReject {
			reason = verdictReason(f.Status)
		}
		switch {
		case want != 0 && want != v.Verdict && im.Status != quality.GradingPending:
			// The stacker changed its mind: undo a reject, or reject again.
			kind := "undo"
			if want == app.TSVerdictReject {
				kind = "redo"
			}
			next := now.Add(backoff(1))
			v.Verdict, v.Reason, v.State = want, reason, app.TSVerdictSent
			v.Attempts++
			v.SentAt, v.NextAttemptAt, v.AppliedAt = now, &next, nil
			out.Sends = append(out.Sends, verdictSend{Image: im, FrameID: f.FrameID, Filter: f.Filter, Verdict: want, Reason: reason, Kind: kind})
		case v.State == app.TSVerdictSent && im.Status != quality.GradingPending &&
			v.NextAttemptAt != nil && !now.Before(*v.NextAttemptAt):
			// Not applied yet (TS was grading the plan): again.
			v.Attempts++
			next := now.Add(backoff(v.Attempts))
			v.SentAt, v.NextAttemptAt = now, &next
			out.Sends = append(out.Sends, verdictSend{Image: im, FrameID: f.FrameID, Filter: f.Filter, Verdict: v.Verdict, Reason: v.Reason, Kind: "retry"})
		}
		if v != before {
			out.Records = append(out.Records, v)
		}
	}
	return out
}

// verdictFrames loads the lights the verdicts are about: left out for low
// score or moon, or stacked after all when a verdict was sent for them.
func (p *Pipeline) verdictFrames(ctx context.Context, opts VerdictOptions) ([]verdictFrame, error) {
	q := p.db.WithContext(ctx).Table("stack_frames sf").
		Select("sf.frame_id, f.key, f.object, f.filter, sf.status").
		Joins("JOIN frames f ON f.id = sf.frame_id").
		Where("sf.status IN ? OR (sf.status = ? AND EXISTS (SELECT 1 FROM ts_verdicts v WHERE v.frame_id = sf.frame_id))",
			[]string{app.StackStatusLowScore, app.StackStatusMoon}, app.StackStatusAdded)
	if !opts.Since.IsZero() {
		q = q.Where("f.date_obs >= ?", opts.Since)
	}
	if len(opts.Targets) > 0 {
		q = q.Where("f.object IN ?", opts.Targets)
	}
	var frames []verdictFrame
	if err := q.Order("sf.frame_id").Scan(&frames).Error; err != nil {
		return nil, fmt.Errorf("load lights for verdicts: %w", err)
	}
	return frames, nil
}

// tsImages reads TS's acquired images. The file names come from the
// metadata, parsed once per image and kept in p.verdictFiles.
func (p *Pipeline) tsImages(ctx context.Context) ([]tsImage, error) {
	db := p.sched.WithContext(ctx)
	rows, err := db.Table("acquiredimage a").
		Select(`a."Id", coalesce(a.guid, ''), coalesce(a."exposureId", 0), a."gradingStatus", coalesce(a.rejectreason, ''), ` +
			`coalesce(t.name, ''), coalesce(pr.enablegrader, 0)`).
		Joins(`LEFT JOIN target t ON t."Id" = a."targetId"`).
		Joins(`LEFT JOIN project pr ON pr."Id" = a."projectId"`).
		Order(`a."Id"`).Rows()
	if err != nil {
		return nil, fmt.Errorf("load acquired images: %w", err)
	}
	var images []tsImage
	for rows.Next() {
		var im tsImage
		var grader int
		if err := rows.Scan(&im.ID, &im.GUID, &im.PlanID, &im.Status, &im.Reason, &im.Target, &grader); err != nil {
			rows.Close()
			return nil, fmt.Errorf("load acquired images: %w", err)
		}
		im.Grader = grader == 1
		images = append(images, im)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load acquired images: %w", err)
	}

	p.verdictMu.Lock()
	defer p.verdictMu.Unlock()
	if p.verdictFiles == nil {
		p.verdictFiles = map[int]string{}
	}
	var missing []int
	for _, im := range images {
		if _, ok := p.verdictFiles[im.ID]; !ok {
			missing = append(missing, im.ID)
		}
	}
	for i := 0; i < len(missing); i += metadataChunk {
		ids := missing[i:min(i+metadataChunk, len(missing))]
		var metas []struct {
			ID       int
			Metadata string
		}
		if err := db.Table("acquiredimage").Select(`"Id" as id, metadata`).Where(`"Id" IN ?`, ids).Scan(&metas).Error; err != nil {
			return nil, fmt.Errorf("load acquired image metadata: %w", err)
		}
		for _, m := range metas {
			// A file name never changes; one that can't be read is kept as
			// "" and never matches.
			md, err := quality.ParseMetadata(m.Metadata)
			file := ""
			if err == nil {
				file = md.FileName[strings.LastIndexAny(md.FileName, `\/`)+1:]
			}
			p.verdictFiles[m.ID] = file
		}
	}
	for i := range images {
		images[i].File = p.verdictFiles[images[i].ID]
	}
	return images, nil
}

// metadataChunk bounds how many ids go in one IN list.
const metadataChunk = 1000

// SendVerdicts runs one sweep: it sends new verdicts, sends again those not
// applied, undoes rejects of subs since stacked, and asks for reconciles. In
// dry-run mode it only logs what it would send. It returns the plan.
func (p *Pipeline) SendVerdicts(ctx context.Context, opts VerdictOptions) (verdictPlan, error) {
	var plan verdictPlan
	if opts.Mode != verdictModeOn && opts.Mode != verdictModeDryRun {
		return plan, nil
	}
	frames, err := p.verdictFrames(ctx, opts)
	if err != nil {
		return plan, err
	}
	images, err := p.tsImages(ctx)
	if err != nil {
		return plan, err
	}
	var records []app.TSVerdict
	if err := p.db.WithContext(ctx).Find(&records).Error; err != nil {
		return plan, fmt.Errorf("load verdicts: %w", err)
	}
	known := make(map[int]app.TSVerdict, len(records))
	for _, r := range records {
		known[r.AcquiredImageID] = r
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	maxNew := opts.Max
	if maxNew <= 0 {
		maxNew = 200
	}
	plan = planVerdicts(frames, images, known, now, maxNew)
	reportVerdicts(plan, opts.Mode)

	if opts.Mode == verdictModeDryRun {
		return plan, nil
	}
	sched := p.sched.WithContext(ctx)
	// The scheduler database first: if the stacker stops in between, the
	// next sweep sends the verdict again, which is harmless.
	for _, s := range plan.Sends {
		row := map[string]any{
			"acquiredimage_id": s.Image.ID,
			"guid":             s.Image.GUID,
			"exposureplan_id":  s.Image.PlanID,
			"verdict":          s.Verdict,
			"reason":           s.Reason,
			"attempt":          0,
			"created_at":       now,
			"updated_at":       now,
		}
		if err := sched.Table("stacker_verdict").Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "acquiredimage_id"}},
			DoUpdates: clause.Assignments(map[string]any{
				"guid":            s.Image.GUID,
				"exposureplan_id": s.Image.PlanID,
				"verdict":         s.Verdict,
				"reason":          s.Reason,
				// A new value fires the observatory's trigger again.
				"attempt":    gorm.Expr("stacker_verdict.attempt + 1"),
				"updated_at": now,
			}),
		}).Create(row).Error; err != nil {
			return plan, fmt.Errorf("send verdict on acquired image %d: %w", s.Image.ID, err)
		}
		metrics.TSVerdictsSent.WithLabelValues(s.Kind).Inc()
	}
	for _, r := range plan.Records {
		if err := p.db.WithContext(ctx).Save(&r).Error; err != nil {
			return plan, fmt.Errorf("record verdict on acquired image %d: %w", r.AcquiredImageID, err)
		}
	}
	if err := p.requestReconciles(ctx, now); err != nil {
		return plan, err
	}
	return plan, p.countVerdicts(ctx)
}

// requestReconciles asks for each plan with applied rejects to be reconciled,
// at most once every reconcileEvery.
func (p *Pipeline) requestReconciles(ctx context.Context, now time.Time) error {
	var plans []int
	if err := p.db.WithContext(ctx).Model(&app.TSVerdict{}).
		Where("verdict = ? AND state = ?", app.TSVerdictReject, app.TSVerdictApplied).
		Distinct("exposure_plan_id").Pluck("exposure_plan_id", &plans).Error; err != nil {
		return fmt.Errorf("load plans to reconcile: %w", err)
	}
	if len(plans) == 0 {
		return nil
	}
	sched := p.sched.WithContext(ctx)
	var recent []int
	if err := sched.Table("stacker_reconcile").Where("updated_at > ?", now.Add(-reconcileEvery)).
		Pluck("exposureplan_id", &recent).Error; err != nil {
		return fmt.Errorf("load reconciles: %w", err)
	}
	skip := make(map[int]bool, len(recent))
	for _, id := range recent {
		skip[id] = true
	}
	n := 0
	for _, id := range plans {
		if skip[id] {
			continue
		}
		if err := sched.Table("stacker_reconcile").Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "exposureplan_id"}},
			DoUpdates: clause.Assignments(map[string]any{
				"attempt":    gorm.Expr("stacker_reconcile.attempt + 1"),
				"updated_at": now,
			}),
		}).Create(map[string]any{"exposureplan_id": id, "attempt": 0, "updated_at": now}).Error; err != nil {
			return fmt.Errorf("request reconcile of plan %d: %w", id, err)
		}
		n++
	}
	if n > 0 {
		slog.Info("Asked Target Scheduler to reconcile plans", "plans", n)
	}
	return nil
}

// countVerdicts sets the verdict gauges from the records.
func (p *Pipeline) countVerdicts(ctx context.Context) error {
	var counts []struct {
		Verdict int
		State   string
		N       int
	}
	if err := p.db.WithContext(ctx).Model(&app.TSVerdict{}).Select("verdict, state, count(*) AS n").
		Group("verdict, state").Scan(&counts).Error; err != nil {
		return err
	}
	metrics.TSVerdicts.Reset()
	for _, c := range counts {
		v := "reject"
		if c.Verdict == app.TSVerdictAccept {
			v = "accept"
		}
		metrics.TSVerdicts.WithLabelValues(v, c.State).Set(float64(c.N))
	}
	return nil
}

// reportVerdicts logs what a sweep sends, by target and filter.
func reportVerdicts(plan verdictPlan, mode string) {
	type key struct{ target, filter, kind string }
	counts := map[key]int{}
	for _, s := range plan.Sends {
		counts[key{s.Image.Target, s.Filter, s.Kind}]++
	}
	keys := make([]key, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.target != b.target {
			return a.target < b.target
		}
		if a.filter != b.filter {
			return a.filter < b.filter
		}
		return a.kind < b.kind
	})
	msg := "Sending verdicts to Target Scheduler"
	if mode == verdictModeDryRun {
		msg = "Would send verdicts to Target Scheduler (dry run)"
	}
	for _, k := range keys {
		slog.Info(msg, "target", k.target, "filter", k.filter, "kind", k.kind, "subs", counts[k])
	}
	if len(plan.Sends) > 0 || len(plan.Skipped) > 0 {
		args := []any{"sends", len(plan.Sends)}
		for why, n := range plan.Skipped {
			args = append(args, "skipped_"+why, n)
		}
		slog.Info(msg+": summary", args...)
	}
}

// runVerdicts sends verdicts every hour until the pipeline stops.
func (p *Pipeline) runVerdicts(ctx context.Context, opts VerdictOptions) {
	for !p.stopping(ctx) {
		if _, err := p.SendVerdicts(ctx, opts); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("Sending verdicts to Target Scheduler failed", "error", err)
		}
		if !p.pause(ctx, time.Hour) {
			return
		}
	}
}
