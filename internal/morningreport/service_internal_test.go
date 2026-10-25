package morningreport

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type fakeSender struct {
	mu       sync.Mutex
	messages []string
	photos   [][]Photo
	fail     error
}

func (f *fakeSender) Message(_ context.Context, html string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return f.fail
	}
	f.messages = append(f.messages, html)
	return nil
}

func (f *fakeSender) Photos(_ context.Context, p []Photo) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.photos = append(f.photos, p)
	return nil
}

func testService(t *testing.T, db *gorm.DB, sender Sender, now *time.Time) *Service {
	t.Helper()
	sched, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	s := chicago(t)
	return &Service{
		DB:          db,
		Collector:   Collector{App: db, Sched: sched, Schedule: s},
		Formatter:   Formatter{Loc: s.Loc, Zone: "CT"},
		Sender:      sender,
		Schedule:    s,
		Now:         func() time.Time { return *now },
		RetryAfter:  15 * time.Minute,
		MaxAttempts: 3,
	}
}

func appDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.Frame{}, &app.StackFrame{}, &app.Stack{}, &app.Cover{}, &app.MorningReport{}, &app.BodyNight{}, &app.NightReference{},
		&app.DustFlat{}, &app.DustEvent{}, &app.DustLightCheck{}, &app.RimStreak{}); err != nil {
		t.Fatal(err)
	}
	exp := 600.0
	set := 2.0
	when := at("02:00:00")
	f := app.Frame{Key: "lights/M 15/a.fits", ETag: "a", Type: lightType, Object: objM15, Filter: filterL, Exposure: &exp, SetTemp: &set, DateObs: &when}
	if err := db.Create(&f).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&app.StackFrame{FrameID: f.ID, Status: app.StackStatusAdded}).Error; err != nil {
		t.Fatal(err)
	}
	return db
}

