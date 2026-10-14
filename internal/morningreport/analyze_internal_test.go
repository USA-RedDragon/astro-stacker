package morningreport

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/USA-RedDragon/astro-stacker/internal/sky"
)

// at is a time on the morning of 2026-10-14, US Central daylight time.
func at(clock string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, "2026-10-14T"+clock+"-05:00")
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

func nina(clock, level, file, msg string) Line {
	return Line{T: at(clock), Text: "2026-10-14T" + clock + "|" + level + "|" + file + "|Run|1|" + msg}
}

// Lines as NINA logged them on the night of 2026-10-13: the flap open, an
// unsafe reading at 02:54:27 that waited for the exposure and closed the
// flap at 03:03:14, and a NINA restart with the flap open.
func nightLines() []Line {
	return []Line{
		nina("00:20:00.0000", "INFO", "ApplicationVM.cs", "Application shutting down"),
		nina("00:20:01.0000", "INFO", "SafetyMonitorVM.cs", "SafetyMonitorInfo state changed to Unsafe"),
		nina("00:20:01.1000", "INFO", "SafetyMonitorVM.cs", "Disconnected Safety Monitor"),
		nina("00:21:00.0000", "INFO", "SafetyMonitorVM.cs", "Successfully connected Safety Monitor. Id: ASCOM.Hub.SafetyMonitor"),
		nina("00:21:02.0000", "INFO", "SafetyMonitorVM.cs", "SafetyMonitorInfo state changed to Unsafe"),
		nina("00:21:40.0000", "INFO", "SequenceItem.cs", "Finishing Category: Flat Device, Item: CloseCover"),
		nina("00:30:00.0000", "INFO", "SequenceItem.cs", "Finishing Category: Flat Device, Item: OpenCover"),
		nina("02:54:27.5100", "INFO", "SafetyMonitorVM.cs", "SafetyMonitorInfo state changed to Unsafe"),
		nina("03:03:06.0162", "INFO", "FlatDeviceVM.cs", "Closing Flat Device Cover"),
		nina("03:03:14.0808", "INFO", "SequenceItem.cs", "Finishing Category: Flat Device, Item: CloseCover"),
		nina("03:04:43.3913", "INFO", "SequenceItem.cs", "Finishing Category: Flat Device, Item: OpenCover"),
		nina("05:34:41.5168", "INFO", "SafetyMonitorVM.cs", "SafetyMonitorInfo state changed to Unsafe"),
		nina("05:35:45.7627", "INFO", "SequenceItem.cs", "Finishing Category: Flat Device, Item: CloseCover"),
		nina("07:04:53.5132", "INFO", "SafetyMonitorVM.cs", "SafetyMonitorInfo state changed to Unsafe"),
	}
}

func TestFlapTimelineMeasuresHowLongTheFlapTookToClose(t *testing.T) {
	t.Parallel()
	start, end := at("00:00:00"), at("11:00:00")
	f := FlapTimeline(true, nightLines(), start, end)

	if len(f.Unsafe) != 2 {
		t.Fatalf("got %d unsafe events, want 2 (the restart's and the connect's readings are not weather, 07:04 found the flap closed): %+v", len(f.Unsafe), f.Unsafe)
	}
	d, ok := f.Unsafe[0].Latency()
	if !ok || !f.Unsafe[0].At.Equal(at("02:54:27.5100")) {
		t.Fatalf("first event %+v", f.Unsafe[0])
	}
	if want := at("03:03:14.0808").Sub(at("02:54:27.5100")); d != want || d.Round(time.Second) != 8*time.Minute+47*time.Second {
		t.Fatalf("02:54 latency %s, want %s", d, want)
	}
	if d, _ := f.Unsafe[1].Latency(); d != time.Minute+4*time.Second+245900*time.Microsecond {
		t.Fatalf("05:34 latency %s", d)
	}
	if len(f.Restarts) != 1 || !f.Restarts[0].At.Equal(at("00:20:00")) {
		t.Fatalf("restarts %+v, want the one at 00:20", f.Restarts)
	}
	if d, _ := f.Restarts[0].Latency(); d != 100*time.Second {
		t.Fatalf("restart latency %s, want 1m40s", d)
	}
	want := at("00:21:40").Sub(start) + at("03:03:14.0808").Sub(at("00:30:00")) + at("05:35:45.7627").Sub(at("03:04:43.3913"))
	if f.Open != want {
		t.Fatalf("open %s, want %s", f.Open, want)
	}
	if f.Moves != 5 {
		t.Fatalf("moves %d, want 5", f.Moves)
	}
}

