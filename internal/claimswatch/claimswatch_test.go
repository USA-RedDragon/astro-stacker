package claimswatch_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/claimswatch"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/testdb"
	"github.com/USA-RedDragon/astro-stacker/internal/tnsmirror"
)

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func gaiaEB() claimswatch.Star {
	return claimswatch.Star{SourceID: 1, Name: "Gaia DR3 1859904118325304192", GaiaID: 1859904118325304192, RA: 312.3053, Dec: 32.10498}
}

func garlic() claimswatch.Star {
	return claimswatch.Star{SourceID: 2289, Name: "Gaia DR3 2012823714478462848", GaiaID: 2012823714478462848, TwoMASS: "2MASS J23581952+6229005",
		RA: 359.581384, Dec: 62.483537}
}

const newTitle = "new"

func TestVSXKnownEntryAndEmpty(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case q.Get("view") == "detail.top":
			_, _ = w.Write(read(t, "vsx_detail.html"))
		case strings.HasPrefix(q.Get("ra"), "312."):
			_, _ = w.Write(read(t, "vsx_hit.json"))
		default:
			_, _ = w.Write(read(t, "vsx_empty.json"))
		}
	}))
	defer srv.Close()
	v := &claimswatch.VSX{BaseURL: srv.URL, Discoverer: func(context.Context) string { return "J. McSwain" }}
	out, err := v.Check(context.Background(), []claimswatch.Star{gaiaEB(), garlic()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(out.Found[2289]); n != 0 {
		t.Fatalf("2289 has %d VSX entries in the recorded empty answer", n)
	}
	got := out.Found[1]
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	m := got[0]
	if m.Key != "oid:7831690" || m.Title != "Gaia DR3 1859904118325304192" || m.Own {
		t.Errorf("entry %+v", m)
	}
	if !strings.Contains(m.Detail, `discoverer "Gaia collaboration"`) || !strings.Contains(m.Detail, "period 0.308141 d") {
		t.Errorf("detail %q", m.Detail)
	}
	want := time.Date(2025, 1, 26, 18, 35, 0, 0, time.UTC)
	if m.Dated == nil || !m.Dated.Equal(want) {
		t.Errorf("added %v, want %v", m.Dated, want)
	}
}

func TestVSXOwnEntry(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("view") == "api.list" {
			_, _ = w.Write(read(t, "vsx_hit.json"))
		}
	}))
	defer srv.Close()
	v := &claimswatch.VSX{BaseURL: srv.URL, Discoverer: func(context.Context) string { return "gaia COLLABORATION" }}
	out, err := v.Check(context.Background(), []claimswatch.Star{gaiaEB()}, nil)
	if err != nil || len(out.Found[1]) != 1 || !out.Found[1][0].Own {
		t.Fatalf("own entry not marked: %+v %v", out, err)
	}
}