func clock(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestServiceSendsEachNightOnce(t *testing.T) {
	t.Parallel()
	db := appDB(t)
	sender := &fakeSender{}
	now := clock("2026-10-14T10:59:00-05:00")
	svc := testService(t, db, sender, &now)
	ctx := context.Background()

	if wait := svc.Tick(ctx); wait != time.Minute || len(sender.messages) != 0 {
		t.Fatalf("before 11:00: waited %s, sent %d", wait, len(sender.messages))
	}
	now = clock("2026-10-14T11:00:00-05:00")
	if wait := svc.Tick(ctx); wait != 24*time.Hour {
		t.Fatalf("after sending waits %s, want 24h", wait)
	}
	if len(sender.messages) != 1 || !strings.Contains(sender.messages[0], "night of Tue Oct 13") || !strings.Contains(sender.messages[0], "M 15: Luminance 1") {
		t.Fatalf("sent %q", sender.messages)
	}
	now = clock("2026-10-14T11:05:00-05:00")
	svc.Tick(ctx)
	restarted := testService(t, db, sender, &now)
	restarted.Tick(ctx)
	if len(sender.messages) != 1 {
		t.Fatalf("a tick and a restart sent it again: %d messages", len(sender.messages))
	}
	var rec app.MorningReport
	if err := db.First(&rec, "night = ?", night1013).Error; err != nil || rec.SentAt == nil || rec.Text != sender.messages[0] {
		t.Fatalf("record %+v %v", rec, err)
	}
}

func TestServiceSendsAMissedReportLateTheSameDayOnly(t *testing.T) {
	t.Parallel()
	db := appDB(t)
	sender := &fakeSender{}
	now := clock("2026-10-14T16:30:00-05:00")
	testService(t, db, sender, &now).Tick(context.Background())
	if len(sender.messages) != 1 {
		t.Fatalf("pod back at 16:30: sent %d, want the missed report", len(sender.messages))
	}

	other := appDB(t)
	late := &fakeSender{}
	next := clock("2026-10-15T09:00:00-05:00")
	if wait := testService(t, other, late, &next).Tick(context.Background()); wait != 2*time.Hour {
		t.Fatalf("waited %s, want 2h until 11:00", wait)
	}
	if len(late.messages) != 0 {
		t.Fatal("a report missed the day before is not sent the next morning")
	}
}

func TestServiceSkipsNightsBeforeTheFirst(t *testing.T) {
	t.Parallel()
	db := appDB(t)
	sender := &fakeSender{}
	now := clock("2026-10-14T12:20:00-05:00")
	svc := testService(t, db, sender, &now)
	svc.First = day(night1014)
	svc.Tick(context.Background())
	if len(sender.messages) != 0 {
		t.Fatal("deployed after 11:00: the night before the first must not be sent")
	}
	now = clock("2026-10-15T11:00:00-05:00")
	svc.Tick(context.Background())
	if len(sender.messages) != 1 || !strings.Contains(sender.messages[0], "night of Wed Oct 14") {
		t.Fatalf("the first night is sent: %q", sender.messages)
	}
}

func TestServiceRetriesAFailedSendAndStops(t *testing.T) {
	t.Parallel()
	db := appDB(t)
	sender := &fakeSender{fail: errors.New("telegram sendMessage: Bad Gateway")}
	now := clock("2026-10-14T11:00:00-05:00")
	svc := testService(t, db, sender, &now)
	ctx := context.Background()
	if wait := svc.Tick(ctx); wait != 15*time.Minute {
		t.Fatalf("after a failure waits %s, want 15m", wait)
	}
	now = now.Add(5 * time.Minute)
	if wait := svc.Tick(ctx); wait != 10*time.Minute {
		t.Fatalf("waits %s, want the 10m left", wait)
	}
	now = now.Add(10 * time.Minute)
	svc.Tick(ctx)
	now = now.Add(15 * time.Minute)
	svc.Tick(ctx)
	var rec app.MorningReport
	if err := db.First(&rec, "night = ?", night1013).Error; err != nil || rec.Attempts != 3 || rec.Error == nil {
		t.Fatalf("record %+v %v", rec, err)
	}
	sender.fail = nil
	now = now.Add(time.Hour)
	svc.Tick(ctx)
	if len(sender.messages) != 0 {
		t.Fatal("sent after MaxAttempts")
	}
}

func TestServiceDoesNotResendASendThatMayHaveGoneOut(t *testing.T) {
	t.Parallel()
	db := appDB(t)
	sending := clock("2026-10-14T11:00:30-05:00")
	if err := db.Create(&app.MorningReport{Night: night1013, ClaimedAt: clock("2026-10-14T11:00:00-05:00"), SendingAt: &sending, Attempts: 1}).Error; err != nil {
		t.Fatal(err)
	}
	sender := &fakeSender{}
	now := clock("2026-10-14T13:00:00-05:00")
	testService(t, db, sender, &now).Tick(context.Background())
	if len(sender.messages) != 0 {
		t.Fatal("a send cut off after it went to Telegram may have gone out; it must not go again")
	}
}

func TestServiceRetriesASendStoppedWhileRendering(t *testing.T) {
	t.Parallel()
	db := appDB(t)
	if err := db.Create(&app.MorningReport{Night: night1013, ClaimedAt: clock("2026-10-14T11:00:00-05:00"), Attempts: 1}).Error; err != nil {
		t.Fatal(err)
	}
	sender := &fakeSender{}
	now := clock("2026-10-14T11:04:00-05:00")
	svc := testService(t, db, sender, &now)
	if wait := svc.Tick(context.Background()); wait != 6*time.Minute || len(sender.messages) != 0 {
		t.Fatalf("inside the claim lease: waited %s, sent %d", wait, len(sender.messages))
	}
	now = clock("2026-10-14T11:10:00-05:00")
	svc.Tick(context.Background())
	if len(sender.messages) != 1 {
		t.Fatalf("a claim left by a send that stopped before Telegram is sent after the lease: sent %d", len(sender.messages))
	}
	var rec app.MorningReport
	if err := db.First(&rec, "night = ?", night1013).Error; err != nil || rec.SentAt == nil || rec.SendingAt == nil || rec.Attempts != 2 {
		t.Fatalf("record %+v %v", rec, err)
	}
}

func TestServiceDoesNotRetryWhenTelegramMayHaveIt(t *testing.T) {
	t.Parallel()
	db := appDB(t)
	sender := &fakeSender{fail: fmt.Errorf("telegram sendMessage: %w: timeout", ErrOutcomeUnknown)}
	now := clock("2026-10-14T11:00:00-05:00")
	svc := testService(t, db, sender, &now)
	svc.Tick(context.Background())
	sender.fail = nil
	now = now.Add(2 * time.Hour)
	svc.Tick(context.Background())
	if len(sender.messages) != 0 {
		t.Fatal("a send Telegram may have taken went again")
	}
	var rec app.MorningReport
	if err := db.First(&rec, "night = ?", night1013).Error; err != nil || rec.SendingAt == nil || rec.Error == nil || rec.Attempts != 1 {
		t.Fatalf("record %+v %v", rec, err)
	}
}

func TestServiceRecordsTheOutcomeAfterCancel(t *testing.T) {
	t.Parallel()
	db := appDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	sender := &cancelSender{cancel: cancel}
	now := clock("2026-10-14T11:00:00-05:00")
	testService(t, db, sender, &now).Tick(ctx)
	var rec app.MorningReport
	if err := db.First(&rec, "night = ?", night1013).Error; err != nil || rec.Error == nil || rec.SendingAt != nil {
		t.Fatalf("a definite failure as the pod stops is recorded and retryable: %+v %v", rec, err)
	}
}

type cancelSender struct{ cancel context.CancelFunc }

func (c *cancelSender) Message(context.Context, string) error {
	c.cancel()
	return errors.New("telegram sendMessage: Bad Request")
}

func (c *cancelSender) Photos(context.Context, []Photo) error { return nil }

func TestRenderLeavesUnreachableSourcesNotMeasured(t *testing.T) {
	t.Parallel()
	db := appDB(t)
	now := clock("2026-10-14T11:00:00-05:00")
	text, photos, r, err := testService(t, db, &fakeSender{}, &now).Render(context.Background(), day(night1013))
	if err != nil {
		t.Fatal(err)
	}
	if r.Subs.Count != 1 || r.Subs.Kept != 1 || len(r.Camera.Setpoints) != 1 || len(photos) != 0 {
		t.Fatalf("report %+v photos %d", r, len(photos))
	}
	for _, want := range []string{"Safe not measured", "<b>Guiding</b> not measured", "<b>Autofocus</b> not measured", "setpoint 2 °C (1 sub)"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in\n%s", want, text)
		}
	}
}
