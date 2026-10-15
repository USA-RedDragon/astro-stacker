package discoveryalert

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/morningreport"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/trigger"
	"gorm.io/gorm"
)

// Sender delivers alerts; morningreport.Telegram is one.
type Sender interface {
	Message(ctx context.Context, html string) error
	Photos(ctx context.Context, photos []morningreport.Photo) error
}

// Followups hears of alerts that should have their field imaged again.
type Followups interface {
	Request(ctx context.Context, alerts []app.DiscoveryAlert) error
}

// Service sends each new find once, soon after its run is stored.
type Service struct {
	DB      *gorm.DB
	Sender  Sender
	Cutouts CutoutReader
	// WebURL is the astro-processing site the alerts link to.
	WebURL string
	// Loc is the observatory's time zone, which says what today's night is.
	Loc *time.Location
	Now func() time.Time
	// RetryAfter is the wait before a send that failed for certain is
	// tried again, up to MaxAttempts sends.
	RetryAfter  time.Duration
	MaxAttempts int
	// Interval is the backstop between looks for new finds.
	Interval  time.Duration
	Followups Followups

	kick *trigger.Debouncer
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) loc() *time.Location {
	if s.Loc != nil {
		return s.Loc
	}
	return time.UTC
}

// Kick asks for a look soon, as when a run was stored.
func (s *Service) Kick() {
	if s.kick != nil {
		s.kick.Kick()
	}
}

// Run looks for new finds at start, soon after each Kick and every
// Interval, until ctx ends.
func (s *Service) Run(ctx context.Context) {
	if s.kick == nil {
		s.kick = &trigger.Debouncer{Quiet: 20 * time.Second, MaxWait: time.Minute}
	}
	interval := s.Interval
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	s.kick.Run(ctx, interval, func(ctx context.Context, _ []string) {
		if err := s.Tick(ctx); err != nil {
			slog.Error("Discovery alerts", colError, err)
		}
	})
}

// Tick seeds the baseline the first time, sends what is due and asks for
// the follow-ups of what was sent.
func (s *Service) Tick(ctx context.Context) error {
	seeded, err := s.seed(ctx)
	if err != nil || seeded {
		return err
	}
	var errs []error
	if err := s.retry(ctx); err != nil {
		errs = append(errs, err)
	}
	if err := s.alertNew(ctx); err != nil {
		errs = append(errs, err)
	}
	if err := s.requestFollowups(ctx); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// today is the night that is under way or just ended, by its evening date:
// the civil date in Loc twelve hours ago.
func (s *Service) today() time.Time {
	t := s.now().In(s.loc()).Add(-12 * time.Hour)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

type pair struct {
	c   app.DiscoveryCandidate
	run app.DiscoveryRun
}

// candidates are the stored candidates of a new class from nights since
// since, with their runs.
func (s *Service) candidates(ctx context.Context, since time.Time) ([]pair, error) {
	var cands []app.DiscoveryCandidate
	if err := s.DB.WithContext(ctx).Where("class IN ? AND night >= ?", newClasses(), since).Order("run_id, id").Find(&cands).Error; err != nil {
		return nil, err
	}
	if len(cands) == 0 {
		return nil, nil
	}
	ids := make([]int, 0, len(cands))
	for _, c := range cands {
		if len(ids) == 0 || ids[len(ids)-1] != c.RunID {
			ids = append(ids, c.RunID)
		}
	}
	var runs []app.DiscoveryRun
	if err := s.DB.WithContext(ctx).Where("id IN ?", ids).Find(&runs).Error; err != nil {
		return nil, err
	}
	byID := map[int]app.DiscoveryRun{}
	for _, r := range runs {
		byID[r.ID] = r
	}
	out := make([]pair, 0, len(cands))
	for _, c := range cands {
		if r, ok := byID[c.RunID]; ok {
			out = append(out, pair{c: c, run: r})
		}
	}
	return out, nil
}

// seed records every candidate already stored as a baseline the first time
// the alerts run, so turning them on does not alert on the past.
func (s *Service) seed(ctx context.Context) (bool, error) {
	var n int64
	if err := s.DB.WithContext(ctx).Model(&app.DiscoveryAlertState{}).Count(&n).Error; err != nil || n > 0 {
		return false, err
	}
	pairs, err := s.candidates(ctx, time.Time{})
	if err != nil {
		return false, err
	}
	rows := []app.DiscoveryAlert{}
	for _, p := range pairs {
		if NewClass(p.c.Class) {
			rows = append(rows, alertRow(p.c, true))
		}
	}
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if len(rows) > 0 {
			if err := tx.CreateInBatches(&rows, 200).Error; err != nil {
				return err
			}
		}
		return tx.Create(&app.DiscoveryAlertState{ID: 1, SeededAt: s.now()}).Error
	})
	if err != nil {
		return false, err
	}
	slog.Info("Discovery alerts on; candidates already stored are not alerted", "baseline", len(rows))
	return true, nil
}

