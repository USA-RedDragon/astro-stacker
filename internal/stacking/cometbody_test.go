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

func TestBodyDesignationPrefersStored(t *testing.T) {
	t.Parallel()
	stored := map[string]string{encke: enckeCmd, swanName: " C/2025 R2 ", "Blank": "  "}
	for name, want := range map[string]string{
		encke:    enckeCmd,
		swanName: "C/2025 R2",
		ponsName: ponsDes,
		"Blank":  "",
		"M 31":   "",
	} {
		got, ok := bodyDesignation(stored, name)
		if got != want || ok != (want != "") {
			t.Errorf("bodyDesignation(%q) = %q %v, want %q", name, got, ok, want)
		}
	}
	if got, ok := bodyDesignation(nil, ponsName); !ok || got != ponsDes {
		t.Errorf("name fallback without stored designations: %q %v", got, ok)
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
	if len(m) != 1 || m[encke] != enckeCmd {
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
	if _, err := cometEphemeris(context.Background(), hz, enckeCmd, []time.Time{at}, -97, 32, 200); err != nil {
		t.Fatal(err)
	}
	if cmd != "'DES=2P;CAP;NOFRAG'" || agent != "astro-stacker/v9 (jacob@mcswain.dev)" {
		t.Errorf("COMMAND %s, User-Agent %s", cmd, agent)
	}
	if _, err := cometEphemeris(context.Background(), hz, "C/2025 R2", []time.Time{at}, -97, 32, 200); err != nil || cmd != "'DES=C/2025 R2;CAP;NOFRAG'" {
		t.Errorf("COMMAND %s, %v", cmd, err)
	}
}

func TestCometSignatureFollowsTheStoredDesignation(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	s := &app.Stack{Object: encke, Subs: 12, UpdatedAt: at}
	name := cometSignature(s, "")
	if name != cometSignature(s, "  ") {
		t.Error("a blank designation changed the signature")
	}
	h := sha256.Sum256(fmt.Appendf(nil, "%d\x00%d\x00%d", s.Subs, s.UpdatedAt.UnixNano(), cometMethod))
	if name != hex.EncodeToString(h[:16]) {
		t.Error("targets known by name got a new signature, which would stack every comet again")
	}
	first, second := cometSignature(s, "2P"), cometSignature(s, enckeCmd)
	if first == name || second == first {
		t.Error("storing or changing a designation kept the signature, so the comet is not stacked again")
	}
	s.CometSignature = first
	if _, wait := cometCover([]app.Stack{*s}, map[string]string{encke: "2P"}); wait {
		t.Error("the cover waits on a comet master that is current")
	}
}
