package surveys

import (
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

func wiseServer(t *testing.T, queries *atomic.Int32) http.Handler {
	t.Helper()
	neo := string(readFixture(t, "neowise_ic1396.csv"))
	neo += "323.2752700,58.6339000,56651.63163407,11.000,0.020,11.100,0.030,5,0.5,81.387,0000,0000,1,0,44570a,251\n"
	all := readFixture(t, "allwise_ic1396.csv")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/TAP/sync" || r.ParseForm() != nil || r.PostForm.Get("FORMAT") != "csv" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		queries.Add(1)
		q := r.PostForm.Get("QUERY")
		switch {
		case strings.Contains(q, "FROM neowiser_p1bs_psd") && strings.Contains(q, "qual_frame"):
			_, _ = w.Write([]byte(neo))
		case strings.Contains(q, "FROM allwise_p3as_mep") && strings.Contains(q, "w1mpro_ep"):
			_, _ = w.Write(all)
		default:
			_, _ = w.Write([]byte(`<?xml version="1.0"?><VOTABLE><RESOURCE><INFO name="QUERY_STATUS" value="ERROR">bad table</INFO></RESOURCE></VOTABLE>`))
		}
	})
}

func TestNEOWISE(t *testing.T) {
	t.Parallel()
	var queries atomic.Int32
	f, _ := testFetcher(t, wiseServer(t, &queries))
	ctx := t.Context()
	fe := f.NEOWISE(ctx, ic1396Field(), ic1396Star())
	if fe.State != Done || len(fe.Series) != 2 {
		t.Fatalf("%+v", fe)
	}
	w1 := fe.Series[0]
	if w1.Band != "W1" || w1.Survey != lc.NEOWISE || w1.Relative {
		t.Fatalf("%+v", w1)
	}
	if n := len(w1.Points); n != 40+15 {
		t.Errorf("%d W1 points, want 40 good NEOWISE frames and 15 AllWISE", n)
	}
	for _, p := range w1.Points {
		if p.Mag > 10.9 {
			t.Errorf("kept the farther detection in a shared frame: %+v", p)
		}
	}
	if w1.Dilution >= 1 || !strings.Contains(w1.Note, "approximation") {
		t.Errorf("dilution %v note %q", w1.Dilution, w1.Note)
	}
	before := queries.Load()
	if again := f.NEOWISE(ctx, ic1396Field(), ic1396Star()); again.State != Done || queries.Load() != before {
		t.Errorf("second read asked IRSA again")
	}
}

func TestNEOWISETAPError(t *testing.T) {
	t.Parallel()
	f, _ := testFetcher(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<?xml version="1.0"?><VOTABLE><RESOURCE><INFO name="QUERY_STATUS" value="ERROR">TransientFault: Job ran but timed out.</INFO></RESOURCE></VOTABLE>`))
	}))
	fe := f.NEOWISE(t.Context(), ic1396Field(), ic1396Star())
	if fe.State != Failed || !strings.Contains(fe.Note, "timed out") {
		t.Fatalf("%+v", fe)
	}
}
