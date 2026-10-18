package vetting

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/discoverysource"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/ownimage"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
	"github.com/klauspost/compress/zstd"
)

// auditDir holds the 2026-10-17 audit's cases; see the period package's
// audit_test.go.
const auditDir = "period/testdata/audit"

type auditCase struct {
	Key       string            `json:"key"`
	Want      string            `json:"want"`
	Ephemeris *period.Ephemeris `json:"ephemeris"`
}

func readAuditCases(t *testing.T) []auditCase {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(auditDir, "cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cs []auditCase
	if err := json.Unmarshal(b, &cs); err != nil {
		t.Fatal(err)
	}
	return cs
}

// readAuditSeries reads a case's light curves: the star's, one series per
// survey band, and each comparison star's.
func readAuditSeries(t *testing.T, key string) ([]lc.Series, [][]lc.Series) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(auditDir, key+".csv.zst"))
	if err != nil {
		t.Fatal(err)
	}
	dec, err := zstd.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	body, err := io.ReadAll(dec)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(bytes.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	type sk struct{ who, survey, band string }
	by := map[sk]*lc.Series{}
	var order []sk
	for _, r := range rows[1:] {
		k := sk{r[0], r[1], r[2]}
		s, ok := by[k]
		if !ok {
			s = &lc.Series{Survey: k.survey, Band: k.band, Dilution: 1, Relative: k.survey == lc.TESS, Deblended: k.survey == lc.TESS}
			by[k] = s
			order = append(order, k)
		}
		bjd, err1 := strconv.ParseFloat(r[3], 64)
		mag, err2 := strconv.ParseFloat(r[4], 64)
		if err := errors.Join(err1, err2); err != nil {
			t.Fatal(err)
		}
		e, _ := strconv.ParseFloat(r[5], 64)
		var src int64
		if len(r) > 6 {
			src, _ = strconv.ParseInt(r[6], 10, 64)
		}
		s.Points = append(s.Points, lc.Point{BJD: bjd + 2450000, Mag: mag, Err: e, Src: src})
	}
	var target []lc.Series
	comps := map[string][]lc.Series{}
	var names []string
	for _, k := range order {
		if k.who == "t" {
			target = append(target, *by[k])
			continue
		}
		if _, ok := comps[k.who]; !ok {
			names = append(names, k.who)
		}
		comps[k.who] = append(comps[k.who], *by[k])
	}
	slices.Sort(names)
	out := make([][]lc.Series, 0, len(names))
	for _, n := range names {
		out = append(out, comps[n])
	}
	return target, out
}

// auditVerdict is the vet's suggestion for a case from its survey data
// alone, at the period the vet claimed (or the one its search finds),
// with nothing from the user's own frames or the catalogs.
func auditVerdict(t *testing.T, c auditCase, flags ownimage.Flags, known Known) Suggestion {
	t.Helper()
	series, comps := readAuditSeries(t, c.Key)
	opts := period.Options{Comparisons: func() [][]lc.Series { return comps }}
	var pr *period.Result
	var err error
	if c.Ephemeris != nil {
		pr, err = period.AnalyzeAt(series, c.Ephemeris, opts)
	} else {
		pr, err = period.Analyze(series, opts)
	}
	if err != nil && !errors.Is(err, period.ErrNoPeriod) {
		t.Fatal(err)
	}
	if pr != nil && !hasPeriod(pr.Ephemeris) {
		pr.Ephemeris = nil
	}
	sum := discoverysource.Summary{Source: app.DiscoverySource{Kind: app.DiscoveryKindChange}}
	groups := []Group{{ID: check.GroupSurveys, Checks: surveyRows(fetches{}, pr, nil)}}
	return suggest(sum, flags, nil, known, check.New(check.Stays, check.Pass, ""), fetches{}, pr, nil, groups, change{})
}

// TestAuditVerdicts is the 2026-10-17 audit as known answers for the
// suggested verdict: the 19 confirmed eclipsing binaries and the new
// 16040 and 17511 come out real; 34867, periodic but drifting, unsure;
// none of the false or echoed periods real; and the catalogued stars with
// right periods (9124, whose folded eclipses drift by an hour in one
// season, and 33898) real or unsure, the catalogs naming them in the vet.
func TestAuditVerdicts(t *testing.T) {
	t.Parallel()
	for _, c := range readAuditCases(t) {
		t.Run(c.Key, func(t *testing.T) {
			t.Parallel()
			s := auditVerdict(t, c, ownimage.Flags{}, Known{})
			t.Logf("%s: %s, %s (%s): %s", c.Key, s.State, s.Say, s.Confidence, s.Why)
			switch c.Want {
			case "real":
				if s.State != app.VetReal {
					t.Errorf("want real, got %s: %s", s.State, s.Say)
				}
			case "real-period":
				if s.State != app.VetReal && s.State != app.VetUnsure {
					t.Errorf("want real or unsure, got %s: %s", s.State, s.Say)
				}
			case "drifts":
				if s.State != app.VetUnsure || !strings.Contains(s.Say, "drifts") {
					t.Errorf("want unsure, drifting, got %s: %s", s.State, s.Say)
				}
			default:
				if s.State == app.VetReal {
					t.Errorf("a false period came out real: %s", s.Say)
				}
			}
		})
	}
}

// TestAuditExpectedVariability is failure mode A7: SIMBAD's young stars
// and supergiants whose period fails the echo tests are known, irregular
// variables; the M31 binary SIMBAD lists as a red supergiant candidate
// keeps its eclipses. 17511's own frames had a hot pixel under the spot,
// which does not decide a survey period.
func TestAuditExpectedVariability(t *testing.T) {
	t.Parallel()
	byKey := map[string]auditCase{}
	for _, c := range readAuditCases(t) {
		byKey[c.Key] = c
	}
	for _, x := range []struct {
		key, expected string
		flags         ownimage.Flags
		want          string
	}{
		{"33903", expectYoung, ownimage.Flags{}, app.VetKnown},
		{"41958", expectYoung, ownimage.Flags{}, app.VetKnown},
		{"35873", expectSupergiant, ownimage.Flags{}, app.VetKnown},
		{"235", expectSupergiant, ownimage.Flags{}, app.VetKnown},
		{"ctlM31", expectSupergiant, ownimage.Flags{}, app.VetReal},
		{"17511", "", ownimage.Flags{HotPixel: true}, app.VetReal},
	} {
		s := auditVerdict(t, byKey[x.key], x.flags, Known{Expected: x.expected, ExpectedName: "a SIMBAD entry"})
		if s.State != x.want {
			t.Errorf("%s: want %s, got %s: %s; %s", x.key, x.want, s.State, s.Say, s.Why)
		}
	}
}
