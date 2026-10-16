//go:build vettingdev

package ownimage_test

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/starcat"
	"github.com/USA-RedDragon/astro-stacker/internal/tnsmirror"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/ownimage"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/surveys"
)

type dirStore struct{ root string }

func (d dirStore) path(key string) string { return filepath.Join(d.root, filepath.FromSlash(key)) }

func (d dirStore) Get(_ context.Context, key string) ([]byte, error) {
	b, err := os.ReadFile(d.path(key))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, surveys.ErrNotFound
	}
	return b, err
}

func (d dirStore) Put(_ context.Context, key string, data []byte, _ string) error {
	if err := os.MkdirAll(filepath.Dir(d.path(key)), 0o750); err != nil {
		return err
	}
	return os.WriteFile(d.path(key), data, 0o600)
}

func (d dirStore) Sizes(_ context.Context, prefix string) (map[string]int64, error) {
	out := map[string]int64{}
	root := d.path(prefix)
	err := filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		seg, _, _ := strings.Cut(filepath.ToSlash(rel), "/")
		if info, err := e.Info(); err == nil {
			out[seg] += info.Size()
		}
		return nil
	})
	return out, err
}

type acceptCase struct {
	spot vetting.Spot
}

func acceptCases() map[string]acceptCase {
	day := func(y, m, d int) time.Time { return time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC) }
	return map[string]acceptCase{
		"andromeda": {vetting.Spot{Object: "Andromeda", RA: 10.889321, Dec: 41.662433, Nights: []time.Time{day(2026, 10, 11)}, Groups: []string{discoveries.GroupBroadband}, Sign: -1, Delta: 0.236}},
		"ic1396":    {vetting.Spot{Object: "IC 1396 Panel 1", RA: 323.277032, Dec: 58.634305, Nights: []time.Time{day(2025, 12, 8)}, Groups: []string{discoveries.GroupBroadband}, Sign: -1, Delta: 0.161}},
		"nan":       {vetting.Spot{Object: "North America Nebula Panel 2", RA: 316.161804, Dec: 43.320665, Nights: []time.Time{day(2025, 12, 17)}, Groups: []string{discoveries.GroupBroadband}, Sign: -1, Delta: 0.19}},
		"cygnus": {vetting.Spot{Object: "Cygnis Loop Panel 2", RA: 314.32090, Dec: 28.71857, Nights: []time.Time{day(2026, 10, 5), day(2026, 10, 6), day(2026, 10, 7)},
			Groups: []string{"S-II", "H-a"}, Sign: -1, Delta: 0.58, RunAt: day(2026, 10, 9)}},
	}
}

func TestDevAcceptance(t *testing.T) {
	t.Parallel()
	name := os.Getenv("VETTING_CASE")
	c, ok := acceptCases()[name]
	if !ok {
		t.Skip("set VETTING_CASE to andromeda, ic1396, nan or cygnus")
	}
	ctx := context.Background()
	data, mc := ownimage.NewDevData(t)
	survey := starcat.MinioBucket{Client: mc, Name: "sky-survey"}
	mirrors := vetting.Mirrors{VSX: starcat.NewVSX(survey, "catalogs/vsx"), Variables: map[string]*starcat.Variables{}}
	for _, vc := range starcat.VarCatalogs() {
		mirrors.Variables[vc.Name] = starcat.NewVariables(survey, vc.Prefix)
	}
	tm := &tnsmirror.Mirror{Bucket: survey, Prefix: "catalogs/tns"}
	if err := tm.Load(ctx); err == nil {
		mirrors.TNS = tm.Index
	} else {
		t.Logf("TNS mirror: %v", err)
	}
	home, _ := os.UserHomeDir()
	store := dirStore{root: filepath.Join(home, "work/vetting/surveys/live/store")}
	cat := &discoveries.Catalogs{}
	svc := &vetting.Service{
		Data:    data,
		Gaia:    starcat.NewGaia(survey, "catalogs/gaia-dr3/v1"),
		Mirrors: mirrors,
		Simbad:  vetting.SimbadTAP(nil, "", store, "vetting/cache/", time.Now),
		Bodies:  cat.KnownBodies,
		Surveys: &surveys.Fetcher{Store: store, Prefix: "vetting/cache/", HTTP: &http.Client{Timeout: 5 * time.Minute},
			ATLASToken: os.Getenv("SURVEYS_ATLAS_TOKEN"), WorkDir: filepath.Join(home, "work/vetting/accept/work"), Now: time.Now},
		Threads: 6,
	}
	start := time.Now()
	rep, err := svc.VetSpot(ctx, c.spot)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("took %s", time.Since(start).Round(time.Second))
	t.Logf("STAR %s", rep.Star)
	t.Logf("SUGGESTED %s | %s | %s | %s", rep.Suggestion.State, rep.Suggestion.Confidence, rep.Suggestion.Say, rep.Suggestion.Why)
	if rep.Ephemeris != nil {
		t.Logf("PERIOD %.6f +- %.6f T0 %.5f", rep.Ephemeris.Period, rep.Ephemeris.PeriodErr, rep.Ephemeris.T0)
	}
	for _, g := range rep.Groups {
		for _, ch := range g.Checks {
			t.Logf("%-9s %-12s %-4s %s", g.ID, ch.ID, ch.Status, ch.Evidence)
		}
	}
	t.Logf("pending %v", rep.Pending)
	b, err := json.MarshalIndent(rep, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Join(home, "work/vetting/accept"), 0o750)
	_ = os.WriteFile(filepath.Join(home, "work/vetting/accept", name+".json"), b, 0o600)
}
