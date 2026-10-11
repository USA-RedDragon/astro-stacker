package stacking

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func fakeHorizons(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parse := func(k string) time.Time {
			v, err := time.Parse("2006-01-02 15:04", strings.Trim(r.URL.Query().Get(k), "'"))
			if err != nil {
				t.Errorf("%s: %v", k, err)
			}
			return v
		}
		from, to := parse("START_TIME"), parse("STOP_TIME")
		n := int(to.Sub(from)/time.Minute) + 1
		if n > 90024 {
			fmt.Fprintf(w, `{"error":"Projected output length (~%d) exceeds 90024 -- change step-size"}`, n)
			return
		}
		var b strings.Builder
		b.WriteString("$$SOE\n")
		for at := from; !at.After(to); at = at.Add(time.Minute) {
			fmt.Fprintf(&b, " %s, , , %.6f, 10.0,\n", at.Format("2006-Jan-02 15:04"), math.Mod(float64(at.Unix()/60)*0.001, 360))
		}
		b.WriteString("$$EOE\n")
		fmt.Fprintf(w, `{"result":%q}`, b.String())
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCometEphemerisOverMonths(t *testing.T) {
	t.Parallel()
	srv := fakeHorizons(t)
	var times []time.Time
	for d := time.Date(2026, 8, 1, 3, 0, 0, 0, time.UTC); d.Before(time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)); d = d.AddDate(0, 0, 7) {
		for i := range 4 {
			times = append(times, d.Add(time.Duration(i)*5*time.Minute+30*time.Second))
		}
	}
	e, err := cometEphemeris(context.Background(), srv.URL, "C/2025 R2", times, -99.4, 31.5, 473)
	if err != nil {
		t.Fatal(err)
	}
	for _, at := range times {
		ra, _, ok := e.at(at)
		want := math.Mod((float64(at.Unix())/60)*0.001, 360)
		if !ok || math.Abs(ra-want) > 1e-6 {
			t.Errorf("at %v: %v %v, want %v", at, ra, ok, want)
		}
	}
}

func TestCometEphemerisContinuousRun(t *testing.T) {
	t.Parallel()
	srv := fakeHorizons(t)
	var times []time.Time
	for at := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC); at.Before(time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC)); at = at.Add(2*time.Minute + 30*time.Second) {
		times = append(times, at)
	}
	e, err := cometEphemeris(context.Background(), srv.URL, "C/2025 R2", times, -99.4, 31.5, 473)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(e.times); i++ {
		if !e.times[i].After(e.times[i-1]) {
			t.Fatalf("table out of order at %v", e.times[i])
		}
	}
	for _, at := range times {
		if _, _, ok := e.at(at); !ok {
			t.Errorf("no position at %v", at)
		}
	}
}
