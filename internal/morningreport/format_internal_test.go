package morningreport

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func formatter(t *testing.T) Formatter {
	t.Helper()
	return Formatter{Loc: chicago(t).Loc, Zone: "CT"}
}

func dur(d time.Duration) *time.Duration { return &d }

func ptr(v float64) *float64 { return &v }

func sampleReport() Report {
	closed1, closed2, restartClosed := at("03:03:14.0808"), at("01:54:32"), at("22:42:24").Add(-24*time.Hour)
	started := at("02:12:00")
	return Report{
		Night:    day(night1013),
		Start:    at("11:00:00").Add(-24 * time.Hour),
		End:      at("11:00:00"),
		Dark:     dur(10*time.Hour + 31*time.Minute),
		Safe:     dur(9*time.Hour + 2*time.Minute),
		FlapOpen: dur(8*time.Hour + 16*time.Minute + 8*time.Second),
		Imaged:   dur(4*time.Hour + 21*time.Minute),
		Subs: &Subs{
			Count: 127, Exposure: 4*time.Hour + 21*time.Minute, Kept: 118, Rejected: 9,
			Targets: []TargetSubs{
				{Object: "161P/Hartley-IRAS", Filters: []FilterSubs{{Filter: filterL, Kept: 13}, {Filter: filterR, Kept: 53, Rejected: 7}}},
				{Object: "Barnard's E", Filters: []FilterSubs{{Filter: filterR, Kept: 5, Pending: 2}}},
				{Object: "A<B & C", Filters: []FilterSubs{{Filter: "H-a", Rejected: 1}}},
			},
			Reasons: []Count{{"no stars measured", 5}, {"low quality score", 2}, {"pointed off target", 2}, {"too close to the moon", 1}},
		},
		Unsafe: []UnsafeEvent{
			{At: at("01:54:19"), Closed: &closed2},
			{At: at("02:54:27.5100"), Closed: &closed1},
			{At: at("10:59:00")},
		},
		Restarts:  []UnsafeEvent{{At: at("22:40:38").Add(-24 * time.Hour), Closed: &restartClosed}},
		Dew:       &Dew{Lowest: 1.443, LowestAt: at("04:27:00"), Under: 5*time.Hour + 51*time.Minute, HeaterMax: ptr(87), Tracking: true, TrackingMeasured: true, ImagingLow: ptr(2.3), ImagingHigh: ptr(6.7)},
		Autofocus: []AutofocusRun{{Started: &started, Finished: at("02:14:00"), Success: true}, {Finished: at("02:20:00"), BackToBack: true}},
		Guiding:   &Guiding{Median: 0.744, WorstMeasured: true, Worst: 6.93, WorstAt: at("01:43:00"), WorstEnd: at("01:48:00"), Samples: 200},
		GuiderErrors: []ErrorBurst{
			{From: at("02:25:25"), To: at("02:25:37"), Kinds: []Count{{"dither refused, one already running", 17}}},
		},
		Camera: Camera{Setpoints: []Setpoint{{2, 106}, {4, 18}}, CoolerMin: ptr(43), CoolerMax: ptr(56)},
	}
}

