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

func TestTextOn20261023Flap(t *testing.T) {
	t.Parallel()
	start, end := night1023()
	dark := testDark(start, end)
	f := FlapTimeline(false, fixture(t, "nina-2026-10-23-flap.jsonl"), start, end)
	r := Report{
		Night: time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC), Start: start, End: end, DarkSpans: dark,
		Subs:   &Subs{Count: 13, Rejected: 13},
		Unsafe: f.Unsafe, Restarts: MergeRestarts(f.Restarts), FailedCloses: f.FailedCloses,
	}
	text := Formatter{Loc: central(), Zone: "CT"}.Text(r)
	for _, want := range []string{
		"• 05:28 CT, <b>flap open, could not be closed</b> (flat panel unreachable)\n",
		"• closing the flap failed at 05:28 and 06:54 CT (flat panel unreachable); <b>flap still open</b> at 11:00 CT\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
}
