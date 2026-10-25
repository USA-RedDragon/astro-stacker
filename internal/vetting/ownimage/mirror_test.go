package ownimage

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/klauspost/compress/zstd"
)

const (
	beOct10 = "2026-10-10"
	beOct13 = "2026-10-13"
)

func readBarnardsE(t *testing.T) ([]Sub, Spot) {
	t.Helper()
	raw, err := os.ReadFile("testdata/barnard299_refs.json.zst")
	if err != nil {
		t.Fatal(err)
	}
	dec, err := zstd.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	text, err := io.ReadAll(dec)
	if err != nil {
		t.Fatal(err)
	}
	var in struct {
		Subs []struct {
			FrameID  int       `json:"frame_id"`
			Filter   string    `json:"filter"`
			Night    string    `json:"night"`
			Start    time.Time `json:"start"`
			Exposure float64   `json:"exposure"`
			Weight   float64   `json:"weight"`
			AddedAt  time.Time `json:"added_at"`
		} `json:"subs"`
		Nights []struct {
			CandidateID int                      `json:"candidate_id"`
			RunID       int                      `json:"run_id"`
			Night       string                   `json:"night"`
			Group       string                   `json:"group"`
			Sign        int                      `json:"sign"`
			DeltaMag    float64                  `json:"delta_mag"`
			Light       []discoveries.LightPoint `json:"light"`
			RunAt       time.Time                `json:"run_at"`
		} `json:"nights"`
	}
	if err := json.Unmarshal(text, &in); err != nil {
		t.Fatal(err)
	}
	subs := make([]Sub, 0, len(in.Subs))
	for _, s := range in.Subs {
		night, err := time.Parse(dateLayout, s.Night)
		if err != nil {
			t.Fatal(err)
		}
		subs = append(subs, Sub{FrameID: s.FrameID, Filter: s.Filter, Night: night, Start: s.Start.UTC(), Exposure: s.Exposure, Weight: s.Weight, AddedAt: s.AddedAt.UTC()})
	}
	spot := Spot{SourceID: 299, Object: "Barnard's E", Kind: "change", RA: 293.8657, Dec: 11.0485}
	for _, n := range in.Nights {
		night, err := time.Parse(dateLayout, n.Night)
		if err != nil {
			t.Fatal(err)
		}
		dm := n.DeltaMag
		spot.Nights = append(spot.Nights, Night{Night: night, Group: n.Group, CandidateID: n.CandidateID, RunID: n.RunID, Sign: n.Sign, DeltaMag: &dm, Light: n.Light, RunAt: n.RunAt.UTC()})
	}
	return subs, spot
}

// TestMirrorBarnardsE is source 299: Barnard's E's runs 1 (2026-10-13,
// candidate 295192) and 10 (2026-10-10, candidate 296344) are its only
// nights with Red and Green subs, so each night's reference in those
// filters is the other night and "fainter on 2026-10-13" and "brighter on
// 2026-10-10" are one measurement. The vet called them 8 flagged nights
// and filters. The subs and detections are the real database rows.
func TestMirrorBarnardsE(t *testing.T) {
	t.Parallel()
	subs, spot := readBarnardsE(t)
	sp := &spotPlan{spot: spot, flagged: map[string]bool{}}
	for _, n := range spot.Nights {
		sp.flagged[nightKey(n.Night)] = true
	}
	refSets(subs, sp, defaultRefSubs)
	want := []Mirror{{Nights: [2]string{beOct10, beOct13}, Filter: "Green"}, {Nights: [2]string{beOct10, beOct13}, Filter: synthRed}}
	if len(sp.mirrors) != len(want) {
		t.Fatalf("mirrors %v, want %v", sp.mirrors, want)
	}
	for i := range want {
		if sp.mirrors[i] != want[i] {
			t.Errorf("mirror %d is %v, want %v", i, sp.mirrors[i], want[i])
		}
	}
	if got := independentDetections(sp); got != 1 {
		t.Errorf("runs 1 and 10 are %d independent detections, want 1", got)
	}
}

// TestSummarizeMirror counts the two halves of a mirror once, with the
// error of a difference of two nights, in the change's level count and
// significance and in the own-change wording.
func TestSummarizeMirror(t *testing.T) {
	t.Parallel()
	n10, n13 := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	up, down := -0.09, 0.12
	sp := &spotPlan{
		spot: Spot{Nights: []Night{
			{Night: n13, Group: discoveries.GroupBroadband, Sign: -1, DeltaMag: &down},
			{Night: n10, Group: discoveries.GroupBroadband, Sign: 1, DeltaMag: &up},
		}},
		flagged:  map[string]bool{beOct10: true, beOct13: true},
		fadeSign: -1,
		mirrors:  []Mirror{{Nights: [2]string{beOct10, beOct13}, Filter: synthRed}},
	}
	c := &Curve{Nights: []NightLevel{
		{Night: beOct10, Filter: synthRed, N: 4, DMag: -0.1, Err: 0.01},
		{Night: beOct13, Filter: synthRed, N: 5, DMag: 0.1, Err: 0.01},
		{Night: beOct10, Filter: "Luminance", N: 4, DMag: -0.05, Err: 0.01},
		{Night: beOct13, Filter: "Luminance", N: 5, DMag: 0.05, Err: 0.01},
	}}
	summarize(c, sp)
	if c.ChangeLevels != 3 {
		t.Errorf("change levels %d, want 3", c.ChangeLevels)
	}
	if math.Abs(c.Change-0.06) > 1e-9 || math.Abs(c.ChangeSigma-1500/math.Sqrt(25000)) > 1e-6 {
		t.Errorf("change %.4f at %.3f sigma, want 0.0600 at %.3f", c.Change, c.ChangeSigma, 1500/math.Sqrt(25000))
	}
	if c.FlaggedNights != 2 || c.Detections != 1 {
		t.Errorf("%d flagged nights as %d detections, want 2 as 1", c.FlaggedNights, c.Detections)
	}
	ev := mixedChange(sp, c, 0.11, true).Evidence
	for _, w := range []string{"Over the 3 independent flagged night and filter levels,", "2026-10-10 and 2026-10-13 are each other's only Red reference", "the 2 flagged nights are 1 independent detection."} {
		if !strings.Contains(ev, w) {
			t.Errorf("evidence %q lacks %q", ev, w)
		}
	}
}