func alertRow(c app.DiscoveryCandidate, baseline bool) app.DiscoveryAlert {
	a := app.DiscoveryAlert{Night: c.Night, Class: c.Class, Kind: c.Kind, RA: c.RA, Dec: c.Dec, Object: c.Object, Group: c.Group,
		RunID: c.RunID, CandidateID: c.ID, Baseline: baseline}
	if m, ok := MotionOf(c); ok {
		a.At, a.RateArcmin, a.PA = &m.At, &m.RateArcmin, &m.PA
		a.RA, a.Dec = m.RA, m.Dec
	}
	return a
}

// Batch is what one run's message would send.
type Batch struct {
	RunID   int
	Summary bool
	Cands   []app.DiscoveryCandidate
	// Total is how many alertable finds the run holds, those alerted
	// before included.
	Total int
}

// Due groups the new finds not yet alerted by run. Finds an alert of
// another run covers are sources seen before, not finds of this run. A run
// with more than MaxPerRun finds of its own becomes a summary.
func Due(pairs []pair, have []app.DiscoveryAlert) []Batch {
	var out []Batch
	byRun := map[int][]app.DiscoveryCandidate{}
	order := []int{}
	for _, p := range pairs {
		if !Alertable(p.c, p.run) || slices.ContainsFunc(have, func(a app.DiscoveryAlert) bool { return a.RunID != p.run.ID && Same(a, p.c) }) {
			continue
		}
		if _, ok := byRun[p.run.ID]; !ok {
			order = append(order, p.run.ID)
		}
		byRun[p.run.ID] = append(byRun[p.run.ID], p.c)
	}
	for _, id := range order {
		all := byRun[id]
		todo := fresh(all, have)
		if len(todo) == 0 {
			continue
		}
		out = append(out, Batch{RunID: id, Summary: len(all) > MaxPerRun, Cands: todo, Total: len(all)})
	}
	return out
}

func (s *Service) alertNew(ctx context.Context) error {
	since := s.today().AddDate(0, 0, -MaxAgeNights)
	pairs, err := s.candidates(ctx, since)
	if err != nil || len(pairs) == 0 {
		return err
	}
	objects := []string{}
	for _, p := range pairs {
		if !slices.Contains(objects, p.c.Object) {
			objects = append(objects, p.c.Object)
		}
	}
	var have []app.DiscoveryAlert
	if err := s.DB.WithContext(ctx).Omit("caption", "photo").Where("object IN ?", objects).Find(&have).Error; err != nil {
		return err
	}
	var errs []error
	for _, b := range Due(pairs, have) {
		id, err := s.claim(ctx, b)
		if err != nil {
			errs = append(errs, fmt.Errorf("claim run %d: %w", b.RunID, err))
			continue
		}
		if err := s.send(ctx, id); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Render builds a batch's message and the alert rows it covers, without
// storing or sending anything.
func (s *Service) Render(ctx context.Context, b Batch) (app.DiscoveryAlertMessage, []app.DiscoveryAlert) {
	msg := app.DiscoveryAlertMessage{RunID: b.RunID, Summary: b.Summary, Count: len(b.Cands)}
	rows := make([]app.DiscoveryAlert, 0, len(b.Cands))
	for i, c := range b.Cands {
		row := alertRow(c, false)
		switch {
		case b.Summary:
		case len(b.Cands) == 1:
			row.Caption = Caption(c, s.WebURL)
			row.Photo = Photo(ctx, s.Cutouts, c.CutoutKey)
		default:
			row.Caption = ShortCaption(i, c)
			row.Photo = Photo(ctx, s.Cutouts, c.CutoutKey)
		}
		rows = append(rows, row)
	}
	switch {
	case b.Summary:
		msg.Text = SummaryText(b.Cands, max(b.Total, len(b.Cands)), s.WebURL)
	case len(b.Cands) == 1:
		msg.Text = rows[0].Caption
	default:
		msg.Text = GroupText(b.Cands, s.WebURL)
	}
	return msg, rows
}

// claim stores a batch's message and alert rows in one transaction, after
// checking again that no alert covers its candidates.
func (s *Service) claim(ctx context.Context, b Batch) (int, error) {
	msg, rows := s.Render(ctx, b)
	msg.ClaimedAt, msg.Attempts = s.now(), 1
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var have []app.DiscoveryAlert
		if err := tx.Omit("caption", "photo").Where("object = ?", b.Cands[0].Object).Find(&have).Error; err != nil {
			return err
		}
		for _, c := range b.Cands {
			for _, a := range have {
				if Same(a, c) {
					return errClaimed
				}
			}
		}
		if err := tx.Create(&msg).Error; err != nil {
			return err
		}
		for i := range rows {
			rows[i].MessageID = &msg.ID
		}
		return tx.Create(&rows).Error
	})
	return msg.ID, err
}

var errClaimed = errors.New("already alerted")

const colError = "error"

// Backoff is the wait before the next send of a message tried attempts
// times: RetryAfter doubling with each attempt.
func (s *Service) Backoff(attempts int) time.Duration {
	return s.RetryAfter << min(max(attempts-1, 0), 16)
}

