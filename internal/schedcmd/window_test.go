package schedcmd_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
)

func eventCreate(t *testing.T, mutate func(*schedcmd.NewTarget)) json.RawMessage {
	t.Helper()
	var p schedcmd.ProjectCreatePayload
	if err := json.Unmarshal([]byte(samples()[schedcmd.KindProjectCreate]), &p); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 13, 5, 0, 0, 0, time.UTC)
	p.Targets[0].Window = &schedcmd.EventWindow{Start: start, End: start.Add(31 * time.Hour), Priority: schedcmd.EventPriorityNormal, EventID: "e1"}
	mutate(&p.Targets[0])
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestProjectCreateWithoutEventFieldsKeepsItsShape(t *testing.T) {
	t.Parallel()
	var p schedcmd.ProjectCreatePayload
	if err := json.Unmarshal([]byte(samples()[schedcmd.KindProjectCreate]), &p); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(p.Targets[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"window", "designation", `"kind"`} {
		if strings.Contains(string(b), field) {
			t.Errorf("an ordinary target sends %s: %s", field, b)
		}
	}
}

func TestProjectCreateValidatesTheEventWindow(t *testing.T) {
	t.Parallel()
	s := spec(t, schedcmd.KindProjectCreate)
	ok := eventCreate(t, func(*schedcmd.NewTarget) {})
	if err := s.Validate(ok); err != nil {
		t.Fatalf("valid window rejected: %v", err)
	}
	d, err := s.Describe(ok)
	if err != nil || !strings.Contains(d.Note, "fills gaps") {
		t.Fatalf("note %q, %v", d.Note, err)
	}
	moving := eventCreate(t, func(nt *schedcmd.NewTarget) { nt.Designation, nt.BodyKind = "event:e1", schedcmd.BodyKindEvent })
	if err := s.Validate(moving); err != nil {
		t.Fatalf("moving centre rejected: %v", err)
	}
	for name, bad := range map[string]func(*schedcmd.NewTarget){
		"end before start": func(nt *schedcmd.NewTarget) { nt.Window.End = nt.Window.Start.Add(-time.Hour) },
		"too long":         func(nt *schedcmd.NewTarget) { nt.Window.End = nt.Window.Start.Add(500 * 24 * time.Hour) },
		"priority":         func(nt *schedcmd.NewTarget) { nt.Window.Priority = "urgent" },
		"kind":             func(nt *schedcmd.NewTarget) { nt.Designation, nt.BodyKind = "x", "planet" },
		"kind without des": func(nt *schedcmd.NewTarget) { nt.BodyKind = schedcmd.BodyKindEvent },
		"long designation": func(nt *schedcmd.NewTarget) { nt.Designation = strings.Repeat("x", 201) },
	} {
		if err := s.Validate(eventCreate(t, bad)); !errors.Is(err, schedcmd.ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
}

func TestTargetWindowDescribesAndSwapsOnUndo(t *testing.T) {
	t.Parallel()
	s := spec(t, schedcmd.KindTargetWindow)
	raw := json.RawMessage(samples()[schedcmd.KindTargetWindow])
	d, err := s.Describe(raw)
	if err != nil {
		t.Fatal(err)
	}
	var before, after string
	_ = json.Unmarshal(d.Diffs[0].Before, &before)
	_ = json.Unmarshal(d.Diffs[0].After, &after)
	if d.Title != "Mars with M 44 · event window" || !strings.Contains(before, "fills gaps") || !strings.Contains(after, "high priority") {
		t.Fatalf("%+v %q %q", d, before, after)
	}
	k, inv, err := s.Inverse(raw)
	if err != nil || k != schedcmd.KindTargetWindow {
		t.Fatal(k, err)
	}
	var p schedcmd.TargetWindowPayload
	if err := json.Unmarshal(inv, &p); err != nil {
		t.Fatal(err)
	}
	if p.Window.Priority != schedcmd.EventPriorityNormal || p.Before.Priority != schedcmd.EventPriorityHigh {
		t.Fatalf("undo %+v", p)
	}
	if err := s.Validate(json.RawMessage(`{"target_guid":"","window":null}`)); !errors.Is(err, schedcmd.ErrInvalid) {
		t.Errorf("missing guid: %v", err)
	}
	if err := s.Validate(json.RawMessage(`{"target_guid":"g","window":null}`)); err != nil {
		t.Errorf("clearing: %v", err)
	}
}
