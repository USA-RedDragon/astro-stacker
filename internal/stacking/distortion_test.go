package stacking

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

const cameraFLI = "FLI"

// Only calibrated subs from the reference's camera are registered with its
// distortion, and only when the reference has one.
func TestRegisterWithDistortion(t *testing.T) {
	t.Parallel()
	ref := app.Frame{Key: "Telescope.live/Rho/AUS-2-CCD_Luminance_ID222389_cal.fits", Camera: cameraFLI}
	tl := candidate{frame: app.Frame{Key: "Telescope.live/Rho/AUS-2-CCD_Red_ID224166_cal.fits", Camera: cameraFLI}}
	other := candidate{frame: app.Frame{Key: "Telescope.live/Rho/CHI-1-CMOS_Red_ID1_cal.fits", Camera: "QHY"}}
	nina := candidate{frame: app.Frame{Key: "Rho/LIGHT/2025-05-01_Red_0001.xisf", Camera: cameraFLI}}
	solved := app.TargetReference{Registration: registrationDistortion}
	for _, c := range []struct {
		name  string
		tr    app.TargetReference
		batch []candidate
		want  bool
	}{
		{"calibrated subs", solved, []candidate{tl, tl}, true},
		{"reference registered by stars", app.TargetReference{Registration: registrationStars}, []candidate{tl}, false},
		{"reference from before", app.TargetReference{}, []candidate{tl}, false},
		{"another camera", solved, []candidate{tl, other}, false},
		{"our own subs", solved, []candidate{nina}, false},
	} {
		if got := registerWithDistortion(c.tr, ref, c.batch); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	if cmd := registerCommand(true); !strings.Contains(cmd, "-disto=file seq_00001.fit") {
		t.Errorf("register with distortion: %q", cmd)
	}
	if cmd := registerCommand(false); strings.Contains(cmd, "-disto") {
		t.Errorf("register by stars: %q", cmd)
	}
}

// register -disto needs SIP terms beyond the linear.
func TestHasDistortion(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for name, want := range map[string]bool{"3": true, "1": false, "": false} {
		cards := []imagedata.Card{imagedata.StringCard("CTYPE1", "RA---TAN-SIP", ""), imagedata.StringCard("CTYPE2", "DEC--TAN-SIP", "")}
		if name != "" {
			cards = append(cards, imagedata.Card{Key: "A_ORDER", Value: name})
		}
		file := filepath.Join(dir, "order"+name+".fit")
		f, err := os.Create(file)
		if err != nil {
			t.Fatal(err)
		}
		if err := imagedata.WriteFITS(f, 4, 4, 1, make([]float32, 16), cards); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		if got := hasDistortion(file); got != want {
			t.Errorf("A_ORDER %q: distortion %v, want %v", name, got, want)
		}
	}
}

// Targets whose reference came calibrated, chosen before registration could
// use the distortion, are stacked again from scratch; others are left.
func TestReregisterPrecalibrated(t *testing.T) {
	t.Parallel()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.Frame{}, &app.StackFrame{}, &app.Stack{}, &app.TargetReference{}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		object, key, registration string
	}{
		{objectRhoOphiuchi, "Telescope.live/Rho Ophiuchi/AUS-2-CCD_Red_ID224166_cal.fits", ""},
		{"Carina Nebula", "Telescope.live/Carina Nebula/CHI-1-CCD_Halpha_ID224182_cal.fits", registrationDistortion},
		{objectM31, "M31/LIGHT/2025-10-01_Red_0001.xisf", ""},
	} {
		f := app.Frame{Key: c.key, Object: c.object, Type: frameTypeLight, Filter: filterRed, LastModified: time.Now()}
		if err := db.Create(&f).Error; err != nil {
			t.Fatal(err)
		}
		s := app.Stack{Object: c.object, Filter: filterRed}
		if err := db.Create(&s).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&app.StackFrame{FrameID: f.ID, StackID: &s.ID, Status: app.StackStatusAdded}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&app.TargetReference{Object: c.object, FrameID: f.ID, ObjectKey: "ref", Registration: c.registration}).Error; err != nil {
			t.Fatal(err)
		}
	}
	p := &Pipeline{db: db, workDir: t.TempDir(), busy: map[string]bool{}}
	p.reregisterPrecalibrated(context.Background())
	var left []string
	if err := db.Model(&app.TargetReference{}).Order("object").Pluck("object", &left).Error; err != nil {
		t.Fatal(err)
	}
	if strings.Join(left, ",") != "Carina Nebula,M31" {
		t.Errorf("references left %v, want Carina Nebula and M31", left)
	}
	var stacks int64
	if err := db.Model(&app.Stack{}).Where("object = ?", objectRhoOphiuchi).Count(&stacks).Error; err != nil {
		t.Fatal(err)
	}
	if stacks != 0 {
		t.Error("Rho Ophiuchi's masters were kept")
	}
}
