package discoveries

import (
	"bytes"
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
	"os"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/bodies"
	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/trigger"
	"github.com/klauspost/compress/zstd"
	"github.com/minio/minio-go/v7"
	"gorm.io/gorm"
)

// Method is the revision of the method; runs by an older one are redone.
const Method = 1

const (
	// GroupBroadband stacks a night's L, R, G and B subs together; each
	// narrowband filter is its own group.
	GroupBroadband = "broadband"
	// MinNights is how many nights with accepted subs a target's group
	// needs: the night and at least two to compare it with.
	MinNights      = 3
	keyPrefix      = "discoveries/"
	failureBackoff = 6 * time.Hour
	maxAttempts    = 3
	settleAfter    = 2 * time.Hour
	settleStacked  = 20 * time.Minute
	idlePoll       = 15 * time.Second
	vsxMaxAge      = 30 * 24 * time.Hour
	kickQuiet      = 2 * time.Minute
	kickMaxWait    = 10 * time.Minute
	stageEvery     = 20 * time.Second
	EventType      = "discovery"
	maxRequested   = 500
)

func broadband() []string { return []string{"Luminance", "Red", "Green", "Blue"} }

// GroupOf is the filter group a filter is compared in.
func GroupOf(filter string) string {
	if slices.Contains(broadband(), filter) {
		return GroupBroadband
	}
	return filter
}

// ObjectStore reads and writes the processed bucket.
type ObjectStore interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Put(ctx context.Context, key string, data []byte, contentType string) error
	RemovePrefix(ctx context.Context, prefix string) error
}

// MinioStore is an ObjectStore on a MinIO bucket.
type MinioStore struct {
	Client *minio.Client
	Bucket string
}

func (m MinioStore) Get(ctx context.Context, key string) ([]byte, error) {
	o, err := m.Client.GetObject(ctx, m.Bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer o.Close()
	return io.ReadAll(o)
}

func (m MinioStore) Put(ctx context.Context, key string, data []byte, contentType string) error {
	_, err := m.Client.PutObject(ctx, m.Bucket, key, bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{ContentType: contentType})
	return err
}

func (m MinioStore) RemovePrefix(ctx context.Context, prefix string) error {
	for o := range m.Client.ListObjects(ctx, m.Bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
		if o.Err != nil {
			return o.Err
		}
		if err := m.Client.RemoveObject(ctx, m.Bucket, o.Key, minio.RemoveObjectOptions{}); err != nil {
			return err
		}
	}
	return nil
}

// IsNotFound says whether a store error is a missing key.
func IsNotFound(err error) bool {
	var me minio.ErrorResponse
	return errors.As(err, &me) && (me.Code == "NoSuchKey" || me.StatusCode == http.StatusNotFound)
}

// Options tune the runner.
type Options struct {
	// Interval is the backstop between passes; a master update starts one
	// about two minutes later.
	Interval time.Duration
	// Threads is how many cores one comparison may use.
	Threads int
	// RefSubs caps the reference subs per filter.
	RefSubs int
	// BackfillDays is how far back nights are compared on their own.
	BackfillDays int
	// Busy pauses the work while the stacker is stacking.
	Busy func() bool
	// MaxWait and Headroom let the work go on beside a stacker that has been
	// busy for MaxWait, while Headroom says there is memory to spare.
	MaxWait  time.Duration
	Headroom func() bool
	// Catalog finds galaxies for supernova candidates.
	Catalog func() (*catalog.Index, error)
	// OnRun, if set, hears of each finished run.
	OnRun func(app.DiscoveryRun)
}

// Live is what the runner is doing.
type Live struct {
	State   string     `json:"state"`
	Object  string     `json:"object,omitempty"`
	Night   string     `json:"night,omitempty"`
	Group   string     `json:"group,omitempty"`
	Stage   string     `json:"stage,omitempty"`
	Started *time.Time `json:"started,omitempty"`
	Queued  int        `json:"queued"`
	Done    int        `json:"done_in_pass"`
}

// Runner compares each eligible target's nights with their reference as
// soon as a night's subs are stacked, one at a time in the background.
type Runner struct {
	db      *gorm.DB
	sched   *gorm.DB
	store   ObjectStore
	cat     *Catalogs
	opts    Options
	now     func() time.Time
	kick    *trigger.Debouncer
	drain   chan struct{}
	drained sync.Once

	mu        sync.Mutex
	live      Live
	anchors   map[string][]Anchor
	lastStage time.Time
	busySince time.Time
}

// Job is one night of one target's filter group.
type Job struct {
	Object    string
	Group     string
	Night     time.Time
	Requested bool
}

func (j Job) key() string {
	return j.Object + "\x00" + j.Group + "\x00" + j.Night.Format(time.DateOnly)
}

func New(db, sched *gorm.DB, store ObjectStore, cat *Catalogs, opts Options) *Runner {
	if opts.Interval <= 0 {
		opts.Interval = 10 * time.Minute
	}
	if opts.Threads <= 0 {
		opts.Threads = 2
	}
	if opts.RefSubs <= 0 {
		opts.RefSubs = 48
	}
	if opts.BackfillDays <= 0 {
		opts.BackfillDays = 30
	}
	return &Runner{db: db, sched: sched, store: store, cat: cat, opts: opts, now: func() time.Time { return time.Now().UTC() },
		kick: &trigger.Debouncer{Quiet: kickQuiet, MaxWait: kickMaxWait}, drain: make(chan struct{}), anchors: map[string][]Anchor{},
		live: Live{State: "idle"}}
}

// Live returns what the runner is doing.
func (r *Runner) Live() Live {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.live
}

// Kick asks for a pass soon, as when a master was updated.
func (r *Runner) Kick() { r.kick.Kick() }

// Request queues one night of a target to be compared (again), outside the
// backfill window if need be.
// The request is stored as a queued run, so a restart keeps it; a request
// with no group stands for each of the target's groups.
func (r *Runner) Request(object string, night time.Time, group string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	groups := []string{group}
	if group == "" {
		rows, err := r.subRows(ctx, object)
		if err != nil {
			slog.Warn("Could not queue a discovery request", "object", object, "error", err)
			return
		}
		groups = groups[:0]
		for g := range byGroup(rows)[object] {
			groups = append(groups, g)
		}
	}
	night = dateOnly(night)
	for _, g := range groups {
		var found []app.DiscoveryRun
		if err := r.db.WithContext(ctx).Where("object = ? AND night >= ? AND night < ? AND filter_group = ?", object, night, night.AddDate(0, 0, 1), g).
			Limit(1).Find(&found).Error; err != nil {
			slog.Warn("Could not queue a discovery request", "object", object, "error", err)
			return
		}
		run := app.DiscoveryRun{Object: object, Night: night, Group: g}
		if len(found) > 0 {
			run = found[0]
		}
		run.State, run.Requested, run.Attempts, run.NextTry = app.DiscoveryQueued, true, 0, nil
		if err := r.db.WithContext(ctx).Save(&run).Error; err != nil {
			slog.Warn("Could not queue a discovery request", "object", object, "error", err)
			return
		}
	}
	r.kick.Kick()
}

// Drain stops the runner after the current comparison's next step.
func (r *Runner) Drain() { r.drained.Do(func() { close(r.drain) }) }

func (r *Runner) stopping(ctx context.Context) bool {
	if ctx.Err() != nil {
		return true
	}
	select {
	case <-r.drain:
		return true
	default:
		return false
	}
}

// waitIdle waits while the stacker is working. Once it has been working
// for MaxWait without a break, the comparison goes on beside it as long as
// the container has Headroom of memory to spare, so a long stacking
// backlog does not hold the comparisons back for days.
func (r *Runner) waitIdle(ctx context.Context) error {
	if r.opts.Busy == nil || !r.opts.Busy() {
		r.mu.Lock()
		r.busySince = time.Time{}
		r.mu.Unlock()
		return nil
	}
	r.mu.Lock()
	if r.busySince.IsZero() {
		r.busySince = time.Now()
	}
	since := r.busySince
	r.mu.Unlock()
	r.setStage("waiting for the stacker")
	for r.opts.Busy() {
		if r.opts.MaxWait > 0 && time.Since(since) >= r.opts.MaxWait && r.opts.Headroom != nil && r.opts.Headroom() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-r.drain:
			return context.Canceled
		case <-time.After(idlePoll):
		}
	}
	r.mu.Lock()
	r.busySince = time.Time{}
	r.mu.Unlock()
	return nil
}