func TestFlapTimelineLeavesAnOpenFlapOpenAtTheEnd(t *testing.T) {
	t.Parallel()
	lines := []Line{
		nina("01:00:00.0000", "INFO", "SequenceItem.cs", "Finishing Category: Flat Device, Item: OpenCover"),
		nina("02:00:00.0000", "INFO", "SafetyMonitorVM.cs", "SafetyMonitorInfo state changed to Unsafe"),
	}
	f := FlapTimeline(false, lines, at("00:00:00"), at("03:00:00"))
	if f.Open != 2*time.Hour {
		t.Fatalf("open %s, want 2h", f.Open)
	}
	if len(f.Unsafe) != 1 || f.Unsafe[0].Closed != nil {
		t.Fatalf("want one event never closed, got %+v", f.Unsafe)
	}
}

func TestSeedOpen(t *testing.T) {
	t.Parallel()
	if SeedOpen(nil) {
		t.Error("no line is closed")
	}
	open := nina("01:00:00.0000", "INFO", "SequenceItem.cs", "Finishing Category: Flat Device, Item: OpenCover")
	closed := nina("01:00:00.0000", "INFO", "SequenceItem.cs", "Finishing Category: Flat Device, Item: CloseCover")
	if !SeedOpen(&open) || SeedOpen(&closed) {
		t.Error("seed misread the last cover step")
	}
}

func TestMergeRestarts(t *testing.T) {
	t.Parallel()
	c1, c2 := at("22:42:24"), at("23:16:10")
	got := MergeRestarts([]UnsafeEvent{
		{At: at("22:40:38"), Closed: &c1}, {At: at("22:41:22"), Closed: &c1}, {At: at("22:41:35"), Closed: &c1},
		{At: at("23:14:37"), Closed: &c2},
	})
	if len(got) != 2 || !got[0].At.Equal(at("22:40:38")) || !got[1].At.Equal(at("23:14:37")) {
		t.Fatalf("got %+v", got)
	}
}

func TestGuiderBurstsGroupsAndNamesErrors(t *testing.T) {
	t.Parallel()
	lines := make([]Line, 0, 7)
	lines = append(lines, nina("01:26:06.0000", "ERROR", "PHD2Guider.cs", "PHD2 error:timed-out waiting for guider to settle"))
	for _, s := range []string{"25.4341", "26.1578", "26.9326", "27.6693"} {
		lines = append(lines, nina("02:25:"+s, "WARNING", "PHD2Guider.cs", "PHDError: Cannot initiate dither while dither is in progress CODE: 1"))
	}
	lines = append(lines, nina("02:27:00.0000", "ERROR", "PHD2Guider.cs", "PHD2 error:Guiding stopped"))
	lines = append(lines, nina("02:40:00.0000", "WARNING", "PHD2Guider.cs", "Phd2 - Something New Happened"))
	b := GuiderBursts(lines)
	if len(b) != 3 {
		t.Fatalf("got %d bursts, want 3: %+v", len(b), b)
	}
	if !b[1].From.Equal(at("02:25:25.4341")) || !b[1].To.Equal(at("02:27:00")) {
		t.Fatalf("burst 2 spans %s to %s", b[1].From, b[1].To)
	}
	if b[1].Kinds[0] != (Count{Label: "dither refused, one already running", N: 4}) || b[1].Kinds[1] != (Count{Label: "guiding stopped", N: 1}) {
		t.Fatalf("burst 2 kinds %+v", b[1].Kinds)
	}
	if b[0].Kinds[0].Label != "settle timed out" || b[2].Kinds[0].Label != "something new happened" {
		t.Fatalf("kinds %+v %+v", b[0].Kinds, b[2].Kinds)
	}
}

