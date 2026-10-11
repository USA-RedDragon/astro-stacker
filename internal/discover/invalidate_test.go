package discover_test

import (
	"context"
	"sync/atomic"
	"testing"

	"gorm.io/gorm"
)

func TestInvalidateDuringABuildDropsThatBuild(t *testing.T) {
	t.Parallel()
	s := newService(t)
	ctx := context.Background()
	entered, release := make(chan struct{}), make(chan struct{})
	var hold atomic.Bool
	hold.Store(true)
	if err := s.AppDB.Callback().Query().After("gorm:query").Register("test:hold", func(db *gorm.DB) {
		if db.Statement.Table == "object_xrefs" && hold.CompareAndSwap(true, false) {
			close(entered)
			<-release
		}
	}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.Subjects(ctx)
		done <- err
	}()
	<-entered
	if err := s.SchedDB.Exec(`INSERT INTO project ("Id", "profileId", name, state, minimumaltitude, "isMosaic", enablegrader, guid) VALUES (7, 'p', 'Heart Nebula', 1, 30, 0, 1, 'ph')`).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.SchedDB.Exec(`INSERT INTO target ("Id", name, active, ra, dec, rotation, projectid, guid) VALUES (8, 'Heart Nebula', 1, 2.55, 61.45, 0, 7, 'th')`).Error; err != nil {
		t.Fatal(err)
	}
	s.Invalidate()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	subs, err := s.Subjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, sb := range subs {
		if sb.Key == "project:Heart Nebula" {
			return
		}
	}
	t.Fatal("a snapshot built before Invalidate was kept after it")
}