// CgroupHeadroom reports whether the container's anonymous memory plus
// need stays under 80% of its limit, from the cgroup v2 files; page cache
// is left out, as the kernel drops it under pressure. Without a limit it is
// true; when the files can't be read it is false.
func CgroupHeadroom(need int64) func() bool {
	return func() bool {
		b, err := os.ReadFile("/sys/fs/cgroup/memory.max")
		if err != nil {
			return false
		}
		limitText := strings.TrimSpace(string(b))
		if limitText == "max" {
			return true
		}
		limit, err := strconv.ParseInt(limitText, 10, 64)
		if err != nil {
			return false
		}
		stat, err := os.ReadFile("/sys/fs/cgroup/memory.stat")
		if err != nil {
			return false
		}
		anon, ok := statValue(string(stat), "anon")
		return ok && float64(anon+need) < 0.8*float64(limit)
	}
}

func statValue(stat, key string) (int64, bool) {
	for line := range strings.SplitSeq(stat, "\n") {
		if name, val, found := strings.Cut(line, " "); found && name == key {
			v, err := strconv.ParseInt(strings.TrimSpace(val), 10, 64)
			return v, err == nil
		}
	}
	return 0, false
}

func (r *Runner) setStage(s string) {
	r.mu.Lock()
	r.live.Stage = s
	r.mu.Unlock()
}

// Run passes over the due nights at start, soon after each Kick, and every
// Interval, until ctx ends or Drain is called.
func (r *Runner) Run(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-r.drain:
			cancel()
		case <-ctx.Done():
		}
	}()
	r.kick.Run(ctx, r.opts.Interval, func(ctx context.Context, _ []string) {
		if err := r.Pass(ctx); err != nil && ctx.Err() == nil {
			slog.Error("Discovery pass failed", "error", err)
		}
	})
}

// Pass compares every due night, requested ones first, then newest first.
func (r *Runner) Pass(ctx context.Context) error {
	jobs, err := r.due(ctx)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.live.Queued, r.live.Done = len(jobs), 0
	r.mu.Unlock()
	if len(jobs) > 0 {
		slog.Info("Discovery pass", "due", len(jobs))
	}
	done := map[string]bool{}
	run := func(j Job) error {
		if done[j.key()] {
			return nil
		}
		done[j.key()] = true
		if r.stopping(ctx) {
			return ctx.Err()
		}
		if err := r.waitIdle(ctx); err != nil {
			return err
		}
		r.runOne(ctx, j)
		r.mu.Lock()
		r.live.Queued--
		r.live.Done++
		r.mu.Unlock()
		return nil
	}
	for _, j := range jobs {
		if err := run(j); err != nil {
			return err
		}
		more, err := r.takeRequested(ctx)
		if err != nil {
			continue
		}
		for _, m := range more {
			if !done[m.key()] {
				r.mu.Lock()
				r.live.Queued++
				r.mu.Unlock()
			}
			if err := run(m); err != nil {
				return err
			}
		}
	}
	return nil
}

type subRow struct {
	Object   string
	Filter   string
	Night    time.Time
	FrameID  int
	Key      string
	Weight   float64
	DateObs  time.Time
	Exposure float64
}