func TestSIMBADRecorded(t *testing.T) {
	t.Parallel()
	rows, err := claimswatch.ParseSIMBADRefs(read(t, "simbad_hit.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	ms := claimswatch.SIMBADMentions(gaiaEB(), rows)
	if len(ms) != 2 {
		t.Fatalf("got %d mentions, want object and paper: %+v", len(ms), ms)
	}
	if ms[0].Key != "obj:Gaia DR3 1859904118325304192" || !strings.Contains(ms[0].Detail, "EB*") {
		t.Errorf("object %+v", ms[0])
	}
	if ms[1].Key != "bib:2023A&A...674A..16M" || ms[1].Title != "2023A&A...674A..16M (2023)" || ms[1].Dated != nil {
		t.Errorf("paper %+v", ms[1])
	}
	empty, err := claimswatch.ParseSIMBADRefs(read(t, "simbad_empty.tsv"))
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty answer: %v %v", empty, err)
	}
	if _, err := claimswatch.ParseSIMBADRefs([]byte(`<INFO name="QUERY_STATUS" value="ERROR">Incorrect ADQL query</INFO>`)); err == nil {
		t.Fatal("an error answer was read as empty")
	}
}

func TestArXivRecorded(t *testing.T) {
	t.Parallel()
	ms, err := claimswatch.ParseArXiv(read(t, "arxiv_hit.xml"))
	if err != nil || len(ms) != 1 {
		t.Fatalf("hit: %v %v", ms, err)
	}
	if !strings.HasPrefix(ms[0].Key, "arxiv:") || ms[0].Dated == nil || ms[0].URL == "" {
		t.Errorf("mention %+v", ms[0])
	}
	ms, err = claimswatch.ParseArXiv(read(t, "arxiv_empty.xml"))
	if err != nil || len(ms) != 0 {
		t.Fatalf("empty: %v %v", ms, err)
	}
}

func TestGaiaAlertsRecorded(t *testing.T) {
	t.Parallel()
	rows, err := claimswatch.ParseGaiaAlerts(read(t, "gaia_alerts.csv"))
	if err != nil || len(rows) != 2 {
		t.Fatalf("%v %v", rows, err)
	}
	if rows[0].Name != "Gaia25aeh" || rows[0].RA != 312.55124 || rows[0].Published == nil ||
		!rows[0].Published.Equal(time.Date(2025, 1, 16, 17, 56, 9, 0, time.UTC)) {
		t.Errorf("row %+v", rows[0])
	}
}

func TestATelRecorded(t *testing.T) {
	t.Parallel()
	if n := claimswatch.NewestATel(read(t, "atel.rss")); n != 18114 {
		t.Errorf("newest telegram %d, want 18114", n)
	}
	a := claimswatch.ParseATel(18110, read(t, "atel_18110.html"))
	if a.Date == nil || !a.Date.Equal(time.Date(2026, 10, 19, 0, 46, 0, 0, time.UTC)) {
		t.Errorf("date %v", a.Date)
	}
	sn := claimswatch.Star{SourceID: 9, RA: (3 + 33.0/60 + 35.898/3600) * 15, Dec: -(27 + 45.0/60 + 51.94/3600)}
	if how, ok := a.Match(sn, 10); !ok || !strings.Contains(how, "position") {
		t.Errorf("AT2026abft's position not matched: %q", how)
	}
	if _, ok := a.Match(garlic(), 10); ok {
		t.Error("2289 matched a telegram that does not name it")
	}
}

func TestVizieRRecorded(t *testing.T) {
	t.Parallel()
	tabs, err := claimswatch.ParseVizieRTables(read(t, "vizier_tables.tsv"))
	if err != nil || len(tabs) != 4 || tabs[2].Name != "B/vsx/vsx" {
		t.Fatalf("%+v %v", tabs, err)
	}
	if n, sep := claimswatch.ParseVizieRCone(read(t, "vizier_cone_hit.tsv")); n != 1 || sep != 0.01 {
		t.Errorf("hit %d %v", n, sep)
	}
	if n, _ := claimswatch.ParseVizieRCone(read(t, "vizier_cone_empty.tsv")); n != 0 {
		t.Errorf("empty answer has %d rows", n)
	}
}

func TestADSNeedsKey(t *testing.T) {
	t.Parallel()
	a := &claimswatch.ADS{Token: func() string { return "" }}
	if a.Off() == "" {
		t.Fatal("ADS without a key should say why it is not checked")
	}
}

type fakePlace struct {
	where string
	found map[int][]claimswatch.Mention
	err   error
}

func (f *fakePlace) Where() string { return f.where }
func (f *fakePlace) Off() string   { return "" }
func (f *fakePlace) Check(context.Context, []claimswatch.Star, claimswatch.State) (claimswatch.Outcome, error) {
	return claimswatch.Outcome{Found: f.found, Failed: map[int]string{}}, f.err
}

func TestBaselineThenNew(t *testing.T) {
	t.Parallel()
	db := testdb.SQLite(t, "claims")
	if err := db.AutoMigrate(&app.ClaimMention{}, &app.ClaimCheck{}, &app.ClaimWatchState{}); err != nil {
		t.Fatal(err)
	}
	rec := claimswatch.Record{Name: "Zenodo", Published: time.Date(2026, 10, 21, 0, 12, 24, 0, time.UTC), Sources: []int{2289}}
	added := time.Date(2025, 1, 26, 18, 35, 0, 0, time.UTC)
	place := &fakePlace{where: claimswatch.WhereVSX, found: map[int][]claimswatch.Mention{2289: {{Key: "oid:1", Title: "old", Dated: &added}}}}
	now := time.Date(2026, 10, 25, 8, 10, 0, 0, time.UTC)
	s := &claimswatch.Service{DB: db, Places: []claimswatch.Place{place}, Records: []claimswatch.Record{rec}, Now: func() time.Time { return now },
		Stars: func(context.Context) ([]claimswatch.Star, error) { return []claimswatch.Star{garlic()}, nil }}
	ctx := context.Background()
	if err := s.Check(ctx); err != nil {
		t.Fatal(err)
	}
	later := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	place.found[2289] = append(place.found[2289], claimswatch.Mention{Key: "oid:2", Title: newTitle, Dated: &later})
	now = now.AddDate(0, 0, 7)
	if err := s.Check(ctx); err != nil {
		t.Fatal(err)
	}
	sum, err := s.Summary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sum.NewCount != 1 || sum.Record == nil || sum.Record.Name != "Zenodo" || len(sum.PerStar) != 1 {
		t.Fatalf("summary %+v", sum)
	}
	st := map[string]string{}
	var newID int
	for _, m := range sum.PerStar[0].Mentions {
		st[m.Title] = m.Status
		if m.Title == newTitle {
			newID = m.ID
		}
	}
	if st["old"] != claimswatch.StatusKnown || st[newTitle] != claimswatch.StatusNew {
		t.Fatalf("statuses %v", st)
	}
	found, err := s.FoundBetween(ctx, now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil || len(found) != 1 || found[0].Title != newTitle || found[0].Record == nil {
		t.Fatalf("found %+v %v", found, err)
	}
	if _, err := s.Mark(ctx, newID, app.ClaimMarkCiting); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Mark(ctx, newID, "bogus"); err == nil {
		t.Fatal("a bogus mark was taken")
	}
	sum, _ = s.Summary(ctx)
	if sum.NewCount != 0 {
		t.Fatalf("a mention marked citing still counts as new: %d", sum.NewCount)
	}
}

func TestFirstCheckDatedAfterRecord(t *testing.T) {
	t.Parallel()
	rec := &claimswatch.Record{Published: time.Date(2026, 10, 21, 0, 12, 24, 0, time.UTC)}
	after := time.Date(2026, 10, 21, 3, 0, 0, 0, time.UTC)
	if s := claimswatch.StatusOf(app.ClaimMention{Baseline: true, Dated: &after}, rec); s != claimswatch.StatusFirstAfter {
		t.Fatalf("status %q", s)
	}
}

func TestNextIsSundayThreeCentral(t *testing.T) {
	t.Parallel()
	loc, _ := time.LoadLocation("America/Chicago")
	s := &claimswatch.Service{Loc: loc, Weekday: time.Sunday, Hour: 3}
	got := s.NextAfter(time.Date(2026, 10, 21, 15, 0, 0, 0, time.UTC)).In(loc)
	if got.Weekday() != time.Sunday || got.Hour() != 3 || got.Day() != 25 {
		t.Fatalf("next %v", got)
	}
}

func TestTNSFromMirror(t *testing.T) {
	t.Parallel()
	ix := tnsmirror.NewIndex([]tnsmirror.Object{{ObjID: 7, Prefix: "AT", Name: "2026abft", RA: 53.399575, Dec: -27.764428,
		Discovered: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}}, "20261021", time.Now())
	p := &claimswatch.TNS{Index: func() *tnsmirror.Index { return ix }}
	near := claimswatch.Star{SourceID: 5, RA: 53.3996, Dec: -27.7644}
	out, err := p.Check(context.Background(), []claimswatch.Star{near, garlic()}, nil)
	if err != nil || len(out.Found[5]) != 1 || len(out.Found[2289]) != 0 || out.Found[5][0].Title != "AT 2026abft" {
		t.Fatalf("%+v %v", out, err)
	}
}
