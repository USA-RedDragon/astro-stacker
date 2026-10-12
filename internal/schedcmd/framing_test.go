package schedcmd_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
)

func TestTargetFramingDescribesAndSwapsOnUndo(t *testing.T) {
	t.Parallel()
	s := spec(t, schedcmd.KindTargetFraming)
	raw := json.RawMessage(samples()[schedcmd.KindTargetFraming])
	d, err := s.Describe(raw)
	if err != nil {
		t.Fatal(err)
	}
	var before, after string
	_ = json.Unmarshal(d.Diffs[0].Before, &before)
	_ = json.Unmarshal(d.Diffs[0].After, &after)
	if d.Title != "161P/Hartley-IRAS · framing" || before != "none" || after != "long side at 84.1°, body 25% of the long side from the centre toward 264.1°" {
		t.Fatalf("%+v %q %q", d, before, after)
	}
	k, inv, err := s.Inverse(raw)
	if err != nil || k != schedcmd.KindTargetFraming {
		t.Fatal(k, err)
	}
	var p schedcmd.TargetFramingPayload
	if err := json.Unmarshal(inv, &p); err != nil {
		t.Fatal(err)
	}
	if p.Framing != nil || p.Before == nil || *p.Before.OffsetFrac != -0.25 {
		t.Fatalf("undo %+v", p)
	}
	if err := s.Validate(json.RawMessage(`{"target_guid":"g","framing":null}`)); err != nil {
		t.Errorf("clearing: %v", err)
	}
	for name, bad := range map[string]string{
		"guid":     `{"framing":null}`,
		"empty":    `{"target_guid":"g","framing":{}}`,
		"half":     `{"target_guid":"g","framing":{"rotation_deg":10}}`,
		"rotation": `{"target_guid":"g","framing":{"rotation_deg":360,"offset_frac":0}}`,
		"negative": `{"target_guid":"g","framing":{"rotation_deg":-1,"offset_frac":0}}`,
		"offset":   `{"target_guid":"g","framing":{"rotation_deg":10,"offset_frac":0.51}}`,
	} {
		if err := s.Validate(json.RawMessage(bad)); !errors.Is(err, schedcmd.ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
	centred := `{"target_guid":"g","framing":{"rotation_deg":10,"offset_frac":0}}`
	d, _ = s.Describe(json.RawMessage(centred))
	_ = json.Unmarshal(d.Diffs[0].After, &after)
	if !strings.Contains(after, "body centred") {
		t.Errorf("centred %q", after)
	}
}

func TestProjectCreateChecksTheFraming(t *testing.T) {
	t.Parallel()
	s := spec(t, schedcmd.KindProjectCreate)
	with := func(f func(*schedcmd.NewTarget)) json.RawMessage {
		var v schedcmd.ProjectCreatePayload
		if err := json.Unmarshal([]byte(samples()[schedcmd.KindProjectCreate]), &v); err != nil {
			t.Fatal(err)
		}
		f(&v.Targets[0])
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	fp := func(v float64) *float64 { return &v }
	const hartley = "161P"
	ok := with(func(t *schedcmd.NewTarget) { t.Designation, t.RotationDeg, t.OffsetFrac = hartley, fp(84.1), fp(-0.25) })
	if err := s.Validate(ok); err != nil {
		t.Fatalf("framed comet: %v", err)
	}
	for name, raw := range map[string]json.RawMessage{
		"no designation": with(func(t *schedcmd.NewTarget) { t.RotationDeg, t.OffsetFrac = fp(84.1), fp(-0.25) }),
		"half":           with(func(t *schedcmd.NewTarget) { t.Designation, t.RotationDeg = hartley, fp(84.1) }),
		"offset":         with(func(t *schedcmd.NewTarget) { t.Designation, t.RotationDeg, t.OffsetFrac = hartley, fp(84.1), fp(0.7) }),
	} {
		if err := s.Validate(raw); !errors.Is(err, schedcmd.ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
}