func TestAutofocusRunsFromTheLog(t *testing.T) {
	t.Parallel()
	lines := []Line{
		nina("01:36:34.0000", "INFO", "AutoFocusEngine.cs", "Starting AutoFocus with initial position 26233"),
		nina("01:38:48.0000", "INFO", "HocusFocusVM.cs", "AutoFocus completed with focuser starting at 26233 and ending at 26277"),
		nina("01:42:00.0000", "INFO", "AutoFocusEngine.cs", "Starting AutoFocus with initial position 26277"),
		nina("01:44:00.0000", "WARNING", "AutoFocusEngine.cs", "AutoFocus did not complete successfully, so restoring the focuser position to 26277"),
		nina("03:00:00.0000", "INFO", "AutoFocusEngine.cs", "Starting AutoFocus with initial position 26277"),
		nina("04:48:58.0000", "WARNING", "AutoFocusEngine.cs", "AutoFocus did not complete successfully, so restoring the focuser position to 26168"),
	}
	runs := AutofocusRuns(lines)
	if len(runs) != 3 {
		t.Fatalf("got %d runs, want 3", len(runs))
	}
	if !runs[0].Success || runs[1].Success || runs[2].Success {
		t.Fatalf("outcomes %+v", runs)
	}
	if runs[0].BackToBack || !runs[1].BackToBack || runs[2].BackToBack {
		t.Fatalf("back to back %v %v %v, want only the second", runs[0].BackToBack, runs[1].BackToBack, runs[2].BackToBack)
	}
	lone := AutofocusRuns(lines[5:])
	if len(lone) != 1 || lone[0].Success || lone[0].Started != nil {
		t.Fatalf("an outcome without a start is still a failed run: %+v", lone)
	}
}

func pts(start time.Time, vals ...float64) []Point {
	out := make([]Point, len(vals))
	for i, v := range vals {
		out[i] = Point{T: start.Add(time.Duration(i) * Step), V: v}
	}
	return out
}

func TestGuidingStatsOnlyWhileExposing(t *testing.T) {
	t.Parallel()
	start := at("01:00:00")
	vals := make([]float64, 60)
	for i := range vals {
		vals[i] = 0.7
	}
	for i := 20; i < 30; i++ {
		vals[i] = 2.0
	}
	for i := 40; i < 50; i++ {
		vals[i] = 50
	}
	vals[5] = 0
	spans := []Span{{From: start, To: start.Add(35 * time.Minute)}}
	g := GuidingStats(During(pts(start, vals...), spans))
	if g == nil {
		t.Fatal("no stats")
	}
	if g.Median != 0.7 {
		t.Fatalf("median %v, want 0.7", g.Median)
	}
	if g.Worst != 2.0 || !g.WorstAt.Equal(start.Add(20*time.Minute)) {
		t.Fatalf("worst %v at %s, want 2.0 at 01:20 (the 50s were outside exposures)", g.Worst, g.WorstAt)
	}
	if g.Samples != 35 {
		t.Fatalf("samples %d, want 35 (36 in the span less the zero)", g.Samples)
	}
	if GuidingStats(nil) != nil {
		t.Fatal("no points is not measured")
	}
}

