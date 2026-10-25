package vetting

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/discoverysource"
	"github.com/USA-RedDragon/astro-stacker/internal/starcat"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/ownimage"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
	"github.com/klauspost/compress/gzip"
)

// auditGapMirrors mirrors the real CDS records of the 2026-10-24 audit's
// missing catalogues, from starcat's zstd fixtures, as the vet reads them.
func auditGapMirrors(t *testing.T) Mirrors {
	t.Helper()
	ctx := context.Background()
	m := Mirrors{Variables: map[string]*starcat.Variables{}}
	for _, c := range []*starcat.VarCatalog{starcat.LAMOSTPeriodic(), starcat.LAMOSTVariable(), starcat.GaiaLargeAmplitude(), starcat.TESSEBCandidates()} {
		b, err := os.ReadFile("../starcat/testdata/" + c.Name + ".dat.zst")
		if err != nil {
			t.Fatal(err)
		}
		raw, err := unzstd(b)
		if err != nil {
			t.Fatal(err)
		}
		var gz strings.Builder
		w := gzip.NewWriter(&gz)
		_, _ = w.Write(raw)
		_ = w.Close()
		lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
		lrecl := map[string]int{"lamost-per": 327, "lamost-var": 536, "gaia-lav": 268, "tess-eb-cand": 38}[c.Name]
		readme := fmt.Sprintf("File Summary:\n FileName  Lrecl  Records  Explanations\nReadMe  80  .  This file\n%s  %d  %d  Variables\n", c.File, lrecl, len(lines))
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch strings.TrimPrefix(r.URL.Path, "/") {
			case "ReadMe":
				fmt.Fprint(w, readme)
			case c.File + ".gz":
				fmt.Fprint(w, gz.String())
			default:
				http.NotFound(w, r)
			}
		}))
		bucket := &starcat.MemBucket{}
		err = c.Mirror(ctx, starcat.MirrorOptions{Bucket: bucket, Prefix: c.Prefix, BaseURL: srv.URL + "/"})
		srv.Close()
		if err != nil {
			t.Fatal(err)
		}
		m.Variables[c.Name] = starcat.NewVariables(bucket, c.Prefix)
	}
	return m
}

// TestLAMOSTPeriodicListsSource28305 is source 28305, Gaia DR3
// 171049722563343232, which the vet called new and unlisted: Qiao 2024
// lists it as a ROT star of 11.32 days, Xu 2022 and Mowlavi 2021 flag its
// scatter. Its own 3.833-day cycle stands up in the survey data, so the
// vet must call it a known variable with its period revised, and leave it
// "Already known" when its period agrees with the list.
func TestLAMOSTPeriodicListsSource28305(t *testing.T) {
	t.Parallel()
	m := auditGapMirrors(t)
	var k Known
	ch := surveyVarCheck(context.Background(), m, 62.06072571986, 34.86016808858, 171049722563343232, &k)
	if ch.Status != check.Fail {
		t.Fatalf("got %+v", ch)
	}
	for _, want := range []string{
		"Qiao 2024's LAMOST periodic variables lists it as a spotted, rotating star repeating every 11.31957 days",
		"Xu 2022 lists it as a likely LAMOST variable",
		"Mowlavi 2021 lists it among Gaia DR2's large amplitude variables",
		"(amplitude measure 0.153)",
	} {
		if !strings.Contains(ch.Evidence, want) {
			t.Errorf("evidence %q lacks %q", ch.Evidence, want)
		}
	}
	if !strings.HasPrefix(k.Listed, "Qiao 2024") || k.ListedPeriod != 11.319572 {
		t.Fatalf("listed %q at %v days", k.Listed, k.ListedPeriod)
	}
	sum := discoverysource.Summary{Source: app.DiscoverySource{Kind: app.DiscoveryKindChange}}
	groups := []Group{{ID: check.GroupSurveys, Checks: []check.Check{
		check.New(check.ZTF, check.Pass, "Rises and falls by 0.07 mag in r every 3.833 days."),
		check.New(check.Fold, check.Pass, ""), check.New(check.Echo, check.Pass, ""), check.New(check.Compare, check.Pass, ""),
	}}}
	verdict := func(p float64) Suggestion {
		pr := &period.Result{Ephemeris: &period.Ephemeris{Period: p, Kind: period.KindSinusoidal},
			Robust: &period.Robust{Stable: true, CompPass: true, Real: true, Comparisons: 5}}
		return suggest(sum, ownimage.Flags{}, nil, k, check.New(check.Stays, check.Pass, ""), fetches{}, pr, nil, groups, change{})
	}
	s := verdict(3.8339)
	if s.State != app.VetKnown || !strings.HasPrefix(s.Say, "Known variable, period revised") ||
		!strings.Contains(s.Why, "repeat every 3.8339 days instead of 11.31957") {
		t.Errorf("3.833 days: %+v", s)
	}
	if s := verdict(11.32); s.State != app.VetKnown || s.Say != sayAlreadyKnown {
		t.Errorf("11.32 days: %+v", s)
	}
}

// TestKostovCandidateIsDisclosed is source 76400, Gaia DR3
// 2013040936747919488, a new eclipsing binary that is TIC 470756157 in
// Kostov 2025's list of unvetted TESS binary candidates: the vet must say
// so without counting the star as known.
func TestKostovCandidateIsDisclosed(t *testing.T) {
	t.Parallel()
	m := auditGapMirrors(t)
	var k Known
	ch := surveyVarCheck(context.Background(), m, 357.90165239696, 62.43681258338, 2013040936747919488, &k)
	want := "Listed as an unvetted TESS eclipsing binary candidate (TIC 470756157 in Kostov 2025)"
	if ch.Status != check.Pass || k.Listed != "" || !strings.Contains(ch.Evidence, want) {
		t.Fatalf("got %+v, listed %q", ch, k.Listed)
	}
	if strings.Contains(ch.Evidence, "Mowlavi") {
		t.Errorf("a Mowlavi entry 30 arcsec away named: %q", ch.Evidence)
	}
}
