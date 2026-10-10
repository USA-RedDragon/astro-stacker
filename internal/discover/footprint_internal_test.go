package discover

import (
	"math"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
)

func testField(ra, dec float64) field {
	d := 2.0 / 3600
	w := goals.WCS{RA0: ra, Dec0: dec, PX0: 1800.5, PY0: 1800.5, CD: [2][2]float64{{-d, 0}, {0, d}}}
	return newWCSField(FieldFrames, "t", w, 0.5, 0.5, 3600.5, 3600.5)
}

func TestCoverageOfAnObjectOnTheFrameEdge(t *testing.T) {
	t.Parallel()
	f := testField(100, 0)
	if math.Abs(f.radius-math.Sqrt2) > 0.01 {
		t.Fatalf("a 2° square field has radius %v", f.radius)
	}
	half := catalog.Object{RA: 101, Dec: 0, MajorArcmin: 60}
	c := measureCover(half, []field{f})
	if c.centre || math.Abs(c.fraction-0.5) > 0.06 || !c.hit() || len(c.targets) != 1 {
		t.Errorf("object centred on the edge %+v", c)
	}
	sliver := catalog.Object{RA: 101.4, Dec: 0, MajorArcmin: 60}
	if c := measureCover(sliver, []field{f}); c.hit() || c.fraction <= 0 || c.fraction >= minCoverage {
		t.Errorf("a sliver of an object %+v", c)
	}
	point := catalog.Object{RA: 100.5, Dec: 0.5}
	if c := measureCover(point, []field{f}); !c.centre || c.fraction != 1 {
		t.Errorf("a point object inside %+v", c)
	}
	big := catalog.Object{RA: 100, Dec: 0, MajorArcmin: 600}
	if c := measureCover(big, []field{f}); !c.centre || c.fraction > 0.06 {
		t.Errorf("an object far larger than the frame %+v", c)
	}
	far := catalog.Object{RA: 120, Dec: 0, MajorArcmin: 10}
	if c := measureCover(far, []field{f}); c.hit() || math.Abs(c.nearest-20) > 0.01 {
		t.Errorf("an object 20° away %+v", c)
	}
}

func TestPointingAndPlannedFields(t *testing.T) {
	t.Parallel()
	minor, pa := 10.0, 90.0
	o := catalog.Object{RA: 50, Dec: 30, MajorArcmin: 30, MinorArcmin: &minor, PA: &pa}
	if c := measureCover(o, []field{pointingField("p", 50.2, 30)}); !c.pointing || !c.hit() {
		t.Errorf("pointing along the major axis %+v", c)
	}
	if c := measureCover(o, []field{pointingField("p", 50, 30.2)}); c.hit() {
		t.Errorf("pointing off the minor axis %+v", c)
	}
	o.PA = nil
	if a, b := semiAxes(o); a != b || math.Abs(a*a-(15.0/60)*(5.0/60)) > 1e-12 {
		t.Errorf("an ellipse with no position angle becomes a circle of equal area: %v %v", a, b)
	}
	frame := sky.Frame{FocalLength: 405, PixelSize: 3.76, WidthPx: 6248, HeightPx: 4176}
	wide, ok := planField("w", 50, 30, 0, frame)
	if !ok || !wide.contains(51.5, 30) || wide.contains(50, 31.5) {
		t.Errorf("unrotated plan %+v", wide)
	}
	turned, _ := planField("w", 50, 30, 90, frame)
	if turned.contains(51.5, 30) || !turned.contains(50, 31.5) {
		t.Errorf("plan turned 90° %+v", turned)
	}
	if _, ok := planField("w", 50, 30, 0, sky.Frame{}); ok {
		t.Error("a plan without a rig")
	}
}