func (r *Runner) subRows(ctx context.Context, object string) ([]subRow, error) {
	q := r.db.WithContext(ctx).Table("stack_frames sf").
		Select("s.object, s.filter, f.night, f.id AS frame_id, sf.registered_key AS key, sf.weight, f.date_obs, f.exposure").
		Joins("JOIN frames f ON f.id = sf.frame_id").
		Joins("JOIN stacks s ON s.id = sf.stack_id").
		Where("sf.status = ? AND sf.registered_key IS NOT NULL AND sf.weight > 0 AND f.night IS NOT NULL AND f.date_obs IS NOT NULL AND f.exposure > 0", app.StackStatusAdded)
	if object != "" {
		q = q.Where("s.object = ?", object)
	}
	var rows []subRow
	if err := q.Order("f.date_obs").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load subs: %w", err)
	}
	for i := range rows {
		rows[i].Night = dateOnly(rows[i].Night)
	}
	return rows, nil
}

func dateOnly(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

type groupNights map[time.Time][]subRow

// byGroup arranges subs by object, filter group and night.
func byGroup(rows []subRow) map[string]map[string]groupNights {
	out := map[string]map[string]groupNights{}
	for _, row := range rows {
		g := GroupOf(row.Filter)
		if out[row.Object] == nil {
			out[row.Object] = map[string]groupNights{}
		}
		if out[row.Object][g] == nil {
			out[row.Object][g] = groupNights{}
		}
		out[row.Object][g][row.Night] = append(out[row.Object][g][row.Night], row)
	}
	return out
}

func subsHash(rows []subRow) string {
	ids := make([]string, len(rows))
	for i, s := range rows {
		ids[i] = fmt.Sprintf("%d", s.FrameID)
	}
	sort.Strings(ids)
	h := sha256.Sum256([]byte(strings.Join(ids, ",")))
	return hex.EncodeToString(h[:8])
}

type settleRow struct {
	Object    string
	Night     time.Time
	DateObs   *time.Time
	StackID   *int
	Processed *time.Time
}

// settled says which target nights are done: imaging ended two hours ago,
// every light has a stacking verdict and none came in the last 20 minutes.
func (r *Runner) settled(ctx context.Context, since time.Time) (map[string]bool, error) {
	var rows []settleRow
	if err := r.db.WithContext(ctx).Table("frames f").
		Select("f.object, f.night, f.date_obs, sf.id AS stack_id, sf.processed_at AS processed").
		Joins("LEFT JOIN stack_frames sf ON sf.frame_id = f.id").
		Where("f.type = ? AND f.night >= ? AND f.object IS NOT NULL", "LIGHT", since).
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("check settled nights: %w", err)
	}
	now := r.now()
	out := map[string]bool{}
	for _, s := range rows {
		k := s.Object + "\x00" + dateOnly(s.Night).Format(time.DateOnly)
		if _, seen := out[k]; !seen {
			out[k] = true
		}
		switch {
		case s.StackID == nil:
			out[k] = false
		case s.DateObs != nil && now.Sub(*s.DateObs) < settleAfter:
			out[k] = false
		case s.Processed != nil && now.Sub(*s.Processed) < settleStacked:
			out[k] = false
		}
	}
	return out, nil
}

// takeRequested returns the requested runs still queued, oldest first.
func (r *Runner) takeRequested(ctx context.Context) ([]Job, error) {
	var runs []app.DiscoveryRun
	if err := r.db.WithContext(ctx).Where("requested = ? AND state = ?", true, app.DiscoveryQueued).Order("updated_at").Limit(maxRequested).Find(&runs).Error; err != nil {
		return nil, err
	}
	out := make([]Job, len(runs))
	for i, run := range runs {
		out[i] = Job{Object: run.Object, Group: run.Group, Night: dateOnly(run.Night), Requested: true}
	}
	return out, nil
}

// Cutout reads one of a candidate's cutouts.
func (r *Runner) Cutout(ctx context.Context, key string) ([]byte, error) {
	if !strings.HasPrefix(key, keyPrefix) {
		return nil, errors.New("not a discovery cutout")
	}
	return r.store.Get(ctx, key)
}

func (r *Runner) due(ctx context.Context) ([]Job, error) {
	rows, err := r.subRows(ctx, "")
	if err != nil {
		return nil, err
	}
	groups := byGroup(rows)
	var stored map[string]bodies.Target
	if r.sched != nil {
		if stored, err = bodies.TargetsByName(ctx, r.sched); err != nil {
			slog.Warn("Could not load the moving-body targets for discoveries", "error", err)
		}
	}
	var runs []app.DiscoveryRun
	if err := r.db.WithContext(ctx).Select("id, object, night, filter_group, method, subs_hash, state, attempts, next_try").Find(&runs).Error; err != nil {
		return nil, fmt.Errorf("load discovery runs: %w", err)
	}
	have := map[string]app.DiscoveryRun{}
	for _, run := range runs {
		have[Job{Object: run.Object, Group: run.Group, Night: dateOnly(run.Night)}.key()] = run
	}
	since := dateOnly(r.now()).AddDate(0, 0, -r.opts.BackfillDays)
	settled, err := r.settled(ctx, since)
	if err != nil {
		return nil, err
	}
	now := r.now()
	var jobs []Job
	for object, gs := range groups {
		if _, ok := bodies.OwnBody(stored, object); ok {
			continue
		}
		for g, nights := range gs {
			if len(nights) < MinNights {
				continue
			}
			for night, subs := range nights {
				if night.Before(since) || !settled[object+"\x00"+night.Format(time.DateOnly)] {
					continue
				}
				j := Job{Object: object, Group: g, Night: night}
				if run, ok := have[j.key()]; ok {
					want := subsHash(subs)
					if run.State == app.DiscoverySkipped {
						want = skippedHash(want, len(nights))
					}
					if run.Method == Method && run.SubsHash == want && settledRun(run, now) {
						continue
					}
				}
				jobs = append(jobs, j)
			}
		}
	}
	sort.Slice(jobs, func(a, b int) bool {
		if !jobs[a].Night.Equal(jobs[b].Night) {
			return jobs[a].Night.After(jobs[b].Night)
		}
		return jobs[a].key() < jobs[b].key()
	})
	req, err := r.takeRequested(ctx)
	if err != nil {
		return nil, err
	}
	asked := map[string]bool{}
	for _, j := range req {
		asked[j.key()] = true
	}
	return append(req, slices.DeleteFunc(jobs, func(j Job) bool { return asked[j.key()] })...), nil
}

