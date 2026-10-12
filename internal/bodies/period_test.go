package bodies_test

import (
	"math"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/bodies"
)

func TestSameDesignation(t *testing.T) {
	t.Parallel()
	for _, p := range [][2]string{{"161P", "0161P"}, {"161P/Hartley-IRAS", "161p"}, {swan, "c/2025r2"}, {"2P", "0002P"}} {
		if !bodies.SameDesignation(p[0], p[1]) {
			t.Errorf("%q and %q differ", p[0], p[1])
		}
	}
	for _, p := range [][2]string{{swan, "C/2099 Z9"}, {"2P", "12P"}, {"", ""}} {
		if bodies.SameDesignation(p[0], p[1]) {
			t.Errorf("%q and %q match", p[0], p[1])
		}
	}
}

func TestParseSBDBOrbit(t *testing.T) {
	t.Parallel()
	o, err := bodies.ParseSBDBOrbit("4 Vesta", `{"orbit":{"elements":[{"name":"e","value":"0.0894"},{"name":"per","value":"1325.8"}]}}`)
	if err != nil || o.PeriodYears == nil || math.Abs(*o.PeriodYears-3.63) > 0.01 || o.E != 0.0894 {
		t.Fatalf("%+v %v", o, err)
	}
	o, err = bodies.ParseSBDBOrbit("C/2026 A1", `{"orbit":{"elements":[{"name":"e","value":"1.0003"},{"name":"per","value":null}]}}`)
	if err != nil || o.PeriodYears != nil {
		t.Fatalf("open orbit: %+v %v", o, err)
	}
	if _, err := bodies.ParseSBDBOrbit("x", `{"message":"specified object was not found"}`); err == nil {
		t.Error("no orbit read as one")
	}
}
