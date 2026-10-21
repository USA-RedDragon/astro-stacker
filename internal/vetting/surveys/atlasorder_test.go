package surveys

import (
	"slices"
	"strings"
	"testing"
)

func lineStar(id int64) Target {
	t := ic1396Star()
	t.GaiaID = id
	return t
}

func TestATLASLineServesBestPlaceFirst(t *testing.T) {
	t.Parallel()
	fake := &atlasFake{t: t, positions: map[string]int{"1": 0, "2": 1}}
	f, _ := testFetcher(t, fake)
	fake.url = f.IRSAURL
	f.ATLASToken = testToken
	f.ATLASMaxQueued = 2
	asks := []struct {
		id   int64
		prio Priority
	}{{11, 4}, {12, 3}, {13, 1}, {14, 2}, {15, 1}}
	for _, a := range asks {
		fe := f.ATLAS(WithPriority(t.Context(), a.prio), ic1396Field(), lineStar(a.id))
		if fe.State != Pending || !strings.Contains(fe.Note, "waiting for room") {
			t.Fatalf("star %d: %+v", a.id, fe)
		}
	}
	if fe := f.ATLAS(WithPriority(t.Context(), 1), ic1396Field(), lineStar(16)); !strings.Contains(fe.Note, "2 of our ATLAS tasks are queued and 2 stars wait before this one") {
		t.Fatalf("place note %q", fe.Note)
	}
	if fake.queued != 0 {
		t.Fatalf("queued %d with no room", fake.queued)
	}
	fake.positions = map[string]int{}
	if err := f.ATLASServe(t.Context()); err != nil {
		t.Fatal(err)
	}
	want := make([]string, 0, 6)
	want = append(want, "astro-stacker vetting 13", "astro-stacker vetting 15")
	if !slices.Equal(fake.posted, want) {
		t.Fatalf("queued %v, want %v", fake.posted, want)
	}
	fake.positions = map[string]int{"3": 0}
	if fe := f.ATLAS(WithPriority(t.Context(), 0), ic1396Field(), lineStar(17)); fe.Note != "queued at ATLAS" {
		t.Fatalf("an asked star waited behind the line: %+v", fe)
	}
	fake.positions = map[string]int{}
	f.ATLASMaxQueued = 3
	if err := f.ATLASServe(t.Context()); err != nil {
		t.Fatal(err)
	}
	want = append(want, "astro-stacker vetting 17", "astro-stacker vetting 16", "astro-stacker vetting 14", "astro-stacker vetting 12")
	if !slices.Equal(fake.posted, want) {
		t.Fatalf("queued %v, want %v", fake.posted, want)
	}
}

func TestATLASLineKeepsRoomForBetterStillToCome(t *testing.T) {
	t.Parallel()
	fake := &atlasFake{t: t, positions: map[string]int{}}
	f, _ := testFetcher(t, fake)
	fake.url = f.IRSAURL
	f.ATLASToken = testToken
	f.ATLASMaxQueued = 2
	f.ATLASAhead = func(p Priority) int {
		if p > 1 {
			return 2
		}
		return 0
	}
	fe := f.ATLAS(WithPriority(t.Context(), 4), ic1396Field(), lineStar(21))
	if fe.State != Pending || fake.queued != 0 || !strings.Contains(fe.Note, "waiting for room") {
		t.Fatalf("an artifact took room kept for better ones: %+v, queued %d", fe, fake.queued)
	}
	if fe := f.ATLAS(WithPriority(t.Context(), 1), ic1396Field(), lineStar(22)); fe.Note != "queued at ATLAS" {
		t.Fatalf("%+v", fe)
	}
	f.ATLASAhead = func(Priority) int { return 0 }
	if err := f.ATLASServe(t.Context()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"astro-stacker vetting 22", "astro-stacker vetting 21"}; !slices.Equal(fake.posted, want) {
		t.Fatalf("queued %v, want %v", fake.posted, want)
	}
}
