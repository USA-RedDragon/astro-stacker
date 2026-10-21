package binaries

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/planning"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
	"github.com/klauspost/compress/zstd"
	"gorm.io/gorm"
)

func ebSuggestion() vetting.Suggestion {
	return vetting.Suggestion{State: app.VetReal, Confidence: vetting.High, Kind: vetting.KindEclipsingBinary}
}

func storedReport(t *testing.T, name string) *vetting.Report {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name+".json.zst"))
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zstd.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	var r vetting.Report
	if err := json.NewDecoder(zr).Decode(&r); err != nil {
		t.Fatal(err)
	}
	return &r
}

func TestStoredReportsKnownAnswers(t *testing.T) {
	t.Parallel()
	r3635 := storedReport(t, "report_3635")
	if !EclipsingBinary(r3635) {
		t.Fatal("3635, an EA binary vetted under star:15, is not a binary")
	}
	if p := r3635.Ephemeris.Period; math.Abs(2*p-7.83949) > 0.001 {
		t.Errorf("3635 vet period %.5f, want half of 7.83949", p)
	}
	r15523 := storedReport(t, "report_15523")
	if EclipsingBinary(r15523) {
		t.Error("15523, a dipper that fades now and then, is a binary")
	}
	if o := (Star{Source: app.DiscoverySource{ID: 15523}, Report: r15523}).OtherView(nil); o.VetLine != "Fades now and then" || o.Period != 0 || o.Waiting != "" {
		t.Errorf("15523 card %+v", o)
	}
	r167 := storedReport(t, "report_167_stale")
	if EclipsingBinary(r167) {
		t.Error("167's stale report, with a 0.9977-day eclipsing period from before the period tests, is a binary")
	}
	if k := vetKind(r167); k != "" {
		t.Errorf("167's stale report gives kind %q, want none until vetted again", k)
	}
	o := Star{Source: app.DiscoverySource{ID: 167}, Report: r167}.OtherView(nil)
	if o.Waiting == "" || o.VetLine != "" || o.Period != 0 {
		t.Errorf("167 card %+v", o)
	}
}

func ebReport(p float64) *vetting.Report {
	return &vetting.Report{Suggestion: ebSuggestion(),
		Ephemeris: &period.Ephemeris{Period: p, Kind: period.KindEclipsing, Eclipses: []period.Eclipse{
			{Phase: 0, Width: 0.03, Shape: 1, Depth: map[string]float64{keyZTFr: 0.4}},
			{Phase: 0.5, Width: 0.03, Shape: 1, Depth: map[string]float64{keyZTFr: 0.2}}}},
		Groups: []vetting.Group{{ID: check.GroupSurveys, Checks: []check.Check{
			check.New(check.Echo, check.Pass, "stands out"), check.New(check.Compare, check.Pass, "none")}}}}
}

func TestEclipsingBinaryRules(t *testing.T) {
	t.Parallel()
	if !EclipsingBinary(ebReport(2.7)) {
		t.Fatal("a tested eclipsing binary verdict is not a binary")
	}
	echo := ebReport(0.9977)
	echo.Groups[0].Checks[0] = check.New(check.Echo, check.NotRun, "0.9977 days sits next to one day")
	if EclipsingBinary(echo) {
		t.Error("a daily-alias period nothing confirms is a binary")
	}
	echo.Suggestion.Kind = ""
	if EclipsingBinary(echo) {
		t.Error("a daily-alias period in a report without a kind is a binary")
	}
	tess := ebReport(0.9977)
	tess.Groups[0].Checks[0] = check.New(check.Echo, check.NotRun, "0.9977 days sits next to one day")
	tess.Groups[0].Checks = append(tess.Groups[0].Checks, check.New(check.TESS, check.Pass, "two dips on time"))
	if !EclipsingBinary(tess) {
		t.Error("a period next to one day that TESS shows is not a binary")
	}
	untested := ebReport(2.7)
	untested.Suggestion.Kind = ""
	untested.Groups = nil
	if EclipsingBinary(untested) {
		t.Error("a report from before the period tests is a binary")
	}
	other := ebReport(2.7)
	other.Suggestion.Kind = vetting.KindFades
	if EclipsingBinary(other) {
		t.Error("a fades verdict with an eclipsing fit is a binary")
	}
	unsure := ebReport(2.7)
	unsure.Suggestion = vetting.Suggestion{State: app.VetUnsure}
	if EclipsingBinary(unsure) {
		t.Error("an unsure verdict is a binary")
	}
	noKind := ebReport(2.7)
	noKind.Suggestion.Kind = ""
	if !EclipsingBinary(noKind) {
		t.Error("a tested real eclipsing verdict written before kinds were kept is not a binary")
	}
	for _, row := range []check.Check{
		check.New(check.ZTF, check.Fail, "The points in g the shallow dip are 0.01 +- 0.01 mag fainter, well short of the 0.40 mag the other bands show, against the period from the other surveys."),
		check.New(check.ATLAS, check.Fail, "The points in c the shallow dip are short of the depth the other bands show."),
		check.New(check.Double, check.Fail, "The deep dip is missing on even cycles, so the true period may be 102.9318 days."),
	} {
		dipper := ebReport(51.4659)
		dipper.Groups[0].Checks = append(dipper.Groups[0].Checks, check.New(check.Fold, check.Pass, "on time"), row)
		if EclipsingBinary(dipper) {
			t.Errorf("15523's star:18 report with %s failed is a binary", row.ID)
		}
	}
	single := ebReport(3.919735)
	single.Ephemeris.Eclipses = single.Ephemeris.Eclipses[:1]
	single.Groups[0].Checks = append(single.Groups[0].Checks, check.New(check.Double, check.Fail, "The dip is 0.30 mag deep on even cycles but 0.22 on odd cycles in r, so the true period may be 7.839470 days."))
	if !EclipsingBinary(single) {
		t.Error("one dip whose double check failed, which the facts double, is not a binary")
	}
	noKind.Groups[0].Checks[1] = check.New(check.Compare, check.Fail, "nearby stars")
	if EclipsingBinary(noKind) {
		t.Error("a verdict without a kind whose comparison failed is a binary")
	}
}

