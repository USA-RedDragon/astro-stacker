package stacking

import (
	"slices"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestWorkersClaimDifferentTargets(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.Frame{}, &app.StackFrame{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for i, f := range []struct{ object, filter string }{
		{"A", "Red"}, {"A", "Blue"}, {"B", "Red"}, {"C", "H-a"},
	} {
		d := now.Add(time.Duration(i) * time.Minute)
		if err := db.Create(&app.Frame{Key: f.object + f.filter, Type: "LIGHT", Object: f.object, Filter: f.filter,
			LastModified: now, DateObs: &d}).Error; err != nil {
			t.Fatal(err)
		}
	}
	p := &Pipeline{db: db, busy: map[string]bool{}}
	lights := func() *gorm.DB {
		return db.Model(&app.Frame{}).Joins("LEFT JOIN stack_frames sf ON sf.frame_id = frames.id").
			Where("frames.type = ? AND sf.id IS NULL", "LIGHT")
	}
	var got []string
	for range 4 {
		f, err := p.claim(lights())
		if err != nil {
			t.Fatal(err)
		}
		if f == nil {
			got = append(got, "-")
			continue
		}
		got = append(got, f.Object)
	}
	// A's second filter must wait for A to be released.
	if want := []string{"A", "B", "C", "-"}; !slices.Equal(got, want) {
		t.Fatalf("claimed %v, want %v", got, want)
	}
	p.release("A")
	if f, _ := p.claim(lights()); f == nil || f.Object != "A" {
		t.Fatalf("after release claimed %v, want A", f)
	}
}