func TestDewStats(t *testing.T) {
	t.Parallel()
	start := at("01:00:00")
	margin := pts(start, 5, 3.5, 2.4, 1.0, 2.8)
	tracking := pts(start, 1, 1, 1, 0, 1)
	heater := pts(start, 50, 62, 87, 100, 75)
	d := DewStats(margin, tracking, heater)
	if d.Lowest != 2.4 || !d.LowestAt.Equal(start.Add(2*Step)) {
		t.Fatalf("lowest %v at %s, want 2.4 (1.0 was not tracking)", d.Lowest, d.LowestAt)
	}
	if d.Under != 3*Step || d.Boosted || d.HeaterMax == nil || *d.HeaterMax != 87 {
		t.Fatalf("got %+v", d)
	}
	heater[2].V = 100
	if !DewStats(margin, tracking, heater).Boosted {
		t.Fatal("100% while under the margin is a boost")
	}
	if !DewStats(margin, nil, heater).Boosted || DewStats(margin, nil, heater).Tracking {
		t.Fatal("with no tracking the whole window counts")
	}
	if DewStats(nil, tracking, heater) != nil {
		t.Fatal("no margin is not measured")
	}
}

func TestSafeTimeNeedsAConnectedMonitor(t *testing.T) {
	t.Parallel()
	start := at("01:00:00")
	safe, conn := SafeTime(pts(start, 1, 1, 1, 0, 1), pts(start, 1, 1, 0, 1, 1), []Span{{From: start, To: start.Add(time.Hour)}})
	if safe != 3*Step || conn != 4*Step {
		t.Fatalf("safe %s connected %s", safe, conn)
	}
}

func TestRange(t *testing.T) {
	t.Parallel()
	lo, hi := Range(pts(at("01:00:00"), 43, 7, 60))
	if *lo != 7 || *hi != 60 {
		t.Fatalf("%v %v", *lo, *hi)
	}
	if lo, hi := Range(nil); lo != nil || hi != nil {
		t.Fatal("empty range")
	}
}

func TestGuiderKindIsOneShortLine(t *testing.T) {
	t.Parallel()
	long := "2026-10-14T02:00:00|ERROR|PHD2Guider.cs|Run|1|PHDError: <Ω> & " + strings.Repeat("é", 80) + "\n   at NINA.Foo()"
	got := guiderKind(long)
	if strings.Contains(got, "\n") || !utf8.ValidString(got) || utf8.RuneCountInString(got) > 61 || strings.Contains(got, "nina.foo") {
		t.Fatalf("%q", got)
	}
	if got := guiderKind("2026-10-14T02:00:00|ERROR|PHD2Guider.cs|Run|1|PHD2 error while sending a request"); got != "PHD2 not reachable" {
		t.Fatalf("%q", got)
	}
	b := []ErrorBurst{{From: at("02:00:00"), To: at("02:00:00"), Kinds: []Count{{guiderKind(long), 1}}}}
	if text := formatter(t).guiderErrors(Report{GuiderErrors: b}); strings.Contains(text, "<Ω>") || !strings.Contains(text, "&lt;ω&gt; &amp;") {
		t.Fatalf("labels go into the HTML escaped: %q", text)
	}
}

func TestDarkSpansAtTheSite(t *testing.T) {
	t.Parallel()
	site := sky.Site{Latitude: 32, Longitude: -97}
	start, end := at("11:00:00").Add(-24*time.Hour), at("11:00:00")
	spans := DarkSpans(site, start, end)
	if len(spans) != 1 {
		t.Fatalf("%+v", spans)
	}
	d := spans[0]
	for _, c := range []struct {
		t    time.Time
		dark bool
	}{{d.From.Add(-time.Second), false}, {d.From, true}, {d.To.Add(-time.Second), true}, {d.To, false}} {
		if got := site.SunAltitude(c.t) <= DarkSunAltitude; got != c.dark {
			t.Errorf("at %s dark %v, want %v", c.t, got, c.dark)
		}
	}
	if h := Total(spans).Hours(); h < 9.5 || h > 11.5 {
		t.Fatalf("dark %.2fh in mid October at 32°N", h)
	}
	if got := Overlap([]Span{{From: start, To: end}}, spans); got != Total(spans) {
		t.Fatalf("overlap %s", got)
	}
}