func TestOtherCardKinds(t *testing.T) {
	t.Parallel()
	rot := &vetting.Report{Suggestion: vetting.Suggestion{State: app.VetReal, Confidence: vetting.Medium, Kind: vetting.KindPeriodic},
		Ephemeris: &period.Ephemeris{Period: 4.318, PeriodErr: 0.002, Kind: period.KindSinusoidal},
		Groups:    []vetting.Group{{Checks: []check.Check{check.New(check.Echo, check.Pass, "stands out")}}}}
	o := Star{Source: app.DiscoverySource{ID: 167, Object: "Field"}, Report: rot, Book: app.BinaryBook{StarType: "rot"}}.OtherView(nil)
	if o.VetKind != vetting.KindPeriodic || o.Period != 4.318 || o.Confidence != vetting.Medium || o.StarType == nil || o.StarType.VSX != "ROT" || o.Waiting != "" {
		t.Errorf("rotating star card %+v", o)
	}
	fades := &vetting.Report{Suggestion: vetting.Suggestion{State: app.VetReal, Confidence: vetting.Low, Kind: vetting.KindFades}}
	o = Star{Source: app.DiscoverySource{ID: 15523}, Report: fades, Book: app.BinaryBook{StarType: "dipper"}}.OtherView(nil)
	if o.VetLine != "Fades now and then" || o.Period != 0 || o.StarType == nil || o.StarType.Words != "Dipper" {
		t.Errorf("dipper card %+v", o)
	}
}

// mixedDB stores a confirmed eclipsing binary and a confirmed star whose
// vet found an eclipsing fit but called it a star that fades now and
// then, each with facts, a timing and an open request.
func mixedDB(t *testing.T, site sky.Site, now time.Time) (*gorm.DB, int, int) {
	t.Helper()
	db := seasonalDB(t, site, now)
	var eb app.DiscoverySource
	if err := db.First(&eb).Error; err != nil {
		t.Fatal(err)
	}
	src := eb
	src.ID = 0
	if err := db.Create(&src).Error; err != nil {
		t.Fatal(err)
	}
	var vet app.DiscoveryVet
	if err := db.First(&vet, "source_id = ?", eb.ID).Error; err != nil {
		t.Fatal(err)
	}
	var rep vetting.Report
	if err := json.Unmarshal([]byte(vet.Report), &rep); err != nil {
		t.Fatal(err)
	}
	rep.SourceID = src.ID
	rep.Suggestion = vetting.Suggestion{State: app.VetReal, Kind: vetting.KindFades, Confidence: vetting.Medium}
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&app.DiscoveryVet{SourceID: src.ID, State: "done", Report: string(raw)}).Error; err != nil {
		t.Fatal(err)
	}
	var f app.BinaryFacts
	if err := db.First(&f, "source_id = ?", eb.ID).Error; err != nil {
		t.Fatal(err)
	}
	f.SourceID = src.ID
	for _, row := range []any{&f,
		&app.EclipseTiming{SourceID: eb.ID, Kind: KindPrimary, Predicted: now.AddDate(0, 0, -5), State: app.TimingGood},
		&app.EclipseTiming{SourceID: src.ID, Kind: KindPrimary, Predicted: now.AddDate(0, 0, -5), State: app.TimingGood},
		&app.EclipseRequest{SourceID: eb.ID, Kind: KindPrimary, Mid: now.Add(24 * time.Hour), State: app.EclipseSent, CreateCmd: "c1", Auto: true},
		&app.EclipseRequest{SourceID: src.ID, Kind: KindPrimary, Mid: now.Add(48 * time.Hour), State: app.EclipseWaiting, Auto: true},
	} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	return db, eb.ID, src.ID
}

