package morningreport

import (
	"strings"
	"testing"
	"time"
)

func TestFailedClosesOn20261023(t *testing.T) {
	t.Parallel()
	start, end := night1023()
	f := FlapTimeline(false, fixture(t, "nina-2026-10-23-flap.jsonl"), start, end)
	if len(f.Unsafe) != 1 || !f.Unsafe[0].At.Equal(ct("05:28:20.1883")) || f.Unsafe[0].Closed != nil || f.Unsafe[0].CloseFailed != "flat panel unreachable" {
		t.Fatalf("unsafe %+v, want 05:28 left open because the flat panel was unreachable", f.Unsafe)
	}
	if len(f.FailedCloses) != 2 || !f.FailedCloses[0].At.Equal(ct("05:28:40.6011")) || !near(f.FailedCloses[1].At, ct("06:54:51.6351")) {
		t.Fatalf("failed closes %+v, want 05:28 and 06:54", f.FailedCloses)
	}
}

func TestRestartsOn20261023(t *testing.T) {
	t.Parallel()
	start, end := night1023()
	dark := testDark(start, end)
	if last := dark[len(dark)-1].To; !last.Before(ct("07:57:32")) {
		t.Fatalf("the test site's dark ends at %s, want before the relaunch", last.In(central()))
	}
	f := FlapTimeline(false, fixture(t, "nina-2026-10-23-flap.jsonl"), start, end)

	restarts := MergeRestarts(DropEndOfNight(f.Restarts, dark))
	if len(restarts) != 1 || !restarts[0].At.Equal(ct("00:53:07.5534")) {
		t.Fatalf("restarts %+v, want only 00:53; the 07:57 relaunch after the sequence finished is the night's end", restarts)
	}
	if restarts[0].Closed == nil || !restarts[0].Closed.Equal(ct("00:55:07.5476")) {
		t.Fatalf("00:53 restart closed %v, want 00:55:07", restarts[0].Closed)
	}
}

func TestDropEndOfNightKeepsCrashesAndNightRestarts(t *testing.T) {
	t.Parallel()
	dark := []Span{{From: at("00:00:00"), To: at("06:00:00")}}
	got := DropEndOfNight([]UnsafeEvent{
		{At: at("01:00:00"), SequenceEnd: true},
		{At: at("07:00:00")},
		{At: at("07:30:00"), SequenceEnd: true},
	}, dark)
	if len(got) != 2 || !got[0].At.Equal(at("01:00:00")) || !got[1].At.Equal(at("07:00:00")) {
		t.Fatalf("got %+v", got)
	}
}

func TestTextOn20261023Flap(t *testing.T) {
	t.Parallel()
	start, end := night1023()
	dark := testDark(start, end)
	f := FlapTimeline(false, fixture(t, "nina-2026-10-23-flap.jsonl"), start, end)
	r := Report{
		Night: time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC), Start: start, End: end, DarkSpans: dark,
		Subs:   &Subs{Count: 13, Rejected: 13},
		Unsafe: f.Unsafe, Restarts: MergeRestarts(DropEndOfNight(f.Restarts, dark)), FailedCloses: f.FailedCloses,
	}
	text := Formatter{Loc: central(), Zone: "CT"}.Text(r)
	for _, want := range []string{
		"• 05:28 CT, <b>flap open, could not be closed</b> (flat panel unreachable)\n",
		"• NINA restarted at 00:53 CT with the flap open, flap closed after 2m 00s\n",
		"• closing the flap failed at 05:28 and 06:54 CT (flat panel unreachable); <b>flap still open</b> at 11:00 CT\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "07:57") {
		t.Errorf("text lists the 07:57 end-of-night relaunch:\n%s", text)
	}
}
