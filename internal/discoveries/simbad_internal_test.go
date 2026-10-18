package discoveries

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// simbadHaro is SIMBAD's TAP answer around candidate 14573 (Orion H-alpha,
// 2026-10-06): Haro 4-411, an Orion variable VSX lacks.
const simbadHaro = "main_id\tra\tdec\totype\n\"Haro 4-411\"\t84.19832352917\t-5.6086791076099995\t\"Or*\"\n\"M31N 2008-12a\"\t84.2\t-5.6087\t\"No*\"\n"

func TestSIMBADVariableMakesAChangeKnown(t *testing.T) {
	t.Parallel()
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		query = r.PostForm.Get("QUERY")
		_, _ = w.Write([]byte(simbadHaro))
	}))
	defer srv.Close()
	vs, err := (&Catalogs{SIMBADURL: srv.URL}).SIMBAD(context.Background(), 84.2, -5.6, 1.5)
	if err != nil || len(vs) != 2 || vs[0].Name != "Haro 4-411" || vs[0].Type != "Or*" || vs[0].Catalog != catalogSIMBAD {
		t.Fatalf("%+v %v", vs, err)
	}
	if !strings.Contains(query, "otype = 'V*..'") || !strings.Contains(query, "CIRCLE('ICRS', 84.200000, -5.600000, 1.5000)") {
		t.Errorf("query %s", query)
	}
	res := &Result{Candidates: []Candidate{{RA: 84.19834, Dec: -5.60871, Sign: 1, SNR: 25.2}}}
	ls := classifyChanges(context.Background(), t, res, Known{Variables: vs[:1]})
	if ls[0].Class != ClassKnownVariable || ls[0].Matches[0].Catalog != catalogSIMBAD {
		t.Errorf("Haro 4-411 changed: %s %+v", ls[0].Class, ls[0].Matches)
	}
	ls = classifyChanges(context.Background(), t, res, Known{Variables: vs[1:]})
	if ls[0].Class == ClassKnownVariable {
		t.Error("a brightening at a past nova may be the nova erupting again, not a known variable")
	}
	asked := 0
	alerts := func(_ context.Context, _, _, _ float64) ([]Alert, []string, []string) {
		asked++
		return []Alert{{Source: sourceTNS, ID: "AT 2026xyz"}}, []string{sourceTNS}, nil
	}
	ls, _, checked, _ := Classify(context.Background(), res, Known{Variables: vs[1:]}, alerts)
	if asked != 1 || ls[0].Class != ClassKnownTransient || len(checked) != 1 {
		t.Errorf("a change at a past nova is still checked with the alert brokers: asked %d, class %s", asked, ls[0].Class)
	}
	far := vs[0]
	far.Dec += 3.0 / 3600
	ls = classifyChanges(context.Background(), t, res, Known{Variables: []Variable{far}})
	if ls[0].Class == ClassKnownVariable {
		t.Error("a SIMBAD variable 3\" away is not the change: M 31's disc has one within 5\" of a twentieth of its area")
	}
}

// TestSIMBADMatchRadius replays 57613 (Triangulum, 2025-12-12), 2.12"
// from the Mira M33SSS J013357.42+303958.7, and the real nova AT 2025jub,
// 2.90" from the pulsating star [MSH2004] 33064.
func TestSIMBADMatchRadius(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, typ string
		sep       float64
		known     bool
	}{
		{"M33SSS J013357.42+303958.7", "Mi*", 2.12, true},
		{"[MSH2004] 33064", "Pu*", 2.90, false},
	} {
		v := Variable{Catalog: catalogSIMBAD, Name: tc.name, Type: tc.typ, RA: 23.4892, Dec: 30.6663 + tc.sep/3600}
		res := &Result{Candidates: []Candidate{{RA: 23.4892, Dec: 30.6663, Sign: 1, SNR: 9, PresentIn: 8, SubsTested: 8}}}
		ls := classifyChanges(context.Background(), t, res, Known{Variables: []Variable{v}})
		if known := ls[0].Class == ClassKnownVariable; known != tc.known {
			t.Errorf("%s %.2f\" away: %s %+v", tc.name, tc.sep, ls[0].Class, ls[0].Matches)
		}
	}
}

func TestSIMBADErrorsAndCache(t *testing.T) {
	t.Parallel()
	if _, err := ParseSIMBAD([]byte(`<VOTABLE><RESOURCE><INFO name="QUERY_STATUS" value="ERROR">Incorrect ADQL query</INFO></RESOURCE></VOTABLE>`)); err == nil || !strings.Contains(err.Error(), "Incorrect ADQL query") {
		t.Errorf("error %v", err)
	}
	var up atomic.Bool
	up.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !up.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(simbadHaro))
	}))
	defer srv.Close()
	ctx := context.Background()
	r := New(nil, nil, &memStore{m: map[string][]byte{}}, &Catalogs{SIMBADURL: srv.URL}, Options{})
	now := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	if vs, err := r.simbad(ctx, "Orion", 84, -5.5, 1); err != nil || len(vs) != 2 {
		t.Fatalf("%v %v", vs, err)
	}
	up.Store(false)
	now = now.Add(60 * 24 * time.Hour)
	if vs, err := r.simbad(ctx, "Orion", 84, -5.5, 1); err != nil || len(vs) != 2 {
		t.Errorf("with SIMBAD down an older answer is used: %v %v", vs, err)
	}
	if _, err := r.simbad(ctx, "Andromeda", 10, 41, 1); err == nil {
		t.Error("with SIMBAD down and no older answer the lookup fails")
	}
}

// classifyChanges classifies a result with changes and no tracks.
func classifyChanges(ctx context.Context, t *testing.T, res *Result, k Known) []Labelled {
	t.Helper()
	ls, tracks, _, _ := Classify(ctx, res, k, nil)
	if len(tracks) != 0 {
		t.Fatalf("%d tracks", len(tracks))
	}
	return ls
}