func TestNonBinariesGetNoEclipseWork(t *testing.T) {
	t.Parallel()
	site := sky.Site{Latitude: 40, Longitude: 0}
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	db, ebID, otherID := mixedDB(t, site, now)
	svc := &Service{DB: db, Commands: noCommands{}, Now: func() time.Time { return now },
		Site:     func(context.Context) (sky.Site, error) { return site, nil },
		Snapshot: func(context.Context) (*planning.Snapshot, error) { return &planning.Snapshot{}, nil }}
	ctx := context.Background()
	bins, err := svc.Binaries(ctx)
	if err != nil || len(bins) != 1 || bins[0].Source.ID != ebID {
		t.Fatalf("binaries %d %v", len(bins), err)
	}
	others, err := svc.Others(ctx)
	if err != nil || len(others) != 1 || others[0].Source.ID != otherID {
		t.Fatalf("others %d %v", len(others), err)
	}
	if _, err := svc.RequestImaging(ctx, otherID, KindPrimary, now.Add(72*time.Hour)); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("requestEclipse on a non-binary: %v", err)
	}
	var before int64
	db.Model(&app.EclipseRequest{}).Where("source_id = ?", otherID).Count(&before)
	all := append(append([]Star{}, bins...), others...)
	if err := svc.autoRequests(ctx, all); err != nil {
		t.Fatal(err)
	}
	var after int64
	db.Model(&app.EclipseRequest{}).Where("source_id = ?", otherID).Count(&after)
	if after != before {
		t.Errorf("autoRequests asked for a non-binary: %d requests, had %d", after, before)
	}
	if _, err := svc.Kit(ctx, otherID, nil, nil); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("kit of a non-binary: %v", err)
	}
	if _, err := svc.Timings(ctx, otherID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("timings of a non-binary: %v", err)
	}
	plan, err := svc.Plan(ctx, 30, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range plan.Eclipses {
		if e.SourceID == otherID {
			t.Fatalf("the plan lists an eclipse of a non-binary: %+v", e)
		}
	}
	if _, err := svc.Update(ctx, otherID, BookChange{}); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("binary update of a non-binary: %v", err)
	}
	dip := "dipper"
	if st, err := svc.UpdateOther(ctx, otherID, BookChange{StarType: &dip}); err != nil || st.Book.StarType != dip {
		t.Errorf("star type not saved: %v", err)
	}
	bad := "EA"
	if _, err := svc.UpdateOther(ctx, otherID, BookChange{StarType: &bad}); !errors.Is(err, ErrStarType) {
		t.Errorf("unknown star type: %v", err)
	}
}

func TestPruneTakesAwayNonBinaryState(t *testing.T) {
	t.Parallel()
	site := sky.Site{Latitude: 40, Longitude: 0}
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	db, ebID, otherID := mixedDB(t, site, now)
	objs := &prefixObjects{}
	svc := &Service{DB: db, Objects: objs, Now: func() time.Time { return now }}
	ctx := context.Background()
	bins, err := svc.Binaries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.prune(ctx, bins); err != nil {
		t.Fatal(err)
	}
	count := func(m any, id int, extra ...any) int64 {
		var n int64
		q := db.Model(m).Where("source_id = ?", id)
		if len(extra) > 0 {
			q = q.Where(extra[0], extra[1:]...)
		}
		if err := q.Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count(&app.BinaryFacts{}, otherID) != 0 || count(&app.EclipseTiming{}, otherID) != 0 {
		t.Error("the non-binary's facts or timings linger")
	}
	if count(&app.EclipseRequest{}, otherID, "state = ?", app.EclipseCancel) != 1 {
		t.Error("the non-binary's open request was not cancelled")
	}
	if count(&app.BinaryFacts{}, ebID) != 1 || count(&app.EclipseTiming{}, ebID) != 1 || count(&app.EclipseRequest{}, ebID, "state = ?", app.EclipseSent) != 1 {
		t.Error("the binary's facts, timing or request changed")
	}
	if len(objs.removed) != 1 || objs.removed[0] != fmt.Sprintf("binaries/%d/", otherID) {
		t.Errorf("removed files %v", objs.removed)
	}
}

type prefixObjects struct{ removed []string }

func (*prefixObjects) Get(context.Context, string) ([]byte, error) {
	return nil, gorm.ErrRecordNotFound
}

func (*prefixObjects) Put(context.Context, string, []byte, string) error { return nil }

func (p *prefixObjects) RemovePrefix(_ context.Context, prefix string) error {
	p.removed = append(p.removed, prefix)
	return nil
}
