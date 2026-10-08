package stacking

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/siril"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"gorm.io/gorm"
)

// fakeS3 is a bucket that takes uploads.
func fakeS3(t *testing.T) (*minio.Client, map[string]bool) {
	t.Helper()
	var mu sync.Mutex
	put := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.Error(w, "not implemented", http.StatusNotImplemented)
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		put[strings.TrimPrefix(r.URL.Path, "/out/")] = true
		mu.Unlock()
		w.Header().Set("ETag", `"0"`)
	}))
	t.Cleanup(srv.Close)
	s3, err := minio.New(strings.TrimPrefix(srv.URL, "http://"), &minio.Options{
		Creds: credentials.NewStaticV4("k", "s", ""), Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	return s3, put
}

func offTargetDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&app.Frame{}, &app.StackFrame{}); err != nil {
		t.Fatal(err)
	}
	return db
}

// A sub whose header pointing is wrong but which registers to the target's
// reference is stacked; one that doesn't register is off target, and only
// an on-target sub's failure counts against the reference.
func TestOffPointingSubsStayOnlyIfTheyRegister(t *testing.T) {
	t.Parallel()
	s3, put := fakeS3(t)
	db := offTargetDB(t)
	p := NewPipeline(s3, "in", "out", db, nil, siril.Runner{}, t.TempDir(), DefaultPipelineOptions())
	positions := map[string][2]float64{"Triangulum Galaxy": {23.46, 30.66}}
	exp := 300.0
	sub := func(id int, key string, ra, dec float64) calibrated {
		f := app.Frame{ID: id, Key: "Triangulum Galaxy/LIGHT/" + key, Object: "Triangulum Galaxy", Filter: filterBlue,
			MountRA: &ra, MountDec: &dec, Exposure: &exp}
		c := candidate{frame: f}
		if pos := positions[f.Object]; separation(ra, dec, pos[0], pos[1]) > OffTargetDegrees {
			c.offBy = separation(ra, dec, pos[0], pos[1])
		}
		return calibrated{c: c}
	}
	cals := []calibrated{
		// 2026-10-06: the mount 20° out, the scope on M33.
		sub(1, "2026-10-06_02-57-05_Blue_-11.00_300.00s_0047.xisf", 6.67, 17.72),
		// Parked: other stars.
		sub(2, "parked.xisf", 0.04, 0.0002),
		// On target, clouded over.
		sub(3, "cloud.xisf", 23.46, 30.66),
	}
	reg := filepath.Join(t.TempDir(), "r_seq_00002.fit")
	if err := os.WriteFile(reg, []byte("registered"), 0o600); err != nil {
		t.Fatal(err)
	}
	added, unregistered, err := p.storeRegistered(context.Background(), "Triangulum Galaxy", filterBlue, cals, []string{reg, "", ""})
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 1 || added[0].c.c.frame.ID != cals[0].c.frame.ID ||
		!put["registered/Triangulum Galaxy/Blue/2026-10-06_02-57-05_Blue_-11.00_300.00s_0047.fit"] {
		t.Errorf("the registered off-pointing sub wasn't added: %+v, uploads %v", added, put)
	}
	if unregistered != 1 {
		t.Errorf("%d failures count against the reference, want 1 (the clouded sub)", unregistered)
	}
	status := func(id int) app.StackFrame {
		var sf app.StackFrame
		db.Where("frame_id = ?", id).First(&sf)
		return sf
	}
	if sf := status(cals[1].c.frame.ID); sf.Status != app.StackStatusOffTarget || sf.Error == nil || sf.NextAttemptAt != nil {
		t.Errorf("parked sub: %q, error %v, next %v", sf.Status, sf.Error, sf.NextAttemptAt)
	}
	if sf := status(cals[2].c.frame.ID); sf.Status != app.StackStatusRegistration {
		t.Errorf("clouded sub: %q", sf.Status)
	}
}

// Subs left out on their mount pointing alone (no error recorded) are tried
// again, once; those left out after failing to register stay out.
func TestOffTargetSubsAreRequeuedOnce(t *testing.T) {
	t.Parallel()
	db := offTargetDB(t)
	p := NewPipeline(nil, "", "", db, nil, siril.Runner{}, "", DefaultPipelineOptions())
	now := time.Now()
	for _, sf := range []app.StackFrame{
		{FrameID: 1, Status: app.StackStatusOffTarget, ProcessedAt: now},
		{FrameID: 2, Status: app.StackStatusOffTarget, ProcessedAt: now, Error: offTargetError(20.5)},
		{FrameID: 3, Status: app.StackStatusRegistration, ProcessedAt: now, Attempts: 2},
	} {
		if err := db.Create(&sf).Error; err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	p.requeueOffTarget(ctx)
	p.requeueOffTarget(ctx)
	want := map[int]string{1: app.StackStatusFailed, 2: app.StackStatusOffTarget, 3: app.StackStatusRegistration}
	var rows []app.StackFrame
	db.Order("frame_id").Find(&rows)
	for _, sf := range rows {
		if sf.Status != want[sf.FrameID] {
			t.Errorf("frame %d: %q, want %q", sf.FrameID, sf.Status, want[sf.FrameID])
		}
		if sf.FrameID == 1 && (sf.Attempts != 0 || sf.NextAttemptAt == nil || sf.NextAttemptAt.After(time.Now())) {
			t.Errorf("requeued sub isn't due now: attempts %d, next %v", sf.Attempts, sf.NextAttemptAt)
		}
	}
	// And it is due: the pending query picks it up.
	for id := 1; id <= 3; id++ {
		if err := db.Create(&app.Frame{ID: id, Key: string(rune('a' + id)), Type: frameTypeLight, Object: objectOrion, Filter: filterHa,
			LastModified: now}).Error; err != nil {
			t.Fatal(err)
		}
	}
	var pending []int
	p.pendingLights(db, time.Now().Add(time.Second)).Pluck("frames.id", &pending)
	if len(pending) != 1 || pending[0] != 1 {
		t.Errorf("pending %v, want [1]", pending)
	}
}
