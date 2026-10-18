package binaries

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/activity"
	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/discoveryfollowup"
	"github.com/USA-RedDragon/astro-stacker/internal/planning"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/starcat"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/trigger"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Objects reads and writes the processed bucket.
type Objects interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Put(ctx context.Context, key string, data []byte, contentType string) error
	RemovePrefix(ctx context.Context, prefix string) error
}

// Commands sends scheduler commands once each, reads their outcome and
// takes them back.
type Commands interface {
	SubmitOnce(ctx context.Context, id string, kind schedcmd.Kind, payload json.RawMessage, author string) (schedcmd.Record, error)
	Status(ctx context.Context, id string) (schedcmd.Record, error)
	Undo(ctx context.Context, id, author string) (schedcmd.Record, error)
}

// StarReader reads a source's star as the vet saw it, from its caches.
type StarReader interface {
	Star(ctx context.Context, src app.DiscoverySource) (vetting.StarData, error)
}

// Service keeps the confirmed binaries' facts, files and share pictures
// up to date in the background and answers the pages.
type Service struct {
	DB      *gorm.DB
	Stars   StarReader
	VSX     *starcat.VSX
	Objects Objects
	// Frame is a target's registration frame: plate solution and size.
	Frame    func(ctx context.Context, object string) (*discoveries.WCS, int, int, error)
	Site     func(ctx context.Context) (sky.Site, error)
	Snapshot func(ctx context.Context) (*planning.Snapshot, error)
	Commands Commands
	Loc      *time.Location
	Now      func() time.Time
	// Task shows the background work in the processing indicator.
	Task *activity.Task
	// Photometry measures the stars in the user's own frames to time
	// their eclipses, shown in the indicator by TimingTask; nil leaves
	// eclipses untimed.
	Photometry Photometry
	TimingTask *activity.Task
	Interval   time.Duration

	kick     *trigger.Debouncer
	kickOnce sync.Once
	mu       sync.Mutex
	want     map[int]bool
}

// Settings keys.
const settingDiscoverer = "discoverer"

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) loc() *time.Location {
	if s.Loc != nil {
		return s.Loc
	}
	return time.UTC
}

// Kick asks for a look soon.
func (s *Service) Kick() { s.debouncer().Kick() }

func (s *Service) debouncer() *trigger.Debouncer {
	s.kickOnce.Do(func() { s.kick = &trigger.Debouncer{Quiet: 20 * time.Second, MaxWait: 2 * time.Minute} })
	return s.kick
}

// Run works out facts, files and share pictures at start, soon after
// each Kick and every Interval until ctx ends.
func (s *Service) Run(ctx context.Context) {
	kick := s.debouncer()
	every := s.Interval
	if every <= 0 {
		every = 15 * time.Minute
	}
	kick.Run(ctx, every, func(ctx context.Context, _ []string) {
		if err := s.Tick(ctx); err != nil {
			slog.Error("Discovery pages background work", "error", err)
		}
	})
}

