package stacking

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/astrometry"
	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/siril"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

const (
	cardFocal = "FOCALLEN"
	cardPixel = "XPIXSZ"
	keySPA2   = "Telescope.live/M109/SPA-2-CCD_2021-06-08T22-41-41_M109_Luminance_300s_ID227326_cal.fits"
)

// kws reads "NAME=value;NAME=value" into keywords.
func kws(s string) frameheader.Keywords {
	kw := frameheader.Keywords{}
	for pair := range strings.SplitSeq(s, ";") {
		if name, value, ok := strings.Cut(pair, "="); ok {
			kw[name] = value
		}
	}
	return kw
}

// spa2 is the header of a Telescope.live SPA-2 sub: binned 2x2, XPIXSZ
// after binning, no FOCALLEN and no plate solution.
func spa2() frameheader.Keywords {
	return kws("TELESCOP=SPA-2;INSTRUME=FLI PL16803;XPIXSZ=18.000000000000000;XBINNING=2;" +
		"DATE-OBS=2021-06-08T22:41:41.77;OBJCTRA=11 57 35.98;OBJCTDEC=53 22 28.20")
}

func TestKnownTelescope(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		kw    frameheader.Keywords
		key   string
		scope string
		focal float64
	}{
		{"file name", kws("TELESCOP=;INSTRUME=FLI"), "Telescope.live/Squid/SPA-3-CCD_2021-05-14T01-47-09_x_cal.fits", "SPA-3-CCD", 382},
		{"TELESCOP and INSTRUME", kws("TELESCOP=Takahashi FSQ-106ED;INSTRUME=FLI PL16803"), "NGC292/x.fits", "AUS-2-CCD", 382},
		{"CMOS", kws("TELESCOP=AUS-2-CMOS;INSTRUME=QHY 600M"), "x.fits", "AUS-2-CMOS", 530},
		{"SPA-2 at F8", spa2(), keySPA2, "SPA-2-CCD", 5600},
		{"SPA-2 with its reducer", kws("TELESCOP=SPA-2;INSTRUME=FLI PL16803;DATE-OBS=2021-04-01T00:00:00"), "x.fits", "SPA-2-CCD", 4267},
	} {
		got, ok := knownTelescope(c.kw, c.key)
		if !ok || got.Scope != c.scope || got.FocalMM != c.focal {
			t.Errorf("%s: %+v %v, want %s at %v mm", c.name, got, ok, c.scope, c.focal)
		}
	}
	undated := spa2()
	delete(undated, "DATE-OBS")
	if got, _ := knownTelescope(undated, keySPA2); got.FocalMM != 5600 {
		t.Errorf("undated SPA-2: %v mm, want the latest setup's 5600", got.FocalMM)
	}
	zulu := spa2()
	zulu["DATE-OBS"] = "2021-04-01T00:00:00Z"
	if got, _ := knownTelescope(zulu, "x.fits"); got.FocalMM != 4267 {
		t.Errorf("SPA-2 dated with Z: %v mm, want 4267", got.FocalMM)
	}
	if got, ok := knownTelescope(kws("TELESCOP=;INSTRUME=FLI;DATE-OBS=2024-01-01T00:00:00"), "Telescope.live/x/AUS-2-CCD_2024_cal.fits"); !ok || got.Scope != "AUS-2-CCD" {
		t.Errorf("one setup by file name, outside its dates: %+v %v", got, ok)
	}
	if got, ok := knownTelescope(kws("TELESCOP=Apertura 75Q;INSTRUME=ZWO ASI2600MM Pro"), "M31/LIGHT/a.xisf"); ok {
		t.Errorf("our own scope matched %+v", got)
	}
	if got, ok := knownTelescope(kws("TELESCOP=SPA-1;INSTRUME=QHY 600M"), "x.fits"); ok {
		t.Errorf("SPA-1 with another camera matched %+v", got)
	}
}

