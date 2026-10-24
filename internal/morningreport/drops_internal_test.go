package morningreport

import (
	"strings"
	"testing"
	"time"
)

func TestDropsOn20261023(t *testing.T) {
	t.Parallel()
	drops := Drops(fixture(t, "nina-2026-10-23-drops.jsonl.zst"))
	if len(drops) != 2 {
		t.Fatalf("got %d drops, want the 21:29 and 01:24 ones: %+v", len(drops), drops)
	}
	first, second := drops[0], drops[1]
	if !first.At.Equal(ct("21:29:33.109")) || !second.At.Equal(ct("01:24:46.8247")) {
		t.Fatalf("drops at %s and %s", first.At.In(central()), second.At.In(central()))
	}
	for _, d := range drops {
		if strings.Join(d.Devices, ",") != "Falcon FR5FJYOSA,ZWO Focuser" {
			t.Fatalf("drop at %s lost %q", d.At.In(central()), d.Devices)
		}
	}
	if first.Refused != 8051 || !first.RefusedFrom.Equal(ct("21:40:06.6192")) || !near(first.RefusedTo, ct("00:42:46.8282")) || len(first.Hung) != 0 {
		t.Fatalf("first drop: %d refused %s to %s, %d hung", first.Refused, first.RefusedFrom.In(central()), first.RefusedTo.In(central()), len(first.Hung))
	}
	if second.Refused != 0 || len(second.Hung) != 10 {
		t.Fatalf("second drop: %d refused, %d hung, want 0 and 10", second.Refused, len(second.Hung))
	}
	h := second.Hung[0]
	if !h.From.Equal(ct("01:35:04.4886")) || !h.To.Equal(ct("02:01:06.6303")) || h.Length != 600*time.Second {
		t.Fatalf("first hang %+v", h)
	}
	for _, h := range second.Hung[1:] {
		if h.Took().Round(time.Second) != 20*time.Minute {
			t.Fatalf("hang at %s took %s, want 20m", h.From.In(central()), h.Took())
		}
	}
}

func TestDropsStartAgainAfterNINACloses(t *testing.T) {
	t.Parallel()
	lines := []Line{
		nina("01:00:00.0000", "ERROR", "AscomDevice.cs", "ZWO Focuser should be connected but reports to be disconnected. Trying to reconnect..."),
		nina("01:01:00.0000", "INFO", "ApplicationVM.cs", "Application shutting down"),
		nina("01:02:00.0000", "INFO", "SequenceItem.cs", "Starting Category: Scheduler, Item: TakeExposure, ExposureTime 60, Gain 0"),
		nina("01:30:00.0000", "INFO", "SequenceItem.cs", "Finishing Category: Scheduler, Item: TakeExposure, ExposureTime 60, Gain 0"),
		nina("01:31:00.0000", "ERROR", "SequenceItem.cs", "Failed validation: Category: Scheduler, Item: TakeExposure, ExposureTime 60 - Camera not connected"),
		nina("01:32:00.0000", "ERROR", "AscomDevice.cs", "ZWO Focuser should be connected but reports to be disconnected. Trying to reconnect..."),
	}
	d := Drops(lines)
	if len(d) != 2 || len(d[0].Hung) != 0 || d[0].Refused != 0 || !d[1].At.Equal(at("01:32:00")) {
		t.Fatalf("a hang or refusal after NINA closed was counted against the drop before it: %+v", d)
	}
}

func TestTextOn20261023Drops(t *testing.T) {
	t.Parallel()
	start, end := night1023()
	r := Report{
		Night: time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC), Start: start, End: end,
		Subs:  &Subs{Count: 13, Rejected: 13},
		Drops: Drops(fixture(t, "nina-2026-10-23-drops.jsonl.zst")),
	}
	text := Formatter{Loc: central(), Zone: "CT"}.Text(r)
	for _, want := range []string{
		"<b>Equipment</b> dropped at 21:29 and 01:24 CT\n",
		"• 21:29 CT: NINA lost Falcon FR5FJYOSA and ZWO Focuser; camera not connected, 8051 exposures refused 21:40–00:42 CT\n",
		"• 01:24 CT: NINA lost Falcon FR5FJYOSA and ZWO Focuser; NINA hung on 10 exposures for 20m to 26m each, 01:35–05:21 CT\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
}