// sourceRow is a confirmed source with its vet report.
func (s *Service) load(ctx context.Context, ids []int) ([]Star, error) {
	db := s.DB.WithContext(ctx)
	var srcs []app.DiscoverySource
	q := db.Where("vetting = ?", app.VetReal)
	if ids != nil {
		q = db.Where("id IN ?", ids)
	}
	if err := q.Order("id").Find(&srcs).Error; err != nil {
		return nil, err
	}
	if len(srcs) == 0 {
		return nil, nil
	}
	idList := make([]int, len(srcs))
	for i, x := range srcs {
		idList[i] = x.ID
	}
	var vets []app.DiscoveryVet
	if err := db.Where("source_id IN ?", idList).Find(&vets).Error; err != nil {
		return nil, err
	}
	var facts []app.BinaryFacts
	if err := db.Where("source_id IN ?", idList).Find(&facts).Error; err != nil {
		return nil, err
	}
	var books []app.BinaryBook
	if err := db.Where("source_id IN ?", idList).Find(&books).Error; err != nil {
		return nil, err
	}
	var shares []app.ShareImage
	if err := db.Where("source_id IN ?", idList).Find(&shares).Error; err != nil {
		return nil, err
	}
	var timings []app.EclipseTiming
	if err := db.Where("source_id IN ?", idList).Order("predicted").Find(&timings).Error; err != nil {
		return nil, err
	}
	tm := map[int][]app.EclipseTiming{}
	for _, t := range timings {
		tm[t.SourceID] = append(tm[t.SourceID], t)
	}
	reports := map[int]*vetting.Report{}
	for _, v := range vets {
		if v.Report == "" {
			continue
		}
		var r vetting.Report
		if json.Unmarshal([]byte(v.Report), &r) == nil {
			reports[v.SourceID] = &r
		}
	}
	rows := map[int]*app.BinaryFacts{}
	for i := range facts {
		rows[facts[i].SourceID] = &facts[i]
	}
	bk := map[int]app.BinaryBook{}
	for _, b := range books {
		bk[b.SourceID] = b
	}
	sh := map[int]*app.ShareImage{}
	for i := range shares {
		sh[shares[i].SourceID] = &shares[i]
	}
	var out []Star
	for _, src := range srcs {
		st := Star{Source: src, Report: reports[src.ID], Row: rows[src.ID], Book: bk[src.ID], Share: sh[src.ID], Timings: tm[src.ID]}
		if st.Book.SourceID == 0 {
			st.Book.SourceID = src.ID
		}
		if r := st.Report; r != nil && st.Row != nil && sameRun(st.Row.VetRanAt, r.RanAt) {
			st.Facts = decodeFacts(st.Row)
		}
		out = append(out, st)
	}
	return out, nil
}

// Binaries are the confirmed sources the pages list: vetted real, with a
// periodic eclipsing ephemeris.
func (s *Service) Binaries(ctx context.Context) ([]Star, error) {
	all, err := s.load(ctx, nil)
	if err != nil {
		return nil, err
	}
	var out []Star
	for _, st := range all {
		if Periodic(st.Report) {
			out = append(out, st)
		}
	}
	return out, nil
}

// Binary is one confirmed binary; gorm.ErrRecordNotFound when id is not
// one.
func (s *Service) Binary(ctx context.Context, id int) (Star, error) {
	all, err := s.load(ctx, []int{id})
	if err != nil {
		return Star{}, err
	}
	if len(all) != 1 || all[0].Source.Vetting != app.VetReal || !Periodic(all[0].Report) {
		return Star{}, gorm.ErrRecordNotFound
	}
	return all[0], nil
}

// Discoverer is the discoverer line the user set for VSX.
func (s *Service) Discoverer(ctx context.Context) (string, error) {
	var row app.BinarySetting
	err := s.DB.WithContext(ctx).Where("name = ?", settingDiscoverer).Limit(1).Find(&row).Error
	return row.Value, err
}

// SetDiscoverer stores the discoverer line.
func (s *Service) SetDiscoverer(ctx context.Context, v string) error {
	row := app.BinarySetting{Key: settingDiscoverer, Value: strings.TrimSpace(v)}
	return s.DB.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "name"}}, DoUpdates: clause.AssignmentColumns([]string{"value"})}).Create(&row).Error
}

// BookChange is what the user changes on a card or kit; nil fields stay.
type BookChange struct {
	VSXSubmitted *bool           `json:"vsx_submitted"`
	VSXRef       *string         `json:"vsx_ref"`
	Notes        *string         `json:"notes"`
	PrimaryName  *string         `json:"primary_name"`
	Checklist    map[string]bool `json:"checklist"`
}