func (r *Runner) runOne(ctx context.Context, j Job) {
	start := r.now()
	r.mu.Lock()
	r.live = Live{State: "running", Object: j.Object, Night: j.Night.Format(time.DateOnly), Group: j.Group, Started: &start, Queued: r.live.Queued, Done: r.live.Done}
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.live.State, r.live.Object, r.live.Night, r.live.Group, r.live.Stage, r.live.Started = "idle", "", "", "", "", nil
		r.mu.Unlock()
	}()
	run, err := r.startRun(ctx, j)
	if errors.Is(err, errClaimed) {
		slog.Info("Discovery run is running elsewhere", "object", j.Object, "night", j.Night.Format(time.DateOnly), "group", j.Group)
		return
	}
	if err != nil {
		slog.Error("Could not start a discovery run", "object", j.Object, "night", j.Night.Format(time.DateOnly), "group", j.Group, "error", err)
		return
	}
	beat, stopBeat := context.WithCancel(ctx)
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-beat.Done():
				return
			case <-t.C:
				r.db.Model(&app.DiscoveryRun{}).Where("id = ?", run.ID).Update("updated_at", time.Now())
			}
		}
	}()
	err = r.compare(ctx, j, run)
	stopBeat()
	if err != nil && (ctx.Err() != nil || r.stopping(ctx)) {
		r.db.Model(run).Updates(map[string]any{"state": app.DiscoveryQueued, "stage": ""})
		return
	}
	if err != nil {
		state := app.DiscoveryFailed
		if errors.Is(err, ErrTooLittle) || errors.Is(err, errNotEligible) {
			state = app.DiscoverySkipped
		}
		msg := err.Error()
		next := r.now().Add(failureBackoff)
		if state == app.DiscoverySkipped {
			run.SubsHash = skippedHash(run.SubsHash, run.RefNights+1)
		}
		r.db.Model(run).Updates(map[string]any{"state": state, "error": msg, "stage": "", "attempts": run.Attempts + 1, "next_try": next,
			"subs_hash": run.SubsHash, "method": Method, "finished_at": r.now(), "seconds": r.now().Sub(start).Seconds()})
		if state == app.DiscoveryFailed {
			slog.Warn("Discovery run failed", "object", j.Object, "night", j.Night.Format(time.DateOnly), "group", j.Group, "error", err)
		} else {
			slog.Info("Discovery run skipped", "object", j.Object, "night", j.Night.Format(time.DateOnly), "group", j.Group, "reason", err)
		}
	}
}

// skippedHash marks a skipped run's subs with the group's night count, so
// the night is tried again once more nights could make its reference.
func skippedHash(hash string, nights int) string {
	return fmt.Sprintf("%s:n%d", hash, nights)
}

// settledRun says whether a run needs nothing more: done, skipped, or
// failed and waiting out its backoff or out of attempts. A run left running
// or queued by a restart is due again.
func settledRun(run app.DiscoveryRun, now time.Time) bool {
	switch run.State {
	case app.DiscoveryDone, app.DiscoverySkipped:
		return true
	case app.DiscoveryFailed:
		return run.Attempts >= maxAttempts || (run.NextTry != nil && run.NextTry.After(now))
	}
	return false
}

// errClaimed is a run another runner, such as a backfill job, is working
// on: it updates the run at least every stageEvery, so one not updated for
// claimFor is taken to have died.
var errClaimed = errors.New("running elsewhere")

const claimFor = 10 * time.Minute

var errNotEligible = errors.New("fewer than 3 nights with accepted subs in this filter group")

func (r *Runner) startRun(ctx context.Context, j Job) (*app.DiscoveryRun, error) {
	var found []app.DiscoveryRun
	if err := r.db.WithContext(ctx).Where("object = ? AND night >= ? AND night < ? AND filter_group = ?", j.Object, j.Night, j.Night.AddDate(0, 0, 1), j.Group).
		Limit(1).Find(&found).Error; err != nil {
		return nil, err
	}
	run := app.DiscoveryRun{Object: j.Object, Night: j.Night, Group: j.Group}
	if len(found) > 0 {
		run = found[0]
		if run.State == app.DiscoveryRunning && time.Since(run.UpdatedAt) < claimFor {
			return nil, errClaimed
		}
	}
	now := r.now()
	run.State, run.Stage, run.Method, run.StartedAt, run.Error = app.DiscoveryRunning, "starting", Method, &now, nil
	run.Requested = run.Requested || j.Requested
	if err := r.db.WithContext(ctx).Save(&run).Error; err != nil {
		return nil, err
	}
	return &run, nil
}

func (r *Runner) progress(run *app.DiscoveryRun) func(string) {
	return func(s string) {
		r.setStage(s)
		r.mu.Lock()
		due := time.Since(r.lastStage) >= stageEvery
		if due {
			r.lastStage = time.Now()
		}
		r.mu.Unlock()
		if due {
			r.db.Model(run).Update("stage", s)
		}
	}
}

