package bodies_test

import (
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/bodies"
)

func TestTargetsKeyIgnoresOrderAndSeesChanges(t *testing.T) {
	t.Parallel()
	base := func() []bodies.Target {
		return []bodies.Target{
			{GUID: "b", Designation: "C/2024 T5", Kind: "comet", Active: true},
			{GUID: "a", Designation: "4", Kind: "asteroid", Active: true},
		}
	}
	a := base()
	if bodies.TargetsKey(a) != bodies.TargetsKey([]bodies.Target{a[1], a[0]}) {
		t.Fatal("order changed the key")
	}
	c := base()
	c[0].Designation = "C/2099 Z9"
	if bodies.TargetsKey(a) == bodies.TargetsKey(c) {
		t.Fatal("designation change not seen")
	}
	d := base()
	d = append(d, bodies.Target{GUID: "c", Designation: "2", Kind: "asteroid", Active: true})
	if bodies.TargetsKey(a) == bodies.TargetsKey(d) {
		t.Fatal("added target not seen")
	}
	e := base()
	e[1].Active = false
	if bodies.TargetsKey(a) == bodies.TargetsKey(e) {
		t.Fatal("deactivation not seen")
	}
}

func TestRefreshDue(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 12, 16, 20, 0, 0, time.UTC)
	if bodies.RefreshDue(now, now.Add(time.Hour), "k", "k") {
		t.Fatal("refreshed with no change before the interval")
	}
	if !bodies.RefreshDue(now, now.Add(time.Hour), "k2", "k") {
		t.Fatal("a target change did not refresh")
	}
	if !bodies.RefreshDue(now, now, "k", "k") {
		t.Fatal("the interval did not refresh")
	}
	if !bodies.RefreshDue(now, time.Time{}, "k", "k") {
		t.Fatal("the first pass did not refresh")
	}
}
