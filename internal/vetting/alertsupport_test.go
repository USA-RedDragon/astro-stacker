package vetting

import (
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

const alertOID = "ZTF25aaqvlng"

// TestAlertSupportOutburst: 23991 (IC 4604 Panel 6) brightened on
// 2025-05-20 and 2025-05-21 and read fainter against its reference on
// 2026-03-20 and 21. ALeRCE's ZTF25aaqvlng, 0.2" from the G 20.35 star,
// has three positive difference detections on the first two nights, so
// the open verdict becomes a real outburst. Another star's alert, alerts
// on other nights and alerts of the other sign leave it open.
func TestAlertSupportOutburst(t *testing.T) {
	t.Parallel()
	day := func(s string) time.Time {
		d, err := time.Parse(time.DateOnly, s)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	members := []app.DiscoveryCandidate{
		{Kind: app.DiscoveryKindChange, Night: day("2025-05-20"), Sign: 1},
		{Kind: app.DiscoveryKindChange, Night: day("2025-05-21"), Sign: 1},
		{Kind: app.DiscoveryKindChange, Night: day("2026-03-20"), Sign: -1},
		{Kind: app.DiscoveryKindChange, Night: day("2026-03-21"), Sign: -1},
	}
	const ra, dec = 244.529950, -24.011500
	dets := []ZTFDetection{
		{MJD: 60815.394513899926, Band: "r", Mag: 17.589348, Positive: true},
		{MJD: 60817.334710599855, Band: "g", Mag: 18.855827, Positive: true},
		{MJD: 60817.37649310008, Band: "r", Mag: 18.538807, Positive: true},
	}
	open := Suggestion{State: app.VetUnsure, Say: sayNotSure, Confidence: Low, Why: "The checks found nothing against it, and nothing that confirms it."}

	s := alertSupport(open, []ZTFObject{{OID: alertOID, RA: 244.5299340532666, Dec: -24.011478694735718, Detections: dets}}, ra, dec, members, nil)
	if s.State != app.VetReal || s.Kind != KindBrightens || s.Confidence != High || !strings.Contains(s.Say, "outburst, also seen by ZTF") ||
		!strings.Contains(s.Why, alertOID) || !strings.Contains(s.Why, "17.59 in r on the night of 2025-05-19, 18.86 in g on the night of 2025-05-21") {
		t.Errorf("23991: %+v, want a real outburst seen by ZTF", s)
	}
	if s := alertSupport(Suggestion{State: app.VetUnsure, Say: sayNoSurveySaw}, []ZTFObject{{OID: alertOID, RA: 244.5299340532666, Dec: -24.011478694735718, Detections: dets}}, ra, dec, members, nil); s.State != app.VetReal {
		t.Errorf("no survey saw it: %+v, want real", s)
	}

	shift := func(ds []ZTFDetection, days float64, flip bool) []ZTFDetection {
		out := make([]ZTFDetection, len(ds))
		for i, d := range ds {
			d.MJD += days
			if flip {
				d.Positive = !d.Positive
			}
			out[i] = d
		}
		return out
	}
	for _, x := range []struct {
		name string
		obj  ZTFObject
	}{
		{"another star 1.4 arcsec away", ZTFObject{OID: "ZTFother", RA: ra, Dec: dec + 1.4/3600, Detections: dets}},
		{"other nights", ZTFObject{OID: alertOID, RA: ra, Dec: dec, Detections: shift(dets, -40, false)}},
		{"four days off", ZTFObject{OID: alertOID, RA: ra, Dec: dec, Detections: shift(dets, 4, false)}},
		{"opposite sign", ZTFObject{OID: alertOID, RA: ra, Dec: dec, Detections: shift(dets, 0, true)}},
	} {
		if s := alertSupport(open, []ZTFObject{x.obj}, ra, dec, members, nil); s.State != app.VetUnsure || s.Say != sayNotSure {
			t.Errorf("%s: %+v, want it left open", x.name, s)
		}
	}

	fade := []ZTFDetection{{MJD: 61120.4, Band: "r", Mag: 18.9, Positive: false}}
	if s := alertSupport(open, []ZTFObject{{OID: "ZTFfade", RA: ra, Dec: dec, Detections: fade}}, ra, dec, members, nil); s.State != app.VetReal || s.Kind != KindFades || s.Confidence != Medium {
		t.Errorf("fade on a fade night: %+v, want a real fade at medium confidence", s)
	}

	art := Suggestion{State: app.VetArtifact, Say: "Artifact: hot pixel"}
	if s := alertSupport(art, []ZTFObject{{OID: alertOID, RA: ra, Dec: dec, Detections: dets}}, ra, dec, members, nil); s.State != app.VetArtifact {
		t.Errorf("artifact: %+v, want it kept", s)
	}
}