// compare runs one job and saves what it found.
func (r *Runner) compare(ctx context.Context, j Job, run *app.DiscoveryRun) error {
	start := r.now()
	rows, err := r.subRows(ctx, j.Object)
	if err != nil {
		return err
	}
	nights := byGroup(rows)[j.Object][j.Group]
	if len(nights) < MinNights {
		return errNotEligible
	}
	tonight := nights[j.Night]
	run.SubsHash, run.RefNights = subsHash(tonight), len(nights)-1
	exposure := map[int]float64{}
	for _, subs := range nights {
		for _, s := range subs {
			exposure[s.FrameID] = s.Exposure
		}
	}
	in, err := r.input(ctx, j, nights)
	if err != nil {
		return err
	}
	in.Progress = r.progress(run)
	res, err := Run(ctx, *in)
	if err != nil {
		return err
	}
	r.setStage("checking the catalogues")
	known, err := r.known(ctx, j.Object, res)
	if err != nil {
		return err
	}
	labelled, tracks, checked, failed := Classify(ctx, res, known, r.cat.AlertsFrom())
	r.setStage("saving")
	return r.save(ctx, run, saveInput{res: res, wcs: in.WCS, bodies: known.Bodies, labelled: labelled, tracks: tracks,
		checked: checked, failed: failed, exposure: exposure, start: start})
}

// input gathers a job's subs, anchors, plate solution and Gaia stars.
func (r *Runner) input(ctx context.Context, j Job, nights groupNights) (*Input, error) {
	var tr app.TargetReference
	if err := r.db.WithContext(ctx).Where("object = ?", j.Object).First(&tr).Error; err != nil {
		return nil, fmt.Errorf("no registration reference: %w", err)
	}
	if tr.WCS == nil {
		return nil, errors.New("the registration reference has no plate solution")
	}
	var cards []frameheader.Card
	if err := json.Unmarshal([]byte(*tr.WCS), &cards); err != nil {
		return nil, fmt.Errorf("read the plate solution: %w", err)
	}
	kw := map[string]string{}
	for _, c := range cards {
		if _, dup := kw[c.Name]; !dup {
			kw[c.Name] = c.Value
		}
	}
	var stacks []app.Stack
	if err := r.db.WithContext(ctx).Where("object = ? AND master_key IS NOT NULL", j.Object).Find(&stacks).Error; err != nil {
		return nil, err
	}
	in := &Input{Threads: r.opts.Threads, Load: r.loadSub}
	byFilter := map[string][]subRow{}
	refBy := map[string]map[time.Time][]subRow{}
	for night, subs := range nights {
		for _, s := range subs {
			if night.Equal(j.Night) {
				byFilter[s.Filter] = append(byFilter[s.Filter], s)
				continue
			}
			if refBy[s.Filter] == nil {
				refBy[s.Filter] = map[time.Time][]subRow{}
			}
			refBy[s.Filter][night] = append(refBy[s.Filter][night], s)
		}
	}
	for _, st := range stacks {
		if GroupOf(st.Filter) != j.Group || len(byFilter[st.Filter]) == 0 {
			continue
		}
		if in.W == 0 {
			in.W, in.H = st.Width, st.Height
		}
		if st.Width != in.W || st.Height != in.H {
			continue
		}
		anchors, err := r.anchorsOf(ctx, st)
		if err != nil {
			return nil, err
		}
		f := FilterInput{Filter: st.Filter, Anchors: anchors, Night: toSubs(byFilter[st.Filter]), Ref: toSubs(pickRef(refBy[st.Filter], r.opts.RefSubs))}
		in.Filters = append(in.Filters, f)
	}
	if len(in.Filters) == 0 {
		return nil, ErrTooLittle
	}
	sort.Slice(in.Filters, func(a, b int) bool { return filterRank(in.Filters[a].Filter) < filterRank(in.Filters[b].Filter) })
	w, err := ParseWCS(kw, in.H)
	if err != nil {
		return nil, err
	}
	in.WCS = w
	r.setStage("loading Gaia stars")
	ra, dec := w.SkyOf(float64(in.W)/2, float64(in.H)/2)
	radius := math.Hypot(float64(in.W), float64(in.H)) / 2 * w.PixelScale() / 3600 * 1.05
	gaia, err := r.gaia(ctx, j.Object, ra, dec, radius)
	if err != nil {
		return nil, err
	}
	in.Gaia = gaia
	return in, nil
}

func filterRank(f string) int {
	if f == GroupBroadband {
		return -1
	}
	bb := broadband()
	if i := slices.Index(bb, f); i >= 0 {
		return i
	}
	return len(bb)
}

func toSubs(rows []subRow) []Sub {
	out := make([]Sub, len(rows))
	for i, s := range rows {
		out[i] = Sub{FrameID: s.FrameID, Filter: s.Filter, Night: s.Night, Start: s.DateObs.UTC(), Exposure: s.Exposure, Weight: s.Weight, Key: s.Key}
	}
	return out
}

// pickRef takes up to n reference subs, the best of each night in turn so
// no night dominates.
func pickRef(nights map[time.Time][]subRow, n int) []subRow {
	keys := make([]time.Time, 0, len(nights))
	for k := range nights {
		keys = append(keys, k)
		subs := nights[k]
		sort.SliceStable(subs, func(a, b int) bool { return subs[a].Weight > subs[b].Weight })
	}
	sort.Slice(keys, func(a, b int) bool { return keys[a].Before(keys[b]) })
	var out []subRow
	for i := 0; len(out) < n; i++ {
		added := false
		for _, k := range keys {
			if i < len(nights[k]) && len(out) < n {
				out = append(out, nights[k][i])
				added = true
			}
		}
		if !added {
			break
		}
	}
	return out
}

func (r *Runner) loadSub(ctx context.Context, s Sub) (*Plane, error) {
	if err := r.waitIdle(ctx); err != nil {
		return nil, err
	}
	if r.stopping(ctx) {
		return nil, context.Canceled
	}
	b, err := r.store.Get(ctx, s.Key)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", s.Key, err)
	}
	im, err := imagedata.Decode(b)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", s.Key, err)
	}
	return PlaneOf(im), nil
}