// Every setup's published optics give the pixel scale its solved masters
// were measured at, within 3%, and every one cites where it came from.
func TestTelescopeTableMatchesSolvedMasters(t *testing.T) {
	t.Parallel()
	var doc struct {
		Telescopes []struct {
			Scope     string  `json:"scope"`
			FocalMM   float64 `json:"focal_mm"`
			PixelUM   float64 `json:"pixel_um"`
			Published float64 `json:"scale_arcsec"`
			Measured  float64 `json:"measured_arcsec"`
			Bin       float64 `json:"measured_bin"`
			Source    string  `json:"source"`
		} `json:"telescopes"`
	}
	if err := json.Unmarshal(telescopesJSON, &doc); err != nil {
		t.Fatal(err)
	}
	measured := 0
	for _, s := range doc.Telescopes {
		if !strings.HasPrefix(s.Source, "https://") {
			t.Errorf("%s has no source", s.Scope)
		}
		if s.Measured == 0 {
			continue
		}
		measured++
		bin := max(1, s.Bin)
		if got := pixelScale(s.FocalMM, s.PixelUM*bin); math.Abs(got/s.Measured-1) > 0.03 {
			t.Errorf("%s: %.0f mm with %.2f µm gives %.3f\"/px, measured %.3f", s.Scope, s.FocalMM, s.PixelUM*bin, got, s.Measured)
		}
	}
	if measured < 8 {
		t.Errorf("only %d setups checked against solved masters", measured)
	}
}

func TestFrameOptics(t *testing.T) {
	t.Parallel()
	focal, pixel, src := frameOptics(spa2(), keySPA2)
	if focal != 5600 || pixel != 18 || !strings.Contains(src, "SPA-2-CCD") {
		t.Errorf("SPA-2: %v mm %v µm from %q; want 5600 mm and the binned 18 µm, published", focal, pixel, src)
	}
	cmos := kws("TELESCOP=CHI-1-CMOS;INSTRUME=QHY 600M;XBINNING=2;DATE-OBS=2023-05-01T00:00:00")
	if focal, pixel, _ := frameOptics(cmos, "x.fits"); focal != 3962 || math.Abs(pixel-7.52) > 1e-9 {
		t.Errorf("CHI-1-CMOS: %v mm %v µm, want 3962 mm and 7.52 µm binned", focal, pixel)
	}
	solved := kws("TELESCOP=AUS-2;INSTRUME=FLI PL16803;XPIXSZ=9;FOCALLEN=0;CTYPE1=RA---TAN-SIP;CTYPE2=DEC--TAN-SIP;" +
		"CD1_1=-3.03066414149E-05;CD1_2=0.0013240730461;CD2_1=-0.00132017700099;CD2_2=-3.46093717871E-05")
	if focal, pixel, src := frameOptics(solved, "x.fits"); math.Abs(focal-389.8) > 1 || pixel != 9 || src != opticsSolution {
		t.Errorf("solved AUS-2: %v mm %v µm from %q; the sub's own solution comes first", focal, pixel, src)
	}
	nina := kws("FOCALLEN=405;XPIXSZ=3.76;XBINNING=1")
	if focal, pixel, src := frameOptics(nina, "M31/LIGHT/a.xisf"); focal != 405 || pixel != 3.76 || src != opticsHeader {
		t.Errorf("NINA: %v %v %q", focal, pixel, src)
	}
	if focal, pixel, _ := frameOptics(frameheader.Keywords{}, "x.fits"); focal != 0 || pixel != 0 {
		t.Errorf("nothing known: %v %v", focal, pixel)
	}
}

func cardMap(cards []imagedata.Card) map[string]imagedata.Card {
	m := map[string]imagedata.Card{}
	for _, c := range cards {
		m[c.Key] = c
	}
	return m
}

