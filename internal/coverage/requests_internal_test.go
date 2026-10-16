package coverage

import (
	"testing"
	"time"
)

func TestDarkRequestsNearOneTemperatureShareACombo(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 10, 15, 0, 0, 0, time.UTC)
	need := func(id string, tmp float64, have int) openNeed {
		return openNeed{ref: RequestRef{ID: id, Object: id, Night: "2026-10-09"},
			need: requestNeed{exposure: 300, gain: 0, offset: 50, bin: 1, tmp: tmp, needed: DarkFramesTarget(300), have: have}}
	}
	b := DarkBacklog{Combos: []DarkCombo{{ComboKey: "600|0|50|1|-15", Exposure: 600, Offset: 50, Binning: 1, SetTemp: -15,
		SetTempsCovered: []float64{-15}, Priority: 1, Requests: []RequestRef{}}}}
	mergeDarkRequests(&b, []openNeed{need("a", -15, 4), need("b", -15.5, 2), need("c", -17, 0)}, nil, now)
	if len(b.Combos) != 3 {
		t.Fatalf("combos %+v", b.Combos)
	}
	ab, c, other := b.Combos[0], b.Combos[1], b.Combos[2]
	if ab.ComboKey != "300|0|50|1|-15" || len(ab.Requests) != 2 || len(ab.SetTempsCovered) != 2 || ab.SetTempsCovered[0] != -15.5 ||
		ab.FramesHave != 0 || ab.Priority != 0 {
		t.Errorf("requests 0.5 °C apart: %+v", ab)
	}
	if c.ComboKey != "300|0|50|1|-17" || c.Priority != 0 || len(c.Requests) != 1 {
		t.Errorf("a request 2 °C away: %+v", c)
	}
	if other.Priority != 1 || len(other.Requests) != 0 {
		t.Errorf("another exposure is another setup: %+v", other)
	}
}