// Update applies a change to one binary's book.
func (s *Service) Update(ctx context.Context, id int, c BookChange) (Star, error) {
	st, err := s.Binary(ctx, id)
	if err != nil {
		return st, err
	}
	b := st.Book
	b.SourceID = id
	if c.VSXSubmitted != nil {
		if *c.VSXSubmitted && !b.VSXSubmitted {
			t := s.now()
			b.VSXSubmittedAt = &t
		}
		if !*c.VSXSubmitted {
			b.VSXSubmittedAt = nil
		}
		b.VSXSubmitted = *c.VSXSubmitted
	}
	if c.VSXRef != nil {
		b.VSXRef = strings.TrimSpace(*c.VSXRef)
	}
	if c.Notes != nil {
		b.Notes = strings.TrimSpace(*c.Notes)
	}
	if c.PrimaryName != nil {
		b.PrimaryName = strings.TrimSpace(*c.PrimaryName)
	}
	if c.Checklist != nil {
		keep := map[string]bool{}
		for _, it := range checkItems() {
			if v, ok := c.Checklist[it.key]; ok && v && it.key != checkVSXClear {
				keep[it.key] = true
			}
		}
		raw, err := json.Marshal(keep)
		if err != nil {
			return st, err
		}
		b.Checklist = string(raw)
	}
	b.UpdatedAt = s.now()
	if err := s.DB.WithContext(ctx).Save(&b).Error; err != nil {
		return st, err
	}
	st.Book = b
	return st, nil
}

// Progress is the background facts work, for the pages.
type Progress struct {
	State string `json:"state"`
	Done  int64  `json:"done"`
	Total int64  `json:"total"`
}

// Progress is how far the background work is.
func (s *Service) Progress() Progress {
	if s.Task == nil {
		return Progress{State: activity.StateIdle}
	}
	it := s.Task.Item()
	st := activity.StateIdle
	if it.State == activity.StateRunning {
		st = activity.StateRunning
	}
	return Progress{State: st, Done: it.Done, Total: it.Total}
}

// sameRun says two vet run times are the same run: the database keeps
// microseconds, the report JSON nanoseconds.
func sameRun(a, b time.Time) bool {
	return a.Truncate(time.Microsecond).Equal(b.Truncate(time.Microsecond))
}

// stale says a star's facts need working out again.
func stale(st Star) bool {
	if st.Report == nil || !Periodic(st.Report) {
		return false
	}
	r := st.Row
	return r == nil || r.Version != FactsVersion || !sameRun(r.VetRanAt, st.Report.RanAt) || (r.State == FactsFailed && time.Since(r.ComputedAt) > 6*time.Hour)
}