func TestDerivedOptics(t *testing.T) {
	t.Parallel()
	got := cardMap(derivedOptics(spa2(), keySPA2, nil))
	if f := got[cardFocal]; f.Value != "5600" || !strings.HasPrefix(f.Comment, "[mm] derived: Telescope.live SPA-2-CCD") {
		t.Errorf("SPA-2 unsolved: %+v", got)
	}
	if _, ok := got[cardPixel]; ok {
		t.Errorf("SPA-2 has its own XPIXSZ: %+v", got)
	}
	solution := keywordCards(kws("CTYPE1=RA---TAN-SIP;CTYPE2=DEC--TAN-SIP;CD1_1=0.000402796620827;CD1_2=5.9E-05;CD2_1=-5.91583433412E-05;CD2_2=0.0004028"))
	cmos := kws("TELESCOP=AUS-2-CMOS;INSTRUME=QHY 600M;XBINNING=1")
	got = cardMap(derivedOptics(cmos, "x.fits", solution))
	if p := got[cardPixel]; p.Value != "3.76" || !strings.Contains(p.Comment, "derived") {
		t.Errorf("AUS-2-CMOS XPIXSZ: %+v", p)
	}
	focal, _ := solutionOptics(cardKeywords(solution), 3.76)
	if want := fmt.Sprintf("%.10g", math.Round(focal*10)/10); got[cardFocal].Value != want || !strings.Contains(got[cardFocal].Comment, "plate solution") {
		t.Errorf("AUS-2-CMOS FOCALLEN %+v, want %s from the solution", got[cardFocal], want)
	}
	if out := derivedOptics(kws("FOCALLEN=405;XPIXSZ=3.76"), "a.xisf", nil); out != nil {
		t.Errorf("complete header got %+v", out)
	}
	if out := derivedOptics(kws("TELESCOP=Unknown"), "a.fits", solution); out != nil {
		t.Errorf("unknown telescope got %+v", out)
	}
}

// Optics read from another light depend on its filter; the fingerprint
// doesn't, so a target's filters don't reset each other's tries.
func TestFingerprintSameForEveryFilter(t *testing.T) {
	t.Parallel()
	l := solveInput{ra: 10, dec: 20, focal: 405, pixel: 3.76, optics: opticsOtherLight, solver: "s"}
	rgb := l
	rgb.pixel = 7.52
	if l.fingerprint() != rgb.fingerprint() {
		t.Errorf("%q != %q", l.fingerprint(), rgb.fingerprint())
	}
	ref := l
	ref.optics = opticsHeader
	if ref.fingerprint() == l.fingerprint() {
		t.Error("optics from the reference not in the fingerprint")
	}
}

func TestRepublishPlanRewrites(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		plan    republishPlan
		version int
		want    bool
	}{
		{republishPlan{}, MasterVersion, false},
		{republishPlan{}, headerOnlyVersion, false},
		{republishPlan{derive: true}, headerOnlyVersion, true},
		{republishPlan{derive: true}, MasterVersion, false},
		{republishPlan{solve: true}, MasterVersion, true},
		{republishPlan{}, 1, true},
		{republishPlan{}, 0, true},
	} {
		if got := c.plan.rewrites(c.version); got != c.want {
			t.Errorf("%+v at version %d: %v, want %v", c.plan, c.version, got, c.want)
		}
	}
}