func (r *Runner) anchorsOf(ctx context.Context, st app.Stack) ([]Anchor, error) {
	key := fmt.Sprintf("%s\x00%s\x00%d", *st.MasterKey, st.UpdatedAt.UTC().Format(time.RFC3339Nano), st.Subs)
	r.mu.Lock()
	a, ok := r.anchors[key]
	r.mu.Unlock()
	if ok {
		return a, nil
	}
	r.setStage("finding the master's stars: " + st.Filter)
	b, err := r.store.Get(ctx, *st.MasterKey)
	if err != nil {
		return nil, fmt.Errorf("fetch the %s master: %w", st.Filter, err)
	}
	im, err := imagedata.Decode(b)
	if err != nil {
		return nil, fmt.Errorf("decode the %s master: %w", st.Filter, err)
	}
	a = Anchors(PlaneOf(im))
	r.mu.Lock()
	if len(r.anchors) > 16 {
		r.anchors = map[string][]Anchor{}
	}
	r.anchors[key] = a
	r.mu.Unlock()
	return a, nil
}

type gaiaCache struct {
	RA, Dec, Radius float64
	Fetched         time.Time
	Stars           [][3]float64
}

func objectSlug(object string) string {
	return strings.NewReplacer("/", "_", "\\", "_").Replace(object)
}

func (r *Runner) gaia(ctx context.Context, object string, ra, dec, radius float64) ([]Star, error) {
	key := keyPrefix + "cache/" + objectSlug(object) + "/gaia.json.zst"
	var c gaiaCache
	if err := r.getJSON(ctx, key, &c); err == nil && Separation(ra, dec, c.RA, c.Dec)+radius <= c.Radius+1e-9 {
		out := make([]Star, len(c.Stars))
		for i, s := range c.Stars {
			out[i] = Star{s[0], s[1], s[2]}
		}
		return out, nil
	}
	stars, err := r.cat.Gaia(ctx, ra, dec, radius)
	if err != nil {
		return nil, err
	}
	c = gaiaCache{RA: ra, Dec: dec, Radius: radius, Fetched: r.now(), Stars: make([][3]float64, len(stars))}
	for i, s := range stars {
		c.Stars[i] = [3]float64{s.RA, s.Dec, s.G}
	}
	if err := r.putJSON(ctx, key, c); err != nil {
		slog.Warn("Could not cache Gaia stars", "object", object, "error", err)
	}
	return stars, nil
}

type vsxCache struct {
	RA, Dec, Radius float64
	Fetched         time.Time
	Stars           []Variable
}

func (r *Runner) vsx(ctx context.Context, object string, ra, dec, radius float64) ([]Variable, error) {
	key := keyPrefix + "cache/" + objectSlug(object) + "/vsx.json.zst"
	var c vsxCache
	if err := r.getJSON(ctx, key, &c); err == nil && r.now().Sub(c.Fetched) < vsxMaxAge && Separation(ra, dec, c.RA, c.Dec)+radius <= c.Radius+1e-9 {
		return c.Stars, nil
	}
	vs, err := r.cat.VSX(ctx, ra, dec, radius)
	if err != nil {
		return nil, err
	}
	if err := r.putJSON(ctx, key, vsxCache{RA: ra, Dec: dec, Radius: radius, Fetched: r.now(), Stars: vs}); err != nil {
		slog.Warn("Could not cache VSX stars", "object", object, "error", err)
	}
	return vs, nil
}

func (r *Runner) getJSON(ctx context.Context, key string, v any) error {
	b, err := r.store.Get(ctx, key)
	if err != nil {
		return err
	}
	dec, err := zstd.NewReader(nil)
	if err != nil {
		return err
	}
	defer dec.Close()
	raw, err := dec.DecodeAll(b, nil)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

func (r *Runner) putJSON(ctx context.Context, key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBetterCompression))
	if err != nil {
		return err
	}
	defer enc.Close()
	return r.store.Put(ctx, key, enc.EncodeAll(raw, nil), "application/zstd")
}

// known asks the variable star and minor planet catalogues about the
// field, and the built-in catalogue for its galaxies.
// Without the variable stars or the known asteroids every change and
// track would look new, so either failing fails the run, which is tried
// again later.
func (r *Runner) known(ctx context.Context, object string, res *Result) (Known, error) {
	k := Known{Epoch: res.MidTime}
	radius := res.FieldRadius * 1.05
	vs, err := r.vsx(ctx, object, res.FieldRA, res.FieldDec, radius)
	if err != nil {
		return k, fmt.Errorf("variable star catalogue: %w", err)
	}
	k.Variables = vs
	k.Checked = append(k.Checked, "VSX")
	bs, err := r.cat.SkyBoT(ctx, res.FieldRA, res.FieldDec, radius, res.MidTime)
	if err != nil {
		return k, fmt.Errorf("known asteroids: %w", err)
	}
	k.Bodies = bs
	k.Checked = append(k.Checked, "SkyBoT")
	if r.opts.Catalog != nil {
		if ix, err := r.opts.Catalog(); err == nil {
			objs, _ := ix.Cone(ctx, res.FieldRA, res.FieldDec, radius)
			for _, o := range objs {
				if o.Type != catalog.TypeGalaxy || o.MajorArcmin <= 0 {
					continue
				}
				g := Galaxy{Name: o.Name, RA: o.RA, Dec: o.Dec, Major: o.MajorArcmin, Minor: o.Minor()}
				if g.Name == "" {
					g.Name = o.Designation
				}
				if o.PA != nil && !math.IsNaN(*o.PA) {
					g.PA, g.HasOrientation = *o.PA, true
				}
				k.Galaxies = append(k.Galaxies, g)
			}
			k.Checked = append(k.Checked, "galaxies")
		}
	}
	return k, nil
}

