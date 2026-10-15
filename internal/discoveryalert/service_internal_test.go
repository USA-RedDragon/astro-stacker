package discoveryalert

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/discoveryfollowup"
	"github.com/USA-RedDragon/astro-stacker/internal/morningreport"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/testdb"
	"gorm.io/gorm"
)

type fakeSender struct {
	mu       sync.Mutex
	messages []string
	photos   [][]morningreport.Photo
	fail     error
	calls    int
}

func (f *fakeSender) Message(_ context.Context, html string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.fail != nil {
		return f.fail
	}
	f.messages = append(f.messages, html)
	return nil
}

func (f *fakeSender) Photos(_ context.Context, p []morningreport.Photo) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.fail != nil {
		return f.fail
	}
	f.photos = append(f.photos, p)
	return nil
}

type fakeFollowups struct {
	db  *gorm.DB
	got []app.DiscoveryAlert
}

func (f *fakeFollowups) Request(_ context.Context, alerts []app.DiscoveryAlert) error {
	f.got = append(f.got, alerts...)
	for _, a := range alerts {
		if err := f.db.Create(&app.DiscoveryFollowup{AlertID: a.ID, Object: a.Object, Group: a.Group, Night: a.Night, Until: a.Night,
			Kind: a.Kind, Class: a.Class, State: app.FollowupWaiting}).Error; err != nil {
			return err
		}
	}
	return nil
}

type noCutouts struct{}

func (noCutouts) Cutout(context.Context, string) ([]byte, error) { return nil, errors.New("none") }

const (
	bothBrokers  = "ALeRCE,Fink"
	requiredOnly = "VSX,SkyBoT"
	m33          = "M33"
)

func testNow() time.Time { return time.Date(2026, 10, 15, 3, 0, 0, 0, time.UTC) }

func night(d int) time.Time { return time.Date(2026, 10, 14+d, 0, 0, 0, 0, time.UTC) }

func alertDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testdb.SQLite(t, "app")
	if err := db.AutoMigrate(&app.DiscoveryRun{}, &app.DiscoveryCandidate{}, &app.DiscoveryAlert{}, &app.DiscoveryAlertMessage{},
		&app.DiscoveryAlertState{}, &app.DiscoveryFollowup{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func newService(t *testing.T, db *gorm.DB, s Sender, f Followups, now *time.Time) *Service {
	t.Helper()
	return &Service{DB: db, Sender: s, Followups: f, Cutouts: noCutouts{}, WebURL: "https://example.test", Loc: time.UTC,
		Now: func() time.Time { return *now }, RetryAfter: 5 * time.Minute, MaxAttempts: 3}
}

func goodRun(t *testing.T, db *gorm.DB, object string, n time.Time) app.DiscoveryRun {
	t.Helper()
	r := app.DiscoveryRun{Object: object, Night: n, Group: "broadband", State: app.DiscoveryDone, Checked: "VSX,SkyBoT,galaxies,ALeRCE,Fink"}
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	return r
}

func ptr(v float64) *float64 { return &v }

func cand(run app.DiscoveryRun, class string, ra, dec float64) app.DiscoveryCandidate {
	return app.DiscoveryCandidate{RunID: run.ID, Object: run.Object, Night: run.Night, Group: run.Group, Kind: app.DiscoveryKindChange, Class: class,
		RA: ra, Dec: dec, Sign: 1, SNR: 12, NightMag: ptr(18.06), RefMag: ptr(18.49), DeltaMag: ptr(-0.43), PresentIn: 28, SubsTested: 28,
		Matches: `[{"catalog":"galaxy","name":"Andromeda Galaxy","sep_arcsec":344}]`}
}

func store(t *testing.T, db *gorm.DB, cs ...app.DiscoveryCandidate) {
	t.Helper()
	if err := db.Create(&cs).Error; err != nil {
		t.Fatal(err)
	}
}

func runTick(t *testing.T, s *Service) {
	t.Helper()
	if err := s.Tick(context.Background()); err != nil {
		t.Logf("tick: %v", err)
	}
}

func TestAlertable(t *testing.T) {
	t.Parallel()
	run := app.DiscoveryRun{State: app.DiscoveryDone, Checked: "VSX,SkyBoT,galaxies,ALeRCE,Fink"}
	base := app.DiscoveryCandidate{Kind: app.DiscoveryKindChange, Class: discoveries.ClassSupernova, Sign: 1}
	cases := []struct {
		name string
		edit func(*app.DiscoveryCandidate, *app.DiscoveryRun)
		want bool
	}{
		{"supernova", func(*app.DiscoveryCandidate, *app.DiscoveryRun) {}, true},
		{"nova", func(c *app.DiscoveryCandidate, _ *app.DiscoveryRun) { c.Class = ClassNova }, true},
		{"fading new variable", func(c *app.DiscoveryCandidate, _ *app.DiscoveryRun) {
			c.Class, c.Sign = discoveries.ClassNewVariable, -1
		}, true},
		{"known variable", func(c *app.DiscoveryCandidate, _ *app.DiscoveryRun) { c.Class = discoveries.ClassKnownVariable }, false},
		{"known transient", func(c *app.DiscoveryCandidate, _ *app.DiscoveryRun) { c.Class = discoveries.ClassKnownTransient }, false},
		{"low confidence", func(c *app.DiscoveryCandidate, _ *app.DiscoveryRun) { c.Class = discoveries.ClassLowConfidence }, false},
		{"vetted artifact", func(c *app.DiscoveryCandidate, _ *app.DiscoveryRun) { c.Vetting = app.VetArtifact }, false},
		{"vetted known", func(c *app.DiscoveryCandidate, _ *app.DiscoveryRun) { c.Vetting = app.VetKnown }, false},
		{"vetted real", func(c *app.DiscoveryCandidate, _ *app.DiscoveryRun) { c.Vetting = app.VetReal }, true},
		{"change on a mover's path", func(c *app.DiscoveryCandidate, _ *app.DiscoveryRun) { c.Class = discoveries.ClassUnknownMover }, false},
		{"unknown mover", func(c *app.DiscoveryCandidate, _ *app.DiscoveryRun) {
			c.Kind, c.Class = app.DiscoveryKindMover, discoveries.ClassUnknownMover
		}, true},
		{"known asteroid", func(c *app.DiscoveryCandidate, _ *app.DiscoveryRun) {
			c.Kind, c.Class = app.DiscoveryKindMover, discoveries.ClassKnownAsteroid
		}, false},
		{"Fink failed, ALeRCE answered", func(_ *app.DiscoveryCandidate, r *app.DiscoveryRun) { r.Failed = "Fink" }, true},
		{"both brokers failed", func(_ *app.DiscoveryCandidate, r *app.DiscoveryRun) {
			r.Checked, r.Failed = "VSX,SkyBoT,galaxies", bothBrokers
		}, false},
		{"ALeRCE answered then went down", func(_ *app.DiscoveryCandidate, r *app.DiscoveryRun) { r.Failed = bothBrokers }, false},
		{"no broker asked", func(_ *app.DiscoveryCandidate, r *app.DiscoveryRun) { r.Checked = requiredOnly }, false},
		{"SIMBAD failed", func(_ *app.DiscoveryCandidate, r *app.DiscoveryRun) { r.Failed = "SIMBAD" }, false},
		{"TNS failed", func(_ *app.DiscoveryCandidate, r *app.DiscoveryRun) { r.Failed = "TNS" }, true},
		{"VSX not asked", func(_ *app.DiscoveryCandidate, r *app.DiscoveryRun) { r.Checked = "SkyBoT,ALeRCE,Fink" }, false},
		{"mover while the brokers are down", func(c *app.DiscoveryCandidate, r *app.DiscoveryRun) {
			c.Kind, c.Class, r.Checked, r.Failed = app.DiscoveryKindMover, discoveries.ClassUnknownMover, requiredOnly, bothBrokers
		}, true},
		{"mover needs no brokers", func(c *app.DiscoveryCandidate, r *app.DiscoveryRun) {
			c.Kind, c.Class, r.Checked = app.DiscoveryKindMover, discoveries.ClassUnknownMover, requiredOnly
		}, true},
		{"mover without SkyBoT", func(c *app.DiscoveryCandidate, r *app.DiscoveryRun) {
			c.Kind, c.Class, r.Checked = app.DiscoveryKindMover, discoveries.ClassUnknownMover, "VSX"
		}, false},
		{"run not done", func(_ *app.DiscoveryCandidate, r *app.DiscoveryRun) { r.State = app.DiscoveryRunning }, false},
	}
	for _, tc := range cases {
		c, r := base, run
		tc.edit(&c, &r)
		if got := Alertable(c, r); got != tc.want {
			t.Errorf("%s: Alertable = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestFormatPosition(t *testing.T) {
	t.Parallel()
	if got := RA(10.557966104327647); got != "00h 42m 13.91s" {
		t.Errorf("RA %q", got)
	}
	if got := Dec(41.27670411346332); got != "+41° 16′ 36.1″" {
		t.Errorf("Dec %q", got)
	}
	if got := Dec(-0.5); got != "-00° 30′ 00.0″" {
		t.Errorf("Dec %q", got)
	}
	if got := RA(359.99999999); got != "00h 00m 00.00s" {
		t.Errorf("RA wraps: %q", got)
	}
}

func TestCaptionSaysOnlyWhatWasMeasured(t *testing.T) {
	t.Parallel()
	c := app.DiscoveryCandidate{Object: "Andromeda", Night: night(-3), Group: "broadband", Kind: app.DiscoveryKindChange, Class: discoveries.ClassSupernova,
		RA: 10.557966104327647, Dec: 41.27670411346332, Sign: 1, NightMag: ptr(18.056), RefMag: ptr(18.492), DeltaMag: ptr(-0.348), PresentIn: 28, SubsTested: 28,
		Matches: `[{"catalog":"galaxy","name":"Andromeda Galaxy","sep_arcsec":344.2}]`}
	got := Caption(c, "https://astro.test/")
	for _, want := range []string{"possible supernova in Andromeda", "magnitude 18.06", "0.35 magnitudes brighter than the reference (18.49)",
		"RA 00h 42m 13.91s, Dec +41° 16′ 36.1″", "In Andromeda Galaxy", "Seen in 28 of 28 subs", `href="https://astro.test/discoveries?night=2026-10-11"`} {
		if !strings.Contains(got, want) {
			t.Errorf("caption lacks %q:\n%s", want, got)
		}
	}
	c.NightMag, c.RefMag, c.DeltaMag, c.Matches = nil, nil, nil, ""
	got = Caption(c, "")
	if !strings.Contains(got, "magnitude not measured") || strings.Contains(got, "In ") || strings.Contains(got, "href") {
		t.Errorf("caption without measurements:\n%s", got)
	}
	if len(got) > morningreport.MaxCaption {
		t.Errorf("caption is %d chars", len(got))
	}
}

func moverTrack(t *testing.T, at time.Time, ra, dec float64) string {
	t.Helper()
	b, err := json.Marshal(discoveries.TrackRecord{RateArcmin: 0.5, PA: 90, Points: []discoveries.TrackPoint{{Time: at, RA: ra, Dec: dec}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSameKeysChangesOnPlaceAndMoversOnPath(t *testing.T) {
	t.Parallel()
	a := app.DiscoveryAlert{Object: m33, Night: night(-1), Class: discoveries.ClassSupernova, Kind: app.DiscoveryKindChange, RA: 10, Dec: 41}
	near := app.DiscoveryCandidate{Object: m33, Night: night(-1), Kind: app.DiscoveryKindChange, Class: discoveries.ClassSupernova, RA: 10 + 1.0/3600, Dec: 41}
	far := near
	far.RA = 10 + 4.0/3600
	relabelled := near
	relabelled.Class = discoveries.ClassNewVariable
	monthLater := near
	monthLater.Night = night(30)
	otherTarget := near
	otherTarget.Object = "M31"
	for name, tc := range map[string]struct {
		c    app.DiscoveryCandidate
		want bool
	}{"near": {near, true}, "far": {far, false}, "relabelled": {relabelled, true}, "a month later": {monthLater, true}, "other target": {otherTarget, false}} {
		if got := Same(a, tc.c); got != tc.want {
			t.Errorf("%s: Same = %v", name, got)
		}
	}
	got := fresh([]app.DiscoveryCandidate{far, near, relabelled, far}, []app.DiscoveryAlert{a})
	if len(got) != 1 || got[0].RA != far.RA {
		t.Errorf("fresh = %+v", got)
	}

	at := time.Date(2026, 10, 13, 5, 0, 0, 0, time.UTC)
	mover := app.DiscoveryCandidate{Object: m33, Night: night(-1), Kind: app.DiscoveryKindMover, Class: discoveries.ClassUnknownMover, RA: 23.4, Dec: 30.6,
		Track: moverTrack(t, at, 23.4, 30.6)}
	ma := alertRow(mover, false)
	later := at.Add(26 * time.Hour)
	ra, dec, _ := discoveryfollowup.Predict(23.4, 30.6, 0.5, 90, 26*time.Hour)
	onPath := app.DiscoveryCandidate{Object: m33, Night: night(0), Kind: app.DiscoveryKindMover, Class: discoveries.ClassUnknownMover, RA: ra, Dec: dec,
		Track: moverTrack(t, later, ra, dec)}
	if !Same(ma, onPath) {
		t.Errorf("the mover on its path the next night is new")
	}
	shifted := app.DiscoveryCandidate{Object: m33, Night: night(-1), Kind: app.DiscoveryKindMover, Class: discoveries.ClassUnknownMover, RA: 23.4 + 30.0/3600, Dec: 30.6,
		Track: moverTrack(t, at.Add(70*time.Minute), 23.4+35.0/3600/math.Cos(30.6*math.Pi/180), 30.6)}
	if !Same(ma, shifted) {
		t.Errorf("a rerun that moved the midpoint along the path is new")
	}
	offPath := onPath
	offPath.Track = moverTrack(t, later, ra, dec+0.05)
	if Same(ma, offPath) {
		t.Errorf("a mover off the path is the same")
	}
}

func TestOneAlertPerSourceAcrossRerunsAndRestarts(t *testing.T) {
	t.Parallel()
	db := alertDB(t)
	now := testNow()
	sender, follow := &fakeSender{}, &fakeFollowups{db: db}
	s := newService(t, db, sender, follow, &now)
	old := goodRun(t, db, "M31", night(-1))
	store(t, db, cand(old, discoveries.ClassSupernova, 10.5, 41.2))
	runTick(t, s)
	if sender.calls != 0 {
		t.Fatalf("the baseline alerted: %d sends", sender.calls)
	}
	var base int64
	db.Model(&app.DiscoveryAlert{}).Where("baseline").Count(&base)
	if base != 1 {
		t.Fatalf("baseline rows %d", base)
	}

	run := goodRun(t, db, m33, night(-1))
	store(t, db, cand(run, discoveries.ClassSupernova, 23.46, 30.66))
	runTick(t, s)
	if len(sender.photos) != 1 || len(sender.photos[0]) != 1 || !strings.Contains(sender.photos[0][0].Caption, "possible supernova in M33") ||
		len(sender.photos[0][0].JPEG) == 0 {
		t.Fatalf("want one photo alert, got %d photo sends, messages %v", len(sender.photos), sender.messages)
	}
	if len(follow.got) != 1 || follow.got[0].Object != m33 {
		t.Errorf("follow-ups %d", len(follow.got))
	}

	db.Where("run_id = ?", run.ID).Delete(&app.DiscoveryCandidate{})
	store(t, db, cand(run, discoveries.ClassSupernova, 23.46+0.5/3600, 30.66))
	runTick(t, s)
	restarted := newService(t, db, sender, follow, &now)
	runTick(t, restarted)
	if sender.calls != 1 {
		t.Errorf("a rerun or restart alerted again: %d sends", sender.calls)
	}
	if len(follow.got) != 1 {
		t.Errorf("follow-up asked again: %d", len(follow.got))
	}

	next := goodRun(t, db, m33, night(0))
	store(t, db, cand(next, discoveries.ClassSupernova, 23.46, 30.66))
	runTick(t, s)
	if sender.calls != 1 {
		t.Errorf("the same source the next night alerted as new: %d sends", sender.calls)
	}
}

func TestOldNightsAndUntrustedRunsNeverAlert(t *testing.T) {
	t.Parallel()
	db := alertDB(t)
	now := testNow()
	sender := &fakeSender{}
	s := newService(t, db, sender, nil, &now)
	runTick(t, s)
	old := goodRun(t, db, "M31", night(-5))
	store(t, db, cand(old, discoveries.ClassSupernova, 10.5, 41.2))
	bad := goodRun(t, db, m33, night(-1))
	db.Model(&bad).Update("failed", bothBrokers)
	store(t, db, cand(bad, discoveries.ClassSupernova, 23.46, 30.66))
	known := goodRun(t, db, "M101", night(-1))
	store(t, db, cand(known, discoveries.ClassKnownTransient, 210.8, 54.3), cand(known, discoveries.ClassLowConfidence, 210.9, 54.3))
	runTick(t, s)
	if sender.calls != 0 {
		t.Errorf("sent %d", sender.calls)
	}
}

func TestSeveralFindsOfARunGoInOneMessage(t *testing.T) {
	t.Parallel()
	db := alertDB(t)
	now := testNow()
	sender, follow := &fakeSender{}, &fakeFollowups{db: db}
	s := newService(t, db, sender, follow, &now)
	runTick(t, s)
	run := goodRun(t, db, m33, night(-1))
	store(t, db, cand(run, discoveries.ClassSupernova, 23.46, 30.66), cand(run, discoveries.ClassNewVariable, 23.50, 30.70),
		cand(run, discoveries.ClassNewVariable, 23.50, 30.70+1.0/3600))
	runTick(t, s)
	if len(sender.messages) != 1 || !strings.Contains(sender.messages[0], "2 new finds in M33") {
		t.Fatalf("messages %v", sender.messages)
	}
	if len(sender.photos) != 1 || len(sender.photos[0]) != 2 {
		t.Fatalf("photo sends %d", len(sender.photos))
	}
	if len(follow.got) != 2 {
		t.Errorf("follow-ups %d", len(follow.got))
	}
}

func TestMoreThanFiveFindsSendOneSummary(t *testing.T) {
	t.Parallel()
	db := alertDB(t)
	now := testNow()
	sender, follow := &fakeSender{}, &fakeFollowups{db: db}
	s := newService(t, db, sender, follow, &now)
	runTick(t, s)
	run := goodRun(t, db, "Cygnus", night(-1))
	for i := range MaxPerRun + 1 {
		store(t, db, cand(run, discoveries.ClassNewVariable, 310+float64(i)*0.01, 30))
	}
	runTick(t, s)
	if len(sender.messages) != 1 || len(sender.photos) != 0 || !strings.Contains(sender.messages[0], "6 new finds in Cygnus in one night") ||
		!strings.Contains(sender.messages[0], "6 possible new variable star") {
		t.Fatalf("messages %v photos %d", sender.messages, len(sender.photos))
	}
	if len(follow.got) != 0 {
		t.Errorf("a summary asked for follow-ups: %d", len(follow.got))
	}
	store(t, db, cand(run, discoveries.ClassNewVariable, 311, 30))
	runTick(t, s)
	if len(sender.messages) != 2 || len(sender.photos) != 0 || !strings.Contains(sender.messages[1], "7 new finds in Cygnus") ||
		!strings.Contains(sender.messages[1], "Not alerted before: 1 possible new variable star") {
		t.Errorf("a busy run's later find: messages %v photos %d", sender.messages, len(sender.photos))
	}
}

func TestUncertainSendIsNeverRepeated(t *testing.T) {
	t.Parallel()
	db := alertDB(t)
	now := testNow()
	sender := &fakeSender{fail: fmt.Errorf("telegram sendPhoto: %w: timeout", morningreport.ErrOutcomeUnknown)}
	s := newService(t, db, sender, nil, &now)
	runTick(t, s)
	run := goodRun(t, db, m33, night(-1))
	store(t, db, cand(run, discoveries.ClassSupernova, 23.46, 30.66))
	runTick(t, s)
	sender.fail = nil
	now = now.Add(time.Hour)
	runTick(t, s)
	if sender.calls != 1 || len(sender.photos) != 0 {
		t.Errorf("an uncertain send went again: %d calls", sender.calls)
	}
}

func TestDefiniteFailureIsRetried(t *testing.T) {
	t.Parallel()
	db := alertDB(t)
	now := testNow()
	sender := &fakeSender{fail: errors.New("telegram sendPhoto: Bad Request: chat not found")}
	s := newService(t, db, sender, nil, &now)
	runTick(t, s)
	run := goodRun(t, db, m33, night(-1))
	store(t, db, cand(run, discoveries.ClassSupernova, 23.46, 30.66))
	runTick(t, s)
	sender.fail = nil
	now = now.Add(time.Minute)
	runTick(t, s)
	if len(sender.photos) != 0 {
		t.Fatalf("retried before RetryAfter")
	}
	now = now.Add(5 * time.Minute)
	runTick(t, s)
	if len(sender.photos) != 1 || sender.calls != 2 {
		t.Errorf("retry: %d photos, %d calls", len(sender.photos), sender.calls)
	}
	runTick(t, s)
	if sender.calls != 2 {
		t.Errorf("sent again after success: %d calls", sender.calls)
	}
}

func TestRetriesStopAtMaxAttempts(t *testing.T) {
	t.Parallel()
	db := alertDB(t)
	now := testNow()
	sender := &fakeSender{fail: errors.New("Bad Request")}
	s := newService(t, db, sender, nil, &now)
	runTick(t, s)
	run := goodRun(t, db, m33, night(-1))
	store(t, db, cand(run, discoveries.ClassSupernova, 23.46, 30.66))
	for range 6 {
		runTick(t, s)
		now = now.Add(10 * time.Minute)
	}
	if sender.calls != s.MaxAttempts {
		t.Errorf("%d sends, want %d", sender.calls, s.MaxAttempts)
	}
}

func TestRecurringSourceAlertsOnce(t *testing.T) {
	t.Parallel()
	db := alertDB(t)
	now := testNow()
	sender := &fakeSender{}
	s := newService(t, db, sender, nil, &now)
	runTick(t, s)
	run := goodRun(t, db, m33, night(-1))
	store(t, db, cand(run, discoveries.ClassSupernova, 23.46, 30.66))
	runTick(t, s)
	for _, d := range []int{3, 11, 40} {
		now = testNow().AddDate(0, 0, d)
		r := goodRun(t, db, m33, night(d-1))
		c := cand(r, discoveries.ClassNewVariable, 23.46+0.5/3600, 30.66)
		store(t, db, c)
		runTick(t, s)
	}
	if sender.calls != 1 {
		t.Errorf("a source that keeps coming back alerted %d times", sender.calls)
	}
}

func TestClaimedButUnsentIsSentLater(t *testing.T) {
	t.Parallel()
	db := alertDB(t)
	now := testNow()
	sender, follow := &fakeSender{}, &fakeFollowups{db: db}
	s := newService(t, db, sender, follow, &now)
	runTick(t, s)
	run := goodRun(t, db, m33, night(-1))
	store(t, db, cand(run, discoveries.ClassSupernova, 23.46, 30.66))
	pairs, err := s.candidates(context.Background(), night(-3))
	if err != nil {
		t.Fatal(err)
	}
	bs := Due(pairs, nil)
	if len(bs) != 1 {
		t.Fatalf("batches %d", len(bs))
	}
	if _, err := s.claim(context.Background(), bs[0]); err != nil {
		t.Fatal(err)
	}
	if err := s.requestFollowups(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(follow.got) != 0 {
		t.Errorf("a follow-up was asked for an alert never sent")
	}
	runTick(t, s)
	if sender.calls != 0 {
		t.Fatalf("sent before the retry wait")
	}
	now = now.Add(6 * time.Minute)
	runTick(t, s)
	if len(sender.photos) != 1 || len(follow.got) != 1 {
		t.Errorf("a claim left by a restart: %d photo sends, %d follow-ups", len(sender.photos), len(follow.got))
	}
}

func TestBackoffDoubles(t *testing.T) {
	t.Parallel()
	s := &Service{RetryAfter: 5 * time.Minute}
	if s.Backoff(1) != 5*time.Minute || s.Backoff(2) != 10*time.Minute || s.Backoff(4) != 40*time.Minute {
		t.Errorf("backoff %v %v %v", s.Backoff(1), s.Backoff(2), s.Backoff(4))
	}
}