// fakeSolveField answers like solve-field with a solution, and records that
// it ran and with what.
func fakeSolveField(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	var header string
	for _, l := range []string{"SIMPLE  = T", "WCSAXES = 2", "CTYPE1  = 'RA---TAN-SIP'", "CTYPE2  = 'DEC--TAN-SIP'", "CRVAL1  = 179.38296",
		"CRVAL2  = 53.31762", "CRPIX1  = 1090.14", "CRPIX2  = 1331.68", "CD1_1   = -0.000183364", "CD1_2   = 5.261E-06",
		"CD2_1   = -5.646E-06", "CD2_2   = -0.000183132", "IMAGEW  = 8", "END"} {
		header += fmt.Sprintf("%-80s", l)
	}
	script := fmt.Sprintf("#!/bin/sh\necho \"$@\" > %q\nout=\nwhile [ $# -gt 1 ]; do [ \"$1\" = --dir ] && out=$2; shift; done\nprintf '%%s' %q > \"$out/$(basename \"$1\" .fit).wcs\"\n", record, header)
	cmd := filepath.Join(dir, "solve-field")
	if err := os.WriteFile(cmd, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return cmd, record
}

func spa2Cards() []frameheader.Card {
	return keywordCards(spa2())
}

func keywordCards(kw frameheader.Keywords) []frameheader.Card {
	cards := make([]frameheader.Card, 0, len(kw))
	for name, value := range kw {
		cards = append(cards, frameheader.Card{Name: name, Value: value})
	}
	return cards
}

// A master given up on before its telescope's optics were known, or before
// a local solver existed, is solved again once those inputs change; one
// given up on with the same inputs is left alone.
func TestPlateSolveRetriesChangedInputs(t *testing.T) {
	t.Parallel()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.TargetReference{}, &app.Frame{}); err != nil {
		t.Fatal(err)
	}
	cmd, record := fakeSolveField(t)
	solver := &astrometry.Solver{Command: cmd, IndexDir: "/astrometry", CPUSeconds: 60, Timeout: time.Minute}
	solver.MarkReady()
	p := &Pipeline{db: db, workDir: t.TempDir(), siril: siril.Runner{Command: "false"}, opts: PipelineOptions{Astrometry: solver}}
	dir := t.TempDir()
	master := filepath.Join(dir, "master.fit")
	f, err := os.Create(master)
	if err != nil {
		t.Fatal(err)
	}
	if err := imagedata.WriteFITS(f, 8, 8, 1, make([]float32, 64), nil); err != nil {
		t.Fatal(err)
	}
	f.Close()

	gaveUp := "Telescope.live/M109/SPA-2-CCD_x_cal.fits has no FOCALLEN or XPIXSZ"
	tr := app.TargetReference{Object: "M 109", FrameID: 1, ObjectKey: "stacks/M 109/ref.fit", SolveAttempts: 3, SolveError: &gaveUp}
	if err := db.Create(&tr).Error; err != nil {
		t.Fatal(err)
	}
	stack := &app.Stack{Object: "M 109", Filter: "Luminance"}
	cards := p.plateSolution(context.Background(), &tr, stack, keySPA2, spa2Cards(), dir, master)
	if !hasCard(cards, "CRVAL1") || !hasCard(cards, "PLTSOLVD") {
		t.Fatalf("given-up master not solved again: %+v", cards)
	}
	var saved app.TargetReference
	if err := db.First(&saved, tr.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.WCS == nil || saved.SolveAttempts != 1 || saved.SolveError != nil ||
		!strings.Contains(saved.SolveInputs, "focal=5600.0 pixel=18.00 optics=Telescope.live SPA-2-CCD published optics solver=astrometry.net:") {
		t.Errorf("saved %+v inputs %q", saved, saved.SolveInputs)
	}
	args, _ := os.ReadFile(record)
	if !strings.Contains(string(args), "--ra 179.39992 --dec 53.37450") || !strings.Contains(string(args), "--scale-low 0.4641 --scale-high 0.9282") {
		t.Errorf("solve-field ran with %q", args)
	}

	if err := os.Remove(record); err != nil {
		t.Fatal(err)
	}
	same := app.TargetReference{Object: "Black Eye Galaxy", FrameID: 1, ObjectKey: "stacks/Black Eye Galaxy/ref.fit", SolveAttempts: 3, SolveError: &gaveUp, SolveInputs: saved.SolveInputs}
	if err := db.Create(&same).Error; err != nil {
		t.Fatal(err)
	}
	if cards := p.plateSolution(context.Background(), &same, &app.Stack{Object: "Black Eye Galaxy"}, keySPA2, spa2Cards(), dir, master); cards != nil {
		t.Errorf("given up with the same inputs, solved: %+v", cards)
	}
	if _, err := os.Stat(record); err == nil {
		t.Error("solve-field ran for a master given up on with the same inputs")
	}
	if !solveDue(&app.TargetReference{SolveAttempts: 3, SolveInputs: "r0 before the optics table"}, saved.SolveInputs) {
		t.Error("changed inputs not due")
	}
	if solveDue(&app.TargetReference{SolveAttempts: 1, WCS: saved.WCS}, saved.SolveInputs) {
		t.Error("solved target due")
	}
}