// retry sends again the messages not sent for certain: those whose send
// failed for certain and those claimed but never sent, as by a restart
// between the claim and the send.
func (s *Service) retry(ctx context.Context) error {
	var msgs []app.DiscoveryAlertMessage
	if err := s.DB.WithContext(ctx).Where("sent_at IS NULL AND sending_at IS NULL AND attempts < ?", s.MaxAttempts).
		Find(&msgs).Error; err != nil {
		return err
	}
	var errs []error
	for _, m := range msgs {
		if s.now().Before(m.ClaimedAt.Add(s.Backoff(m.Attempts))) {
			continue
		}
		res := s.DB.WithContext(ctx).Model(&app.DiscoveryAlertMessage{}).Where("id = ? AND attempts = ? AND sending_at IS NULL AND sent_at IS NULL", m.ID, m.Attempts).
			Updates(map[string]any{"claimed_at": s.now(), "attempts": m.Attempts + 1})
		if res.Error != nil || res.RowsAffected == 0 {
			continue
		}
		if err := s.send(ctx, m.ID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// send sends a claimed message once. A send whose outcome is unknown keeps
// SendingAt set and is never sent again; one that failed for certain is
// left for retry.
func (s *Service) send(ctx context.Context, id int) error {
	db := s.DB.WithContext(context.WithoutCancel(ctx))
	var msg app.DiscoveryAlertMessage
	if err := db.First(&msg, id).Error; err != nil {
		return err
	}
	var rows []app.DiscoveryAlert
	if err := db.Where("message_id = ?", id).Order("id").Find(&rows).Error; err != nil {
		return err
	}
	record := func(fields map[string]any) {
		if err := db.Model(&app.DiscoveryAlertMessage{}).Where("id = ?", id).Updates(fields).Error; err != nil {
			slog.Error("Recording a discovery alert's state failed", "message", id, colError, err)
		}
	}
	if s.Sender == nil {
		record(map[string]any{colError: "no sender"})
		return errors.New("discovery alert: no sender")
	}
	res := db.Model(&app.DiscoveryAlertMessage{}).Where("id = ? AND sending_at IS NULL AND sent_at IS NULL", id).Update("sending_at", s.now())
	if res.Error != nil {
		return fmt.Errorf("record the send before sending: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return nil
	}
	var err error
	single := !msg.Summary && len(rows) == 1 && len(rows[0].Photo) > 0
	if single {
		err = s.Sender.Photos(ctx, []morningreport.Photo{{Caption: rows[0].Caption, JPEG: rows[0].Photo}})
	} else {
		err = s.Sender.Message(ctx, msg.Text)
	}
	if err != nil {
		if errors.Is(err, morningreport.ErrOutcomeUnknown) {
			record(map[string]any{colError: err.Error()})
			return fmt.Errorf("discovery alert %d may have gone out: %w", id, err)
		}
		record(map[string]any{colError: err.Error(), "sending_at": nil})
		if msg.Attempts >= s.MaxAttempts {
			slog.Error("Discovery alert not sent; giving up", "message", id, "run", msg.RunID, "attempts", msg.Attempts, "error", err)
		}
		return fmt.Errorf("discovery alert %d: %w", id, err)
	}
	record(map[string]any{"sent_at": s.now(), colError: nil})
	slog.Info("Discovery alert sent", "message", id, "run", msg.RunID, "candidates", len(rows), "summary", msg.Summary)
	if single || msg.Summary {
		return nil
	}
	photos := make([]morningreport.Photo, 0, len(rows))
	for _, r := range rows {
		if len(r.Photo) > 0 {
			photos = append(photos, morningreport.Photo{Caption: r.Caption, JPEG: r.Photo})
		}
	}
	if len(photos) == 0 {
		return nil
	}
	if err := s.Sender.Photos(ctx, photos); err != nil {
		record(map[string]any{colError: "photos: " + err.Error()})
		return fmt.Errorf("discovery alert %d photos: %w", id, err)
	}
	return nil
}

// requestFollowups asks for the follow-up of every alert that went into a
// message listing it, summaries and baselines aside, which has none yet.
func (s *Service) requestFollowups(ctx context.Context) error {
	if s.Followups == nil {
		return nil
	}
	var rows []app.DiscoveryAlert
	err := s.DB.WithContext(ctx).Table("discovery_alerts a").Select("a.*").
		Joins("JOIN discovery_alert_messages m ON m.id = a.message_id").
		Where("NOT a.baseline AND NOT m.summary AND a.night >= ?", s.today().AddDate(0, 0, -MaxAgeNights)).
		Where("m.sent_at IS NOT NULL OR m.sending_at IS NOT NULL").
		Where("NOT EXISTS (SELECT 1 FROM discovery_followups f WHERE f.alert_id = a.id)").
		Order("a.id").Find(&rows).Error
	if err != nil || len(rows) == 0 {
		return err
	}
	return s.Followups.Request(ctx, rows)
}