// Tick works out stale facts and files, makes share pictures whose colour
// picture changed, and follows the imaging requests.
func (s *Service) Tick(ctx context.Context) error {
	stars, err := s.Binaries(ctx)
	if err != nil {
		return err
	}
	var todo []Star
	for _, st := range stars {
		if stale(st) {
			todo = append(todo, st)
		}
	}
	shares, err := s.shareWork(ctx, stars)
	if err != nil {
		slog.Warn("Could not list the share pictures to make", "error", err)
	}
	total := int64(len(todo) + len(shares))
	var errs []error
	if total > 0 {
		s.Task.Start("Working out the facts of your confirmed variable stars", total, "stars")
		for _, st := range todo {
			s.Task.SetCurrent("Survey facts of find #" + fmt.Sprint(st.Source.ID))
			if err := s.work(ctx, st); err != nil {
				errs = append(errs, fmt.Errorf("source %d: %w", st.Source.ID, err))
			}
			s.Task.Step(1)
		}
		for _, w := range shares {
			s.Task.SetCurrent("Picture of find #" + fmt.Sprint(w.src.ID))
			if err := s.makePictures(ctx, w); err != nil {
				errs = append(errs, fmt.Errorf("picture of source %d: %w", w.src.ID, err))
			}
			s.Task.Step(1)
		}
		s.Task.Finish(errors.Join(errs...))
	}
	if err := s.followRequests(ctx); err != nil {
		errs = append(errs, err)
	}
	if err := s.timingWork(ctx, stars); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// Timings is one binary's timing record.
func (s *Service) Timings(ctx context.Context, id int) (TimingRecord, error) {
	st, err := s.Binary(ctx, id)
	if err != nil {
		return TimingRecord{}, err
	}
	return st.Record(s.loc()), nil
}

// vsxRadius is how far around a star the VSX copy is read, in arcseconds.
const vsxRadius = 30.0

// work works out one star's facts and renders its plots.
func (s *Service) work(ctx context.Context, st Star) error {
	row := app.BinaryFacts{SourceID: st.Source.ID, Version: FactsVersion, VetRanAt: st.Report.RanAt.Truncate(time.Microsecond), ComputedAt: s.now(), State: FactsReady}
	if st.Row != nil {
		row.ChartStamp, row.Files = st.Row.ChartStamp, st.Row.Files
	}
	sd, err := s.Stars.Star(ctx, st.Source)
	if err != nil {
		row.State, row.Error = FactsFailed, err.Error()
		return errors.Join(err, s.DB.WithContext(ctx).Save(&row).Error)
	}
	var near []starcat.VSXStar
	var checked time.Time
	if s.VSX != nil {
		ra, dec := st.Source.RA, st.Source.Dec
		if sd.Gaia != nil {
			ra, dec = sd.Gaia.RA, sd.Gaia.Dec
		}
		if near, err = s.VSX.Cone(ctx, ra, dec, vsxRadius/3600, nil); err == nil {
			checked = s.now()
		} else {
			slog.Warn("Could not read the VSX copy", "source", st.Source.ID, "error", err)
		}
	}
	f := Work(st.Report, sd, near, checked)
	st.Facts = &f
	files := Files(st.Row)
	name, _ := st.Names()
	files[FilePhased] = s.putPNG(ctx, st.Source.ID, FilePhased, func() ([]byte, string, error) {
		return PhasedPlot(sd.Series, f.Ephemeris, f.Deeper, name, [2]int{f.FirstYear, f.LastYear})
	})
	if st.ownEclipse() {
		files[FileOwn] = s.putPNG(ctx, st.Source.ID, FileOwn, func() ([]byte, string, error) {
			for _, ch := range st.Report.Charts {
				if ch.ID == "night" {
					return OwnPlot(ch, name)
				}
			}
			return nil, "", errors.New("the vet kept no chart of your night")
		})
	} else {
		files[FileOwn] = FileFact{State: FileNone, Note: "Not measured yet. Your frames have not timed an eclipse of this star; the Eclipse planner lists the next ones."}
	}
	raw, err := json.Marshal(f)
	if err != nil {
		return err
	}
	fr, err := json.Marshal(files)
	if err != nil {
		return err
	}
	row.Facts, row.Files = string(raw), string(fr)
	row.ChartStamp = ""
	return s.DB.WithContext(ctx).Save(&row).Error
}

// maxFileBytes is the largest supporting file VSX takes.
const maxFileBytes = 1 << 20

func fileKey(id int, name string) string { return fmt.Sprintf("binaries/%d/%s.png", id, name) }

func (s *Service) putPNG(ctx context.Context, id int, name string, render func() ([]byte, string, error)) FileFact {
	b, caption, err := render()
	if err != nil {
		return FileFact{State: FileNone, Note: "Not made: " + err.Error() + "."}
	}
	if len(b) > maxFileBytes {
		return FileFact{State: FileFailed, Note: fmt.Sprintf("The picture came out %d KB, over the 1 MB VSX takes.", len(b)/1024)}
	}
	key := fileKey(id, name)
	if err := s.Objects.Put(ctx, key, b, "image/png"); err != nil {
		return FileFact{State: FileFailed, Note: "Could not store it: " + err.Error()}
	}
	return FileFact{State: FileReady, Key: key, Bytes: len(b), Caption: caption}
}

// File is one rendered file's bytes.
func (s *Service) File(ctx context.Context, id int, name string) ([]byte, error) {
	st, err := s.Binary(ctx, id)
	if err != nil {
		return nil, err
	}
	ff, ok := Files(st.Row)[strings.TrimSuffix(name, ".png")]
	if !ok || ff.State != FileReady || ff.Key == "" {
		return nil, gorm.ErrRecordNotFound
	}
	return s.Objects.Get(ctx, ff.Key)
}

// Kit is one binary's VSX kit.
func (s *Service) Kit(ctx context.Context, id int, fileURL func(int, string) string, shareURL func(int, int) string) (Kit, error) {
	st, err := s.Binary(ctx, id)
	if err != nil {
		return Kit{}, err
	}
	d, err := s.Discoverer(ctx)
	if err != nil {
		return Kit{}, err
	}
	return KitOf(st, d, Files(st.Row), fileURL, shareURL), nil
}

// Plan is the eclipse planner for the next days.
func (s *Service) Plan(ctx context.Context, days int, all bool) (EclipsePlan, error) {
	stars, err := s.Binaries(ctx)
	if err != nil {
		return EclipsePlan{}, err
	}
	var reqs []app.EclipseRequest
	if err := s.DB.WithContext(ctx).Where("state <> ?", app.EclipseCancel).Find(&reqs).Error; err != nil {
		return EclipsePlan{}, err
	}
	in := PlanInput{Stars: stars, Loc: s.loc(), From: s.now().Truncate(time.Minute), All: all, Requests: reqs}
	in.To = in.From.AddDate(0, 0, days)
	if s.Site != nil {
		if site, err := s.Site(ctx); err == nil {
			in.Site = &site
		}
	}
	return Plan(in), nil
}

// Request errors the caller made, as opposed to the server's.
var (
	ErrNoEclipse = errors.New("no predicted eclipse of that star near that time")
	ErrNotReady  = errors.New("the star's survey facts are not worked out yet")
	ErrOver      = errors.New("that eclipse's imaging window is over")
	// ErrCannotCancel says a request's project can no longer be taken back.
	ErrCannotCancel = errors.New("the request cannot be cancelled")
)

// RequestImaging asks the scheduler to image one predicted eclipse of a
// binary: kind and mid must match a prediction within requestMatch.
func (s *Service) RequestImaging(ctx context.Context, id int, kind string, mid time.Time) (app.EclipseRequest, error) {
	st, err := s.Binary(ctx, id)
	if err != nil {
		return app.EclipseRequest{}, err
	}
	if st.Facts == nil || st.Facts.Gaia == nil {
		return app.EclipseRequest{}, ErrNotReady
	}
	e, _ := st.ephemeris()
	ra, dec := st.Facts.Gaia.Now(mid)
	var hit *Predicted
	for _, p := range Predict(e, st.Facts.Deeper, ra, dec, mid.Add(-requestMatch), mid.Add(requestMatch)) {
		if p.Kind == kind && (hit == nil || absDur(p.Mid.Sub(mid)) < absDur(hit.Mid.Sub(mid))) {
			p = st.withPhaseError(p, e)
			hit = &p
		}
	}
	if hit == nil {
		return app.EclipseRequest{}, ErrNoEclipse
	}
	start, end := hit.Window()
	r := app.EclipseRequest{SourceID: id, Kind: kind, Mid: hit.Mid, Start: start, End: end, State: app.EclipseWaiting, CreatedAt: s.now(), UpdatedAt: s.now()}
	if !end.After(s.now()) {
		return r, ErrOver
	}
	var before []app.EclipseRequest
	if err := s.DB.WithContext(ctx).Where("source_id = ? AND kind = ?", id, kind).Find(&before).Error; err != nil {
		return r, err
	}
	for _, b := range before {
		if absDur(b.Mid.Sub(hit.Mid)) > time.Minute {
			continue
		}
		if b.State == app.EclipseWaiting || b.State == app.EclipseSent || b.State == app.EclipseDone {
			return b, nil
		}
		r.Attempt++
	}
	if s.Commands == nil || s.Snapshot == nil {
		return r, errors.New("the scheduler is not set up")
	}
	snap, err := s.Snapshot(ctx)
	if err != nil {
		return r, fmt.Errorf("planning snapshot: %w", err)
	}
	orig, err := discoveryfollowup.OriginalTarget(ctx, s.DB, &app.DiscoveryFollowup{Object: st.Source.Object, Night: st.Source.Night})
	if err != nil {
		return r, err
	}
	name, _ := st.Names()
	g := st.Facts.Gaia.G
	payload, why := DraftRequest(snap, orig, &r, name, st.Source.Object, g, hit.Duration, s.loc())
	if payload == nil {
		r.State, r.Note = app.EclipseFailed, why
		return r, s.DB.WithContext(ctx).Create(&r).Error
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return r, err
	}
	rec, err := s.Commands.SubmitOnce(ctx, "eclipse-request-"+r.ProjectGUID, schedcmd.KindProjectCreate, raw, RequestAuthor)
	if err != nil {
		r.State, r.Note = app.EclipseFailed, "the scheduler did not take it: "+err.Error()
	} else {
		r.CreateCmd, r.State = rec.ID, app.EclipseSent
		r.Note = fmt.Sprintf("Sent to the scheduler: %d × %gs %s from %s to %s.", r.Subs, r.Exposure, r.Filter,
			start.In(s.loc()).Format("Jan 2 3:04 PM"), end.In(s.loc()).Format("3:04 PM MST"))
	}
	return r, s.DB.WithContext(ctx).Create(&r).Error
}

// CancelRequest takes an imaging request back: its command is withdrawn
// or its project removed.
func (s *Service) CancelRequest(ctx context.Context, id int) (app.EclipseRequest, error) {
	var r app.EclipseRequest
	if err := s.DB.WithContext(ctx).First(&r, id).Error; err != nil {
		return r, err
	}
	if r.CreateCmd != "" && s.Commands != nil && (r.State == app.EclipseSent || r.State == app.EclipseWaiting) {
		if _, err := s.Commands.Undo(ctx, r.CreateCmd, RequestAuthor); err != nil {
			if errors.Is(err, schedcmd.ErrNotUndoable) {
				return r, fmt.Errorf("%w: the scheduler cannot take its project back any more; close it in Target Scheduler", ErrCannotCancel)
			}
			return r, err
		}
	}
	r.State, r.Note, r.UpdatedAt = app.EclipseCancel, "Cancelled.", s.now()
	return r, s.DB.WithContext(ctx).Save(&r).Error
}

// followRequests marks requests the scheduler refused as failed, and
// closes the project of each request whose window has passed.
func (s *Service) followRequests(ctx context.Context) error {
	if s.Commands == nil {
		return nil
	}
	var open []app.EclipseRequest
	if err := s.DB.WithContext(ctx).Where("state = ?", app.EclipseSent).Find(&open).Error; err != nil {
		return err
	}
	var errs []error
	for _, r := range open {
		rec, err := s.Commands.Status(ctx, r.CreateCmd)
		switch {
		case err == nil && refused(rec.Status):
			r.State, r.Note = app.EclipseFailed, "The scheduler did not take it: "+cmpOr(rec.Message, string(rec.Status))
		case s.now().After(r.End.Add(time.Hour)):
			if err := s.closeProject(ctx, &r); err != nil {
				errs = append(errs, err)
				continue
			}
			r.State, r.Note = app.EclipseDone, "The window has passed."
		default:
			continue
		}
		r.UpdatedAt = s.now()
		if err := s.DB.WithContext(ctx).Save(&r).Error; err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

func refused(s schedcmd.Status) bool {
	return s == schedcmd.StatusRejected || s == schedcmd.StatusFailed || s == schedcmd.StatusConflict || s == schedcmd.StatusCancelled
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// closeProject closes a passed request's project in Target Scheduler.
func (s *Service) closeProject(ctx context.Context, r *app.EclipseRequest) error {
	if s.Snapshot == nil {
		return nil
	}
	snap, err := s.Snapshot(ctx)
	if err != nil {
		return err
	}
	for _, p := range snap.Projects {
		if p.GUID != r.ProjectGUID || p.State == "Closed" {
			continue
		}
		raw, err := json.Marshal(schedcmd.EditPayload{Entity: "project", ID: int64(p.ID), GUID: p.GUID, Name: p.Name,
			Changes: []schedcmd.FieldChange{{Field: "state", Before: json.RawMessage(stateNumber(p.State)), After: json.RawMessage("3")}}})
		if err != nil {
			return err
		}
		rec, err := s.Commands.SubmitOnce(ctx, "eclipse-request-close-"+r.ProjectGUID, schedcmd.KindProjectEdit, raw, RequestAuthor)
		if err != nil {
			return err
		}
		r.CloseCmd = rec.ID
	}
	return nil
}

func stateNumber(name string) string {
	for k, v := range schedcmd.ProjectStates() {
		if v == name {
			return fmt.Sprint(k)
		}
	}
	return "1"
}

// decodeImage reads a JPEG, as the stacker writes colour pictures.
func decodeImage(b []byte) (image.Image, error) {
	return jpeg.Decode(bytes.NewReader(b))
}

func finiteAll(v ...float64) bool {
	for _, x := range v {
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return false
		}
	}
	return true
}
