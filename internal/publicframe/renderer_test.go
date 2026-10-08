package publicframe

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestChoose(t *testing.T) {
	cands := []Candidate{{FrameID: 3}, {FrameID: 2}, {FrameID: 1}, {FrameID: 0}}
	ids := func(cs []Candidate) []int {
		out := []int{}
		for _, c := range cs {
			out = append(out, c.FrameID)
		}
		return out
	}
	for _, tc := range []struct {
		name   string
		cur    *app.PublicFrame
		failed map[int]bool
		want   []int
	}{
		{"nothing shown: newest, then fallbacks", nil, nil, []int{3, 2, 1}},
		{"newest shown already", &app.PublicFrame{FrameID: 3, Revision: Revision}, nil, []int{}},
		{"newest shown at an older revision", &app.PublicFrame{FrameID: 3, Revision: Revision - 1}, nil, []int{3, 2, 1}},
		{"newer light than the one shown; stop at it", &app.PublicFrame{FrameID: 2, Revision: Revision}, nil, []int{3}},
		{"newest failed: previous good one", nil, map[int]bool{3: true}, []int{2, 1, 0}},
		{"newest failed, previous shown: keep it", &app.PublicFrame{FrameID: 2, Revision: Revision}, map[int]bool{3: true}, []int{}},
	} {
		if got := ids(choose(cands, tc.cur, tc.failed)); !equal(got, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func equal(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestPass checks which light each target shows: its newest accepted one,
// never a low-score, moon or off-target one, falling back to the previous
// good one when the newest won't render or leaves its master.
func TestPass(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.Frame{}, &app.StackFrame{}, &app.Stack{}, &app.PublicFrame{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	stacks := map[string]*app.Stack{}
	light := func(object, name, status string, age time.Duration) int {
		s, ok := stacks[object]
		if !ok {
			s = &app.Stack{Object: object, Filter: "L"}
			if err := db.Create(s).Error; err != nil {
				t.Fatal(err)
			}
			stacks[object] = s
		}
		at := now.Add(-age)
		f := app.Frame{Key: object + "/" + name, ETag: name, Type: "LIGHT", Object: object, Filter: "L", DateObs: &at}
		if err := db.Create(&f).Error; err != nil {
			t.Fatal(err)
		}
		reg := "registered/" + object + "/" + name + ".fit"
		sf := app.StackFrame{FrameID: f.ID, Status: status}
		if status == app.StackStatusAdded {
			sf.StackID, sf.RegisteredKey = &s.ID, &reg
		}
		if err := db.Create(&sf).Error; err != nil {
			t.Fatal(err)
		}
		return f.ID
	}
	a1 := light("A", "a1", app.StackStatusAdded, 3*time.Hour)
	a0 := light("A", "a0", app.StackStatusAdded, 4*time.Hour)
	light("A", "a2", app.StackStatusLowScore, 2*time.Hour)
	light("A", "a3", app.StackStatusMoon, time.Hour)
	light("A", "a4", app.StackStatusOffTarget, 30*time.Minute)
	b1 := light("B", "b1", app.StackStatusAdded, 2*time.Hour)
	b2 := light("B", "b2", app.StackStatusAdded, time.Hour)
	light("C", "c1", app.StackStatusAdded, 60*24*time.Hour) // not imaged lately
	d1 := light("D", "d1", app.StackStatusAdded, 60*24*time.Hour)

	r := NewRenderer(nil, "processed", db, DefaultOptions(), 14*24*time.Hour)
	var rendered []int
	broken := map[int]bool{b2: true}
	r.render = func(_ context.Context, c Candidate) (Result, error) {
		if broken[c.FrameID] {
			return Result{}, errors.New("registered sub missing")
		}
		rendered = append(rendered, c.FrameID)
		return Result{JPEG: []byte{byte(c.FrameID)}, Sigma: 5, Amplitude: 2.5}, nil
	}
	// D was shown before; its light has since been rejected.
	if err := db.Create(&app.PublicFrame{Object: "D", FrameID: d1, Key: Key("D", d1), ETag: `"x"`, Revision: Revision}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&app.StackFrame{}).Where("frame_id = ?", d1).Update("status", app.StackStatusMoon).Error; err != nil {
		t.Fatal(err)
	}

	shown := func() map[string]int {
		var fs []app.PublicFrame
		if err := db.Find(&fs).Error; err != nil {
			t.Fatal(err)
		}
		out := map[string]int{}
		for _, f := range fs {
			out[f.Object] = f.FrameID
		}
		return out
	}

	ctx := context.Background()
	if _, err := r.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	got := shown()
	if got["A"] != a1 {
		t.Errorf("A shows %d, want its newest accepted light %d", got["A"], a1)
	}
	if got["B"] != b1 {
		t.Errorf("B shows %d, want %d, the previous good one, its newest not rendering", got["B"], b1)
	}
	if _, ok := got["C"]; ok {
		t.Error("C rendered though not imaged within max age")
	}
	if _, ok := got["D"]; ok {
		t.Error("D still shows a light that left its master")
	}

	// Nothing changed: nothing renders.
	rendered = nil
	if n, err := r.Pass(ctx); err != nil || n != 0 || len(rendered) != 0 {
		t.Errorf("second pass rendered %d (%v), err %v", n, rendered, err)
	}

	// A's light is rescored out of its master: A falls back to a0.
	if err := db.Model(&app.StackFrame{}).Where("frame_id = ?", a1).Update("status", app.StackStatusLowScore).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := r.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if got := shown(); got["A"] != a0 {
		t.Errorf("A shows %d after its light left the master, want %d", got["A"], a0)
	}
	var f app.PublicFrame
	if err := db.Where("object = ?", "A").First(&f).Error; err != nil {
		t.Fatal(err)
	}
	if f.Key != Key("A", a0) || f.ETag == "" || f.Revision != Revision || f.DateObs == nil {
		t.Errorf("stored frame %+v", f)
	}
}
