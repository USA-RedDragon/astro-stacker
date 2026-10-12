package bodies_test

import (
	"context"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/bodies"
)

func TestEventCentresAreNotHorizonsBodies(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	sched := scheduler(t, true, true)
	exec(t, sched,
		`insert into ts_target_body (target_guid, designation, kind) values ('dddd-fixed', 'event:mars-jupiter-2026-11-08', 'event')`,
		`insert into ts_body_position (target_guid, at, designation, night, ra, dec, ra_rate, dec_rate, computed_at) values
		 ('dddd-fixed', '2026-11-16 07:30:00', 'event:mars-jupiter-2026-11-08', '2026-11-15', 148.2, 14.3, 10, -5, '2026-10-13 01:00:00')`)
	targets, err := bodies.Targets(context.Background(), sched)
	if err != nil {
		t.Fatal(err)
	}
	for _, tg := range targets {
		if tg.GUID == "dddd-fixed" {
			t.Fatalf("the event centre is listed as a body: %+v", tg)
		}
	}
	s := service(t, sched, f, bodies.ModeOn)
	res, err := s.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Targets != 1 {
		t.Errorf("%d targets fetched, want only the comet", res.Targets)
	}
	for range f.hits.Load() {
		if cmd := <-f.cmds; cmd != "'DES=C/2025 R2;CAP;NOFRAG'" {
			t.Errorf("Horizons was asked for %s", cmd)
		}
	}
	if n := count(t, sched, "designation = 'event:mars-jupiter-2026-11-08'"); n != 1 {
		t.Errorf("the body tables service removed the event centre rows: %d left", n)
	}
	if !bodies.IsEvent("Event:x", "") || !bodies.IsEvent("x", bodies.KindEvent) || bodies.IsEvent("C/2025 R2", bodies.KindComet) {
		t.Error("IsEvent")
	}
}
