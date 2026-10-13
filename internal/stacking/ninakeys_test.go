package stacking

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/siril"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

func TestKeysUseNINAFolders(t *testing.T) {
	t.Parallel()
	const comet = "161P/Hartley-IRAS"
	for got, want := range map[string]string{
		stackPrefix(&app.Stack{Object: comet, Filter: "Luminance"}):         "stacks/161P-Hartley-IRAS/Luminance",
		stackPrefix(&app.Stack{Object: "M 31", Filter: "Ha/OIII"}):          "stacks/M 31/Ha-OIII",
		registeredKey(comet, "Luminance", "161P-Hartley-IRAS/LIGHT/a.xisf"): "registered/161P-Hartley-IRAS/Luminance/a.xisf",
		nightRefKey(comet, "2026-10-12"):                                    "stacks/161P-Hartley-IRAS/nights/2026-10-12/reference.fit",
		mosaicPrefix("Veil: East/West", "Red"):                              "mosaics/Veil_ East-West/Red",
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestRegisteredTargets(t *testing.T) {
	t.Parallel()
	masters := map[string][]string{
		"161P-Hartley-IRAS-Luminance": {"161P/Hartley-IRAS"},
		"M31-Ha-OIII":                 {"M31"},
		"A-B-L":                       {"A/B", "A-B"},
	}
	for key, want := range map[string][]string{
		"registered/161P-Hartley-IRAS/Luminance/a.xisf": {"161P/Hartley-IRAS"},
		"registered/161P/Hartley-IRAS/Luminance/a.xisf": {"161P/Hartley-IRAS"},
		"registered/M31/Ha/OIII/a.xisf":                 {"M31"},
		"registered/M31/Ha-OIII/a.xisf":                 {"M31"},
		"registered/A-B/L/a.xisf":                       {"A/B", "A-B"},
		"registered/M33/Ha/a.xisf":                      {"M33"},
		"registered/a.fit":                              {"a.fit"},
	} {
		if got := registeredTargets(key, masters); !slices.Equal(got, want) {
			t.Errorf("registeredTargets(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestReRegisteredSlashTargetIsCollected(t *testing.T) {
	t.Parallel()
	now := time.Now()
	old := now.Add(-72 * time.Hour)
	legacy := registeredObject{Key: "registered/C/2025 A6 (Lemmon)/Green/a.fit", LastModified: old}
	current := "registered/C-2025 A6 (Lemmon)/Green/a.xisf"
	listed := map[string]registeredObject{current: {Key: current, LastModified: old}}
	rows := []subRow{{Status: app.StackStatusAdded, RegisteredKey: &current}}
	if got := gcDecide(legacy, rows, map[string]bool{current: true}, listed, nil, now, 24*time.Hour); got != gcDelete {
		t.Errorf("old-layout copy of a re-registered sub: verdict %v, want gcDelete", got)
	}
	shifted := registeredObject{Key: "registered/A/B-C/a.fit", LastModified: old}
	moved := "registered/A-B/C/a.xisf"
	rows = []subRow{{Status: app.StackStatusAdded, RegisteredKey: &moved}}
	listed[moved] = registeredObject{Key: moved, LastModified: old}
	if got := gcDecide(shifted, rows, map[string]bool{moved: true}, listed, nil, now, 24*time.Hour); got != gcAmbiguous {
		t.Errorf("same file name of another target and filter: verdict %v, want gcAmbiguous", got)
	}
	other := "registered/C-2025 A6 (Lemmon)/Red/a.xisf"
	rows = []subRow{{Status: app.StackStatusAdded, RegisteredKey: &other}}
	if got := gcDecide(legacy, rows, map[string]bool{other: true}, listed, nil, now, 24*time.Hour); got != gcAmbiguous {
		t.Errorf("same file name in another filter: verdict %v, want gcAmbiguous", got)
	}
}

func TestCollectRegisteredHoldsSlashTarget(t *testing.T) {
	t.Parallel()
	db := registeredDB(t)
	const comet = "161P/Hartley-IRAS"
	if err := db.Create(&app.Stack{Object: comet, Filter: "Luminance"}).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	old := now.Add(-72 * time.Hour)
	store := newFakeStore(now)
	legacy, nina := "registered/161P/Hartley-IRAS/Luminance/a.xisf", "registered/161P-Hartley-IRAS/Luminance/b.xisf"
	store.set(legacy, make([]byte, 10), old)
	store.set(nina, make([]byte, 10), old)
	p := NewPipeline(nil, "", "", db, nil, siril.Runner{}, "", DefaultPipelineOptions())
	p.opts.RegisteredDeletePause = 0
	p.hold(comet)
	res, err := p.collectRegistered(context.Background(), store, now)
	if err != nil || res.deleted != 0 || res.busy != 2 {
		t.Errorf("target busy: %+v %v", res, err)
	}
	p.release(comet)
	if res, err = p.collectRegistered(context.Background(), store, now); err != nil || res.busy != 0 || res.pending != 2 {
		t.Errorf("target free: %+v %v", res, err)
	}
}
