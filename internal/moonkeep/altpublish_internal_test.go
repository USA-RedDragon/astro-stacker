package moonkeep

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestPublishAltWritesOnlyWithTheTable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	res := &Result{ComputedAt: &at, Subs: 1, alt: FitAltitude(lowSky())}
	s := &Service{Sched: db, Mode: ModeOn, Now: func() time.Time { return at }}
	if st, err := s.publishAlt(ctx, res); err != nil || st.Skipped != "no "+AltTable+" table" {
		t.Fatalf("no table: %+v %v", st, err)
	}
	if err := db.Exec(AltSchema).Error; err != nil {
		t.Fatal(err)
	}
	off := &Service{Sched: db, Mode: ModeOff}
	if st, _ := off.publishAlt(ctx, res); st.Skipped != SkipOff {
		t.Errorf("off: %+v", st)
	}
	st, err := s.publishAlt(ctx, res)
	if err != nil || st.Changed != st.Rows || st.Rows == 0 || st.PublishedAt == nil {
		t.Fatalf("first: %+v %v", st, err)
	}
	var n int64
	db.Table(AltTable).Count(&n)
	if int(n) != st.Rows {
		t.Fatalf("%d rows, want %d", n, st.Rows)
	}
	if st, err := s.publishAlt(ctx, res); err != nil || st.Skipped != SkipUnchanged {
		t.Errorf("second: %+v %v", st, err)
	}
	fewer := &Result{ComputedAt: &at, Subs: 1, alt: FitAltitude(altSamples(high(false)))}
	if st, err := s.publishAlt(ctx, fewer); err != nil || st.Deleted == 0 {
		t.Errorf("stale rows go: %+v %v", st, err)
	}
}
