package stacking

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/bodies"
	"github.com/USA-RedDragon/astro-stacker/internal/horizons"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	swanName = "C/2025 R2 (SWAN)"
	ponsName = "12P/Pons-Brooks"
	ponsDes  = "12P"
	enckeCmd = "DES=2P;CAP;NOFRAG"
	encke    = "Encke"
)

func comet(designation string) bodies.Target {
	return bodies.Target{Designation: designation, Kind: bodies.KindComet}
}

func TestBodyDesignationPrefersStored(t *testing.T) {
	t.Parallel()
	stored := map[string]bodies.Target{encke: comet(enckeCmd), swanName: comet(" C/2025 R2 "), "Blank": comet("  "),
		"1 Ceres": {Designation: " 1 ", Kind: bodies.KindAsteroid}}
	for name, want := range map[string]bodies.Target{
		encke:     comet(enckeCmd),
		swanName:  comet("C/2025 R2"),
		ponsName:  comet(ponsDes),
		"Blank":   comet(""),
		"M 31":    comet(""),
		"1 Ceres": {Designation: "1", Kind: bodies.KindAsteroid},
	} {
		got, ok := bodyDesignation(stored, name)
		if got.Designation != want.Designation || got.Kind != want.Kind || ok != (want.Designation != "") {
			t.Errorf("bodyDesignation(%q) = %+v %v, want %+v", name, got, ok, want)
		}
	}
	if got, ok := bodyDesignation(nil, ponsName); !ok || got.Designation != ponsDes || got.Kind != bodies.KindComet {
		t.Errorf("name fallback without stored designations: %+v %v", got, ok)
	}
}

func TestStoredDesignations(t *testing.T) {
	t.Parallel()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := db.DB()
	raw.SetMaxOpenConns(1)
	p := &Pipeline{sched: db}
	for _, q := range []string{
		`create table project ("Id" integer primary key, name text, state integer)`,
		`create table target ("Id" integer primary key, name text, active integer, projectid integer, guid text)`,
		`insert into project values (1, 'Comets', 1)`,
		`insert into target values (1, '` + encke + `', 1, 1, 'g1'), (2, 'M 31', 1, 1, 'g2')`,
	} {
		if err := db.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	if m := p.storedDesignations(context.Background()); len(m) != 0 {
		t.Errorf("without ts_target_body: %v", m)
	}
	for _, q := range []string{
		`create table ts_target_body (target_guid text primary key, designation text not null, kind text not null default 'comet', updated_at text)`,
		`insert into ts_target_body (target_guid, designation) values ('g1', 'DES=2P;CAP;NOFRAG')`,
	} {
		if err := db.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	m := p.storedDesignations(context.Background())
	if len(m) != 1 || m[encke].Designation != enckeCmd || m[encke].Kind != bodies.KindComet {
		t.Errorf("stored %v", m)
	}
	if m := (&Pipeline{}).storedDesignations(context.Background()); m != nil {
		t.Errorf("no scheduler: %v", m)
	}
}

func TestCometEphemerisSendsStoredCommand(t *testing.T) {
	t.Parallel()
	var cmd, agent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cmd, agent = r.URL.Query().Get("COMMAND"), r.Header.Get("User-Agent")
		_, _ = w.Write([]byte(`{"result":"$$SOE\n 2026-Oct-13 00:00, , , 10.0, 20.0,\n 2026-Oct-13 00:01, , , 10.1, 20.0,\n 2026-Oct-13 00:02, , , 10.2, 20.0,\n$$EOE\n"}`))
	}))
	defer srv.Close()
	at := time.Date(2026, 10, 13, 0, 1, 0, 0, time.UTC)
	hz := &horizons.Client{BaseURL: srv.URL, UserAgent: horizons.UserAgent("v9")}
	if _, err := cometEphemeris(context.Background(), hz, comet(enckeCmd), []time.Time{at}, -97, 32, 200); err != nil {
		t.Fatal(err)
	}
	if cmd != "'DES=2P;CAP;NOFRAG'" || agent != "astro-stacker/v9 (jacob@mcswain.dev)" {
		t.Errorf("COMMAND %s, User-Agent %s", cmd, agent)
	}
	if _, err := cometEphemeris(context.Background(), hz, comet("C/2025 R2"), []time.Time{at}, -97, 32, 200); err != nil || cmd != "'DES=C/2025 R2;CAP;NOFRAG'" {
		t.Errorf("COMMAND %s, %v", cmd, err)
	}
	if _, err := cometEphemeris(context.Background(), hz, bodies.Target{Designation: "C/2025 R2"}, []time.Time{at}, -97, 32, 200); err != nil || cmd != "'DES=C/2025 R2;CAP;NOFRAG'" {
		t.Errorf("no kind: COMMAND %s, %v", cmd, err)
	}
	for des, want := range map[string]string{"1": "'1;'", "2026 TC6": "'DES=2026 TC6;'"} {
		if _, err := cometEphemeris(context.Background(), hz, bodies.Target{Designation: des, Kind: bodies.KindAsteroid}, []time.Time{at}, -97, 32, 200); err != nil || cmd != want {
			t.Errorf("asteroid %s: COMMAND %s, %v", des, cmd, err)
		}
	}
}

func TestCometSignatureFollowsTheStoredDesignation(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	s := &app.Stack{Object: encke, Subs: 12, UpdatedAt: at}
	name := cometSignature(s, bodies.Target{})
	if name != cometSignature(s, comet("  ")) {
		t.Error("a blank designation changed the signature")
	}
	h := sha256.Sum256(fmt.Appendf(nil, "%d\x00%d\x00%d", s.Subs, s.UpdatedAt.UnixNano(), cometMethod))
	if name != hex.EncodeToString(h[:16]) {
		t.Error("targets known by name got a new signature, which would stack every comet again")
	}
	before := sha256.Sum256(fmt.Appendf(nil, "%d\x00%d\x00%d\x002P", s.Subs, s.UpdatedAt.UnixNano(), cometMethod))
	for _, kind := range []string{bodies.KindComet, "", " comet "} {
		if got := cometSignature(s, bodies.Target{Designation: " 2P ", Kind: kind}); got != hex.EncodeToString(before[:16]) {
			t.Errorf("kind %q: a stored comet got a new signature, which would stack it again", kind)
		}
	}
	first, second := cometSignature(s, comet("2P")), cometSignature(s, comet(enckeCmd))
	if first == name || second == first {
		t.Error("storing or changing a designation kept the signature, so the comet is not stacked again")
	}
	asteroid := cometSignature(s, bodies.Target{Designation: "2P", Kind: bodies.KindAsteroid})
	if asteroid == first || asteroid == name {
		t.Error("an asteroid shares a comet's signature")
	}
	s.CometSignature = first
	if _, wait := cometCover([]app.Stack{*s}, map[string]bodies.Target{encke: comet("2P")}); wait {
		t.Error("the cover waits on a comet master that is current")
	}
}

func TestAlignedOnNamesTheBody(t *testing.T) {
	t.Parallel()
	if got := alignedOn(comet("2P")); got != "aligned on the comet" {
		t.Errorf("comet: %q", got)
	}
	if got := alignedOn(bodies.Target{Designation: "2P"}); got != "aligned on the comet" {
		t.Errorf("no kind: %q", got)
	}
	if got := alignedOn(bodies.Target{Designation: "1", Kind: bodies.KindAsteroid}); got != "aligned on the asteroid" {
		t.Errorf("asteroid: %q", got)
	}
}