func jsonString(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// RunPrefix is where a run's cutouts live in the processed bucket.
func RunPrefix(run *app.DiscoveryRun) string {
	return path.Join(strings.TrimSuffix(keyPrefix, "/"), objectSlug(run.Object), run.Night.Format(time.DateOnly), run.Group) + "/"
}

// ADES is one detection of a moving object in the fields the IAU ADES
// format needs, ready for a Minor Planet Center report.
type ADES struct {
	ObsTime string   `json:"obsTime"`
	RA      float64  `json:"ra"`
	Dec     float64  `json:"dec"`
	RMSRA   float64  `json:"rmsRA"`
	RMSDec  float64  `json:"rmsDec"`
	AstCat  string   `json:"astCat"`
	Mag     *float64 `json:"mag,omitempty"`
	RMSMag  *float64 `json:"rmsMag,omitempty"`
	Band    string   `json:"band"`
	PhotCat string   `json:"photCat"`
	Mode    string   `json:"mode"`
	Exp     float64  `json:"exp"`
	Filter  string   `json:"filter"`
	FrameID int      `json:"frame_id"`
}

// TrackRecord is how a mover's track is stored.
type TrackRecord struct {
	Points     []TrackPoint `json:"points"`
	ADES       []ADES       `json:"ades"`
	TrkSub     string       `json:"trkSub"`
	RateArcmin float64      `json:"rate_arcsec_per_min"`
	PA         float64      `json:"pa_deg"`
	ResidualPx float64      `json:"residual_px"`
	BodyName   string       `json:"body_name,omitempty"`
	BodyNumber string       `json:"body_number,omitempty"`
}

type saveInput struct {
	res      *Result
	wcs      *WCS
	bodies   []Body
	labelled []Labelled
	tracks   []LabelledTrack
	checked  []string
	failed   []string
	exposure map[int]float64
	start    time.Time
}

// trkSub is a track's ADES temporary designation: D, the run in base 36,
// then the track's index in base 36, at most 8 characters.
func trkSub(run, i int) string {
	return strings.ToUpper(fmt.Sprintf("D%s%02s", strconv.FormatInt(int64(run), 36), strconv.FormatInt(int64(i%1296), 36)))
}

// counted says whether a candidate counts as new for the morning report:
// a change or a track that matched nothing known, not on a linked track
// and not vetted as an artifact or as known.
func counted(c app.DiscoveryCandidate) bool {
	if c.Vetting == app.VetArtifact || c.Vetting == app.VetKnown {
		return false
	}
	if c.Kind == app.DiscoveryKindMover {
		return c.Class == ClassUnknownMover
	}
	return c.Class == ClassSupernova || c.Class == ClassNewVariable
}

// trackCandidate stores a moving object as a candidate, with its detections
// in the fields an ADES report needs.
func trackCandidate(run *app.DiscoveryRun, i int, t LabelledTrack, res *Result, exposure map[int]float64) app.DiscoveryCandidate {
	astro := 0.0
	if res.AstroRMSArcsec != nil {
		astro = *res.AstroRMSArcsec
	}
	rec := TrackRecord{Points: t.Points, TrkSub: trkSub(run.ID, i), RateArcmin: t.RateArcmin, PA: t.PA, ResidualPx: t.ResidualPx,
		BodyName: t.BodyName, BodyNumber: t.BodyNumber}
	mags := make([]float64, 0, len(t.Points))
	snrs := make([]float64, 0, len(t.Points))
	for _, p := range t.Points {
		cen := res.HFRNight * 2 / math.Max(p.SNR, 1) * res.PixelScale
		rms := math.Round(math.Hypot(astro, cen)*1000) / 1000
		rec.ADES = append(rec.ADES, ADES{ObsTime: p.Time.UTC().Format("2006-01-02T15:04:05.000Z"), RA: round6(p.RA), Dec: round6(p.Dec), RMSRA: rms, RMSDec: rms,
			AstCat: "Gaia3", Mag: p.Mag, RMSMag: p.MagErr, Band: "G", PhotCat: "Gaia3", Mode: "CCD", Exp: exposure[p.FrameID], Filter: p.Filter, FrameID: p.FrameID})
		if p.Mag != nil {
			mags = append(mags, *p.Mag)
		}
		snrs = append(snrs, p.SNR)
	}
	mid := t.Points[len(t.Points)/2]
	c := app.DiscoveryCandidate{RunID: run.ID, Object: run.Object, Night: run.Night, Group: run.Group, Kind: app.DiscoveryKindMover, Class: t.Class,
		X: mid.X, Y: mid.Y, RA: mid.RA, Dec: mid.Dec, Sign: 1, SNR: median64(snrs), PresentIn: len(t.Points), SubsTested: res.NightSubs,
		Track: jsonString(rec), RateArcmin: &t.RateArcmin, OffsetArc: t.OffsetArc, Matches: jsonString(nonNil(matchList(t.Match)))}
	if len(mags) > 0 {
		m := median64(mags)
		c.NightMag = &m
	}
	return c
}

func (r *Runner) save(ctx context.Context, run *app.DiscoveryRun, in saveInput) error {
	res, w := in.res, in.wcs
	var old []app.DiscoveryCandidate
	if err := r.db.WithContext(ctx).Select("id, cutout_key").Where("run_id = ? AND cutout_key <> ''", run.ID).Find(&old).Error; err != nil {
		return err
	}
	prefix := RunPrefix(run) + strconv.FormatInt(r.now().UnixNano(), 36) + "/"
	cands := make([]app.DiscoveryCandidate, 0, len(in.labelled)+len(in.tracks))
	for i, l := range in.labelled {
		c := app.DiscoveryCandidate{RunID: run.ID, Object: run.Object, Night: run.Night, Group: run.Group, Kind: app.DiscoveryKindChange, Class: l.Class,
			X: l.X, Y: l.Y, RA: l.RA, Dec: l.Dec, Sign: l.Sign, SNR: l.SNR, DiffFlux: l.DiffFlux, RefFlux: l.RefFlux, DeltaMag: l.DeltaMag,
			NightMag: l.NightMag, RefMag: l.RefMag, GaiaG: l.GaiaG, Dipole: l.Dipole, PresentIn: l.PresentIn, SubsTested: l.SubsTested,
			Matches: jsonString(nonNil(l.Matches)), Light: jsonString(l.Light)}
		key := fmt.Sprintf("%sc%03d/", prefix, i)
		if err := r.putStamps(ctx, key, Cut(res.Night, res.Ref, res.Diff, l.X, l.Y), w, res.MidTime); err == nil {
			c.CutoutKey = key
		} else {
			slog.Warn("Could not save a cutout", "key", key, "error", err)
		}
		cands = append(cands, c)
	}
	known := 0
	for i, t := range in.tracks {
		c := trackCandidate(run, i, t, res, in.exposure)
		if t.Stamps != nil {
			key := fmt.Sprintf("%sm%03d/", prefix, i)
			if err := r.putStamps(ctx, key, *t.Stamps, w, t.Points[len(t.Points)/2].Time); err == nil {
				c.CutoutKey = key
			}
		}
		if t.Class == ClassKnownAsteroid {
			known++
		}
		cands = append(cands, c)
	}
	inField := 0
	if res.SubDepth != nil {
		for _, b := range bodiesInField(res, w, in.bodies) {
			if b.V <= *res.SubDepth {
				inField++
			}
		}
	}
	now := r.now()
	mid := res.MidTime
	run.State, run.Stage, run.Error, run.Attempts, run.NextTry = app.DiscoveryDone, "", nil, 0, nil
	run.Filters = strings.Join(res.Filters, ",")
	run.NightSubs, run.RefSubs, run.RefNights = res.NightSubs, res.RefSubs, res.RefNights
	run.HFRNight, run.HFRRef, run.Convolved, run.ApSigma = res.HFRNight, res.HFRRef, res.Convolved, res.ApSigma
	run.ZeroPoint, run.Depth, run.SubDepth, run.AstroRMS, run.AstroStars = res.ZeroPoint, res.Depth, res.SubDepth, res.AstroRMSArcsec, res.AstroStars
	run.PixelScale, run.FieldRA, run.FieldDec, run.MidTime = res.PixelScale, res.FieldRA, res.FieldDec, &mid
	run.Funnel, run.Checked, run.Failed = jsonString(res.Funnel), strings.Join(in.checked, ","), strings.Join(in.failed, ",")
	run.Candidates, run.Tracks, run.KnownTracks, run.BodiesInField, run.SubDetections = len(in.labelled), len(in.tracks), known, inField, res.SubDetections
	run.FinishedAt, run.Seconds = &now, now.Sub(in.start).Seconds()
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var vetted []app.DiscoveryCandidate
		if err := tx.Where("run_id = ? AND vetting <> ''", run.ID).Find(&vetted).Error; err != nil {
			return err
		}
		run.NewCandidates = 0
		for i := range cands {
			for _, o := range vetted {
				if o.Kind == cands[i].Kind && o.Sign == cands[i].Sign && math.Hypot(o.X-cands[i].X, o.Y-cands[i].Y) <= 2 {
					cands[i].Vetting, cands[i].VetNote, cands[i].VettedAt = o.Vetting, o.VetNote, o.VettedAt
				}
			}
			if counted(cands[i]) {
				run.NewCandidates++
			}
		}
		if err := tx.Where("run_id = ?", run.ID).Delete(&app.DiscoveryCandidate{}).Error; err != nil {
			return err
		}
		if len(cands) > 0 {
			if err := tx.CreateInBatches(&cands, 200).Error; err != nil {
				return err
			}
		}
		return tx.Save(run).Error
	})
	if err != nil {
		if rerr := r.store.RemovePrefix(context.WithoutCancel(ctx), prefix); rerr != nil {
			slog.Warn("Could not remove unsaved discovery cutouts", "prefix", prefix, "error", rerr)
		}
		return err
	}
	for _, o := range old {
		if err := r.store.RemovePrefix(ctx, o.CutoutKey); err != nil {
			slog.Warn("Could not remove old discovery cutouts", "prefix", o.CutoutKey, "error", err)
		}
	}
	counts := map[string]int{}
	for _, c := range cands {
		counts[c.Class]++
	}
	slog.Info("Discovery run", "object", run.Object, "night", run.Night.Format(time.DateOnly), "group", run.Group, "subs", res.NightSubs, "reference", res.RefSubs,
		"dropped_subs", res.DroppedSubs, "candidates", len(in.labelled), "tracks", len(in.tracks), "new", run.NewCandidates, "classes", counts,
		"depth", deref(res.Depth), "failed_checks", in.failed, "took", now.Sub(in.start).Round(time.Second))
	if r.opts.OnRun != nil {
		r.opts.OnRun(*run)
	}
	return nil
}