func TestTextShowsTheMeasuredNight(t *testing.T) {
	t.Parallel()
	got := formatter(t).Text(sampleReport())
	for _, want := range []string{
		"<b>Observatory, night of Tue Oct 13</b>",
		"Safe 9h 02m of 10h 31m dark · flap open 8h 16m · imaged 4h 21m\n",
		"<b>Subs</b> 118 kept, 9 rejected",
		"• 161P/Hartley-IRAS: Luminance 13, Red 53 (7 rejected)",
		"• Barnard's E: Red 5 (2 waiting)",
		"• A&lt;B &amp; C: H-a 0 (1 rejected)",
		"Top rejection reasons: 5 no stars measured, 2 low quality score, 2 pointed off target\n",
		"• 01:54 CT, flap closed after 13s\n",
		"• 02:54 CT, flap closed after 8m 47s <b>(over 30s)</b>",
		"• 10:59 CT, <b>flap still open</b> at 11:00 CT",
		"• NINA restarted at 22:40 CT with the flap open, flap closed after 1m 46s",
		"<b>Dew</b> lowest margin 1.4 °C at 04:27 CT while tracking (2.3–6.7 °C while imaging); under 4 °C for 5h 51m. Heaters <b>did not boost</b> to 100% (highest 87%)",
		"<b>Autofocus</b> 2 runs, <b>1 failed</b>, <b>1 back to back</b>: 02:12, 02:20 (failed, right after the last) CT",
		"<b>Guiding</b> median RMS 0.74″, worst 10 min 6.93″ at 01:43–01:48 CT",
		"<b>Guider errors</b> 17 in 1 burst\n• 02:25 CT: 17 dither refused, one already running",
		"<b>Camera</b> setpoints 2 °C (106 subs), 4 °C (18 subs); cooler power 43–56%",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if len(got) > MaxMessage {
		t.Fatalf("%d chars", len(got))
	}
}

func TestTextSaysNotMeasured(t *testing.T) {
	t.Parallel()
	r := Report{Night: day(night1013), Start: at("11:00:00").Add(-24 * time.Hour), End: at("11:00:00"), Imaged: dur(time.Hour), Subs: &Subs{Count: 3, Kept: 3, Exposure: time.Hour},
		UnsafeMissing: noLog, GuiderErrorsMissing: noLog, AutofocusMissing: noLog}
	got := formatter(t).Text(r)
	for _, want := range []string{
		"Safe not measured · flap open not measured · imaged 1h 00m of the whole 24h 00m (dark hours not measured)\n",
		"<b>Unsafe</b> not measured", "<b>Dew</b> not measured", "<b>Autofocus</b> not measured",
		"<b>Guiding</b> not measured", "<b>Guider errors</b> not measured",
		"<b>Camera</b> setpoint not measured; cooler power not measured",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestIdleNightIsOneLineWithTheReason(t *testing.T) {
	t.Parallel()
	f := formatter(t)
	base := Report{Night: day(night1013), Start: at("11:00:00").Add(-24 * time.Hour), End: at("11:00:00"), Dark: dur(10 * time.Hour), Subs: &Subs{}}
	cases := []struct {
		name string
		edit func(*Report)
		want string
	}{
		{"unsafe", func(r *Report) { r.Safe, r.SafetyConnected = dur(0), dur(12*time.Hour) }, "nothing imaged; unsafe all night."},
		{"off", func(r *Report) { r.Safe, r.SafetyConnected = dur(0), dur(0) }, "nothing imaged; the safety monitor never connected."},
		{"unknown", func(*Report) {}, "nothing imaged; safety not measured."},
		{"idle", func(r *Report) {
			r.Safe, r.SafetyConnected, r.FlapOpen = dur(2*time.Hour+10*time.Minute), dur(12*time.Hour), dur(time.Hour)
		},
			"safe for 2h 10m of 10h 00m dark with the flap open 1h 00m, but no subs were taken."},
	}
	for _, c := range cases {
		r := base
		c.edit(&r)
		got := f.Text(r)
		if !strings.HasPrefix(got, "<b>Observatory, night of Tue Oct 13</b>: ") || !strings.HasSuffix(got, c.want) || strings.Contains(got, "\n") {
			t.Errorf("%s: got %q", c.name, got)
		}
	}
	r := base
	r.Safe, r.SafetyConnected = dur(time.Hour), dur(time.Hour)
	r.Unsafe = []UnsafeEvent{{At: at("02:00:00")}}
	if got := f.Text(r); !strings.Contains(got, "\n<b>Unsafe</b>\n• 02:00 CT, <b>flap still open</b> at 11:00 CT") {
		t.Errorf("an idle night still shows a flap left open: %q", got)
	}
}

func TestTextFitsOneMessage(t *testing.T) {
	t.Parallel()
	r := sampleReport()
	r.Subs.Targets = nil
	for i := range 200 {
		r.Subs.Targets = append(r.Subs.Targets, TargetSubs{Object: fmt.Sprintf("A long target name number %03d", i),
			Filters: []FilterSubs{{Filter: filterL, Kept: 10}, {Filter: filterR, Kept: 3, Rejected: 1}, {Filter: "Green", Kept: 3}, {Filter: "Blue", Kept: 3}}})
	}
	got := formatter(t).Text(r)
	if len(got) > MaxMessage {
		t.Fatalf("%d chars, over %d", len(got), MaxMessage)
	}
	if !strings.Contains(got, "more targets") || !strings.Contains(got, "<b>Camera</b>") {
		t.Fatalf("the target list should shrink and the rest stay:\n%s", got)
	}
}

func TestCaption(t *testing.T) {
	t.Parallel()
	f := formatter(t)
	im := Image{Object: "Barnard's E", Label: "LRGB color", Exposure: 4*time.Hour + 25*time.Minute, NewSubs: 15, Current: true}
	if got := f.Caption(im); got != "<b>Barnard's E</b> · LRGB color · 4h 25m total · +15 subs last night" {
		t.Fatalf("got %q", got)
	}
	im.Current, im.NewSubs = false, 1
	if got := f.Caption(im); !strings.HasSuffix(got, "+1 sub last night (this stack does not include last night yet)") {
		t.Fatalf("got %q", got)
	}
	im.Object = strings.Repeat("Ω<", 600)
	if got := f.Caption(im); len(got) > MaxCaption || !strings.HasPrefix(got, "<b>Ω&lt;") || !strings.Contains(got, "…</b>") {
		t.Fatalf("long caption %d bytes: %q", len(got), got[:40])
	}
	im.ByNight = true
	if got := f.Caption(im); !strings.HasSuffix(got, "(a moving target: last night's subs are in that night's own stack, not this image)") {
		t.Fatalf("got %q", got)
	}
}

func TestDurations(t *testing.T) {
	t.Parallel()
	for d, want := range map[time.Duration]string{
		13 * time.Second: "13s", 64 * time.Second: "1m 04s", 527 * time.Second: "8m 47s", 2 * time.Hour: "2h 00m",
	} {
		if got := ms(d); got != want {
			t.Errorf("ms(%s) = %q, want %q", d, got, want)
		}
	}
	if got := hm(59 * time.Second); got != "1m" {
		t.Errorf("hm = %q", got)
	}
	if got := num(-0.04, 1); got != "0.0" {
		t.Errorf("num = %q", got)
	}
}

func TestTextCapsLongListsSoLaterSectionsStay(t *testing.T) {
	t.Parallel()
	r := sampleReport()
	r.Autofocus, r.Unsafe = nil, nil
	for i := range 300 {
		r.Autofocus = append(r.Autofocus, AutofocusRun{Finished: at("00:00:00").Add(time.Duration(i) * time.Minute), BackToBack: i > 0, Success: true})
		r.Unsafe = append(r.Unsafe, UnsafeEvent{At: at("00:00:00").Add(time.Duration(i) * time.Minute)})
	}
	got := formatter(t).Text(r)
	for _, want := range []string{"<b>Autofocus</b> 300 runs, <b>299 back to back</b>: ", "and 288 more CT", "• and 290 more, 0 closed in over 30s, 290 left open", "<b>Guiding</b>", "<b>Guider errors</b>", "<b>Camera</b>"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if len(got) > MaxMessage {
		t.Fatalf("%d chars", len(got))
	}
}

func TestTextSaysWhenTrackingOrTheWorstStretchIsNotMeasured(t *testing.T) {
	t.Parallel()
	r := sampleReport()
	r.Dew.Tracking, r.Dew.TrackingMeasured = false, false
	r.Guiding.WorstMeasured = false
	got := formatter(t).Text(r)
	for _, want := range []string{"at 04:27 CT (mount tracking not measured, so over the whole window)", "<b>Guiding</b> median RMS 0.74″, worst 10 min not measured (too few readings)"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "never tracked") {
		t.Error("a missing tracking series is not a mount that never tracked")
	}
	r.Dew.TrackingMeasured = true
	if got := formatter(t).Text(r); !strings.Contains(got, "(the mount never tracked)") {
		t.Errorf("a tracking series at 0 is a mount that never tracked:\n%s", got)
	}
}

func TestTruncateKeepsTagsAndCharactersWhole(t *testing.T) {
	t.Parallel()
	s := "<b>Ωmega &amp; ÅÅÅÅÅÅÅÅ</b>"
	for n := 4; n < len(s); n++ {
		got := truncate(s, n)
		if len(got) > n || !utf8.ValidString(got) || strings.Count(got, "<b>") != strings.Count(got, "</b>") || strings.Contains(got, "&am…") || strings.Contains(got, "&amp…") {
			t.Fatalf("truncate to %d: %q", n, got)
		}
	}
}