func deref(p *float64) any {
	if p == nil {
		return nil
	}
	return math.Round(*p*100) / 100
}

func round6(v float64) float64 { return math.Round(v*1e6) / 1e6 }

func nonNil(m []Match) []Match {
	if m == nil {
		return []Match{}
	}
	return m
}

func matchList(m *Match) []Match {
	if m == nil {
		return nil
	}
	return []Match{*m}
}

// bodiesInField are the known bodies inside the compared area at the
// night's middle.
func bodiesInField(res *Result, w *WCS, bs []Body) []Body {
	var out []Body
	for _, b := range bs {
		x, y, ok := w.PixelOf(b.RA, b.Dec)
		if !ok || x < 0 || y < 0 || int(x) >= res.Diff.W || int(y) >= res.Diff.H {
			continue
		}
		if res.Edge[int(y)*res.Diff.W+int(x)] {
			continue
		}
		out = append(out, b)
	}
	return out
}

func (r *Runner) putStamps(ctx context.Context, key string, st Stamps, w *WCS, at time.Time) error {
	n, rf, d, err := st.PNGs()
	if err != nil {
		return err
	}
	fits, err := st.FITS(w, []imagedata.Card{imagedata.StringCard("DATE-AVG", at.UTC().Format("2006-01-02T15:04:05.000"), "Mid time of the night")})
	if err != nil {
		return err
	}
	for name, b := range map[string][]byte{"night.png": n, "ref.png": rf, "diff.png": d} {
		if err := r.store.Put(ctx, key+name, b, "image/png"); err != nil {
			return err
		}
	}
	return r.store.Put(ctx, key+"stamp.fits", fits, "application/fits")
}
