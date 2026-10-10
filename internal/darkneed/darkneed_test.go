package darkneed_test

import (
	"bytes"
	"context"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/coverage"
	"github.com/USA-RedDragon/astro-stacker/internal/darkcheck"
	"github.com/USA-RedDragon/astro-stacker/internal/darkneed"
	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	pgdriver "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	imgW = 512
	imgH = 384
)

func fp(v float64) *float64 { return &v }

func pixels(seed uint64, level, ramp float64) []float32 {
	r := rand.New(rand.NewPCG(seed, seed))
	d := make([]float32, imgW*imgH)
	for y := range imgH {
		for x := range imgW {
			n := level + 7*r.NormFloat64()
			d[y*imgW+x] = float32((n + ramp*float64(imgW-x+imgH-y)/float64(imgW+imgH)) / 65535)
		}
	}
	return d
}

type fixture struct {
	files map[string][]byte
	now   time.Time
}

func (fx *fixture) download(_ context.Context, key string) ([]byte, error) {
	b, ok := fx.files[key]
	if !ok {
		return nil, fmt.Errorf("no object %s", key)
	}
	return b, nil
}

func (fx *fixture) add(t *testing.T, db *gorm.DB, f app.Frame, data []float32) {
	t.Helper()
	if data != nil {
		var buf bytes.Buffer
		if err := imagedata.WriteFITS(&buf, imgW, imgH, 1, data, nil); err != nil {
			t.Fatal(err)
		}
		fx.files[f.Key] = buf.Bytes()
	}
	f.ETag, f.Size, f.LastModified = "e", 1, fx.now
	if f.BinX == nil {
		f.BinX = fp(1)
	}
	if f.Night == nil && f.DateObs != nil {
		n := time.Date(f.DateObs.Year(), f.DateObs.Month(), f.DateObs.Day(), 0, 0, 0, 0, time.UTC)
		f.Night = &n
	}
	if err := db.Create(&f).Error; err != nil {
		t.Fatal(err)
	}
}

func seed(t *testing.T, db *gorm.DB) *fixture {
	t.Helper()
	if err := db.AutoMigrate(&app.Frame{}); err != nil {
		t.Fatal(err)
	}
	fx := &fixture{files: map[string][]byte{}, now: time.Date(2026, 10, 10, 7, 0, 0, 0, time.UTC)}
	at := func(h int) *time.Time {
		v := fx.now.Add(time.Duration(h) * time.Hour)
		return &v
	}
	for i := range 4 {
		fx.add(t, db, app.Frame{Key: fmt.Sprintf("BIAS/b%d.fits", i), Type: darkcheck.TypeBias, Exposure: fp(0), Gain: fp(100), Offset: fp(50),
			SetTemp: fp(-10), DateObs: at(-400 + i)}, pixels(uint64(10+i), 500, 0))
	}
	n := 0
	light := func(setTemp float64, count int, h int) {
		for range count {
			n++
			fx.add(t, db, app.Frame{Key: fmt.Sprintf("M31/LIGHT/l%d.fits", n), Type: "LIGHT", Object: "M31", Filter: "Red",
				Exposure: fp(300), Gain: fp(100), Offset: fp(50), SetTemp: fp(setTemp), DateObs: at(h)}, nil)
		}
	}
	light(-20, 5, -300)
	light(-21, 2, -290)
	light(-19, 1, -280)
	light(-13, 4, -30)
	light(-5, 3, -20)
	for i := range 3 {
		fx.add(t, db, app.Frame{Key: fmt.Sprintf("DARK/d5_%d.fits", i), Type: darkcheck.TypeDark, Exposure: fp(300), Gain: fp(100), Offset: fp(50),
			SetTemp: fp(-5), DateObs: at(-200 + i)}, pixels(uint64(20+i), 502, 0))
	}
	fx.add(t, db, app.Frame{Key: "DARK/d13_0.fits", Type: darkcheck.TypeDark, Exposure: fp(300), Gain: fp(100), Offset: fp(50),
		SetTemp: fp(-13), DateObs: at(-3)}, pixels(30, 501, 0))
	fx.add(t, db, app.Frame{Key: "DARK/d13_1.fits", Type: darkcheck.TypeDark, Exposure: fp(300), Gain: fp(100), Offset: fp(50),
		SetTemp: fp(-13), DateObs: at(-2)}, pixels(31, 501, 150))
	nogain := app.Frame{Key: "old/LIGHT/x.fits", Type: "LIGHT", Exposure: fp(600), SetTemp: fp(-25), DateObs: at(-900)}
	fx.add(t, db, nogain, nil)
	return fx
}

func checkBacklog(t *testing.T, db, sched *gorm.DB) {
	t.Helper()
	fx := seed(t, db)
	checkComputed(t, db, fx)
	checkPublished(t, db, sched, fx)
}

func checkComputed(t *testing.T, db *gorm.DB, fx *fixture) {
	t.Helper()
	ctx := context.Background()
	if _, err := darkcheck.MeasurePending(ctx, db, fx.download, 2); err != nil {
		t.Fatal(err)
	}
	clean, leak, err := darkcheck.JudgePending(ctx, db, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil || clean != 4 || leak != 1 {
		t.Fatalf("clean %d leak %d err %v", clean, leak, err)
	}
	b, err := coverage.BuildDarkBacklog(ctx, db, fx.now)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Combos) != 2 {
		t.Fatalf("combos %+v", b.Combos)
	}
	first, second := b.Combos[0], b.Combos[1]
	if first.SetTemp != -20 || first.LightsBlocked != 8 || first.Priority != 1 || len(first.SetTempsCovered) != 3 || first.FramesNeeded != 3 {
		t.Errorf("first %+v", first)
	}
	if second.SetTemp != -13 || second.LightsScaled != 4 || second.LightsBlocked != 0 || second.FramesHave != 1 ||
		second.FramesRejected != 1 || second.NewestDarkAt == nil {
		t.Errorf("second %+v", second)
	}
	if len(b.Unschedulable) != 1 || b.Unschedulable[0].Lights != 1 {
		t.Errorf("unschedulable %+v", b.Unschedulable)
	}
	if len(b.Rejected) != 1 || b.Rejected[0].Key != "DARK/d13_1.fits" || b.Rejected[0].Reason == "" {
		t.Errorf("rejected %+v", b.Rejected)
	}
	if b.LastSession == nil || b.LastSession.Frames != 2 || b.LastSession.Leak != 1 || b.LastSession.Clean != 1 {
		t.Errorf("last session %+v", b.LastSession)
	}
}

func checkPublished(t *testing.T, db, sched *gorm.DB, fx *fixture) {
	t.Helper()
	ctx := context.Background()
	if err := sched.Exec(darkneed.Schema).Error; err != nil {
		t.Fatal(err)
	}
	p := &darkneed.Publisher{App: db, Sched: sched, Mode: darkneed.PublishOn, Now: func() time.Time { return fx.now }}
	s, err := p.Publish(ctx)
	if err != nil || s.Changed != 2 || s.PublishedAt == nil {
		t.Fatalf("publish %+v %v", s, err)
	}
	var rows []darkneed.Row
	if err := sched.Table(darkneed.Table).Order("priority").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ComboKey != "300|100|50|1|-20" || rows[0].CameraOffset != 50 || rows[1].NewestDarkAt == nil ||
		!rows[1].NewestDarkAt.Equal(fx.now.Add(-2*time.Hour)) {
		t.Fatalf("rows %+v", rows)
	}
	if s, err := p.Publish(ctx); err != nil || s.Skipped != darkneed.SkipUnchanged {
		t.Fatalf("second publish %+v %v", s, err)
	}
	for i := range 2 {
		at := fx.now.Add(time.Duration(-1+i) * time.Minute)
		fx.add(t, db, app.Frame{Key: fmt.Sprintf("DARK/d13_more%d.fits", i), Type: darkcheck.TypeDark, Exposure: fp(300), Gain: fp(100), Offset: fp(50),
			SetTemp: fp(-13), DateObs: &at}, pixels(uint64(40+i), 501, 0))
	}
	if _, err := darkcheck.MeasurePending(ctx, db, fx.download, 1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := darkcheck.JudgePending(ctx, db, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	s, err = p.Publish(ctx)
	if err != nil || s.Deleted != 1 || s.Rows != 1 {
		t.Fatalf("publish after darks %+v %v", s, err)
	}
	var left int64
	sched.Table(darkneed.Table).Count(&left)
	if left != 1 {
		t.Errorf("%d rows left", left)
	}
}

func TestBacklogOnSQLite(t *testing.T) {
	t.Parallel()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if raw, err := db.DB(); err == nil {
		raw.SetMaxOpenConns(1)
	}
	sched, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	checkBacklog(t, db, sched)
}

func TestBacklogOnPostgres(t *testing.T) {
	t.Parallel()
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()
	c, err := postgres.Run(ctx, "postgres:17-alpine", postgres.WithDatabase("stacker"), postgres.WithUsername("stacker"), postgres.WithPassword("stacker"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(2*time.Minute)))
	testcontainers.CleanupContainer(t, c)
	if err != nil {
		t.Fatal(err)
	}
	dsn, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(pgdriver.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	checkBacklog(t, db, db)
}

func TestPublishSkipsWithoutTable(t *testing.T) {
	t.Parallel()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		mode, want string
		sched      *gorm.DB
	}{{darkneed.PublishOff, darkneed.SkipOff, db}, {darkneed.PublishOn, darkneed.SkipNoScheduler, nil}, {darkneed.PublishOn, darkneed.SkipNoTable, db}} {
		p := &darkneed.Publisher{App: db, Sched: c.sched, Mode: c.mode}
		if s, err := p.Publish(context.Background()); err != nil || s.Skipped != c.want {
			t.Errorf("%s: %+v %v", c.mode, s, err)
		}
	}
	var nilPub *darkneed.Publisher
	if s := nilPub.Status(); s.Mode != darkneed.PublishOff {
		t.Errorf("nil publisher %+v", s)
	}
}

func TestExistingDarksAreRecordedOnly(t *testing.T) {
	t.Parallel()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if raw, err := db.DB(); err == nil {
		raw.SetMaxOpenConns(1)
	}
	fx := seed(t, db)
	ctx := context.Background()
	oldLeak := 9.0
	if err := db.Model(&app.Frame{}).Where("key = ?", "DARK/d5_0.fits").Update("light_leak", oldLeak).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := darkcheck.MeasurePending(ctx, db, fx.download, 1); err != nil {
		t.Fatal(err)
	}
	before, err := coverage.Sets(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := darkcheck.JudgePending(ctx, db, fx.now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	var lit, kept app.Frame
	db.Where("key = ?", "DARK/d13_1.fits").First(&lit)
	db.Where("key = ?", "DARK/d5_0.fits").First(&kept)
	if lit.CalCheck == nil || *lit.CalCheck != darkcheck.StateLeak || lit.LightLeak != nil {
		t.Errorf("existing lit dark: check %v leak %v, want the verdict recorded and the dark kept", lit.CalCheck, lit.LightLeak)
	}
	if kept.LightLeak == nil || *kept.LightLeak != oldLeak {
		t.Errorf("an earlier rejection was cleared: %v", kept.LightLeak)
	}
	after, err := coverage.Sets(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("sets changed: %d to %d", len(before), len(after))
	}
	for i := range before {
		if before[i].Count != after[i].Count {
			t.Errorf("set %d changed from %d to %d frames", i, before[i].Count, after[i].Count)
		}
	}
	if _, _, err := darkcheck.JudgePending(ctx, db, fx.now.Add(-6*time.Hour)); err != nil {
		t.Fatal(err)
	}
	db.Where("key = ?", "DARK/d13_1.fits").First(&lit)
	if lit.LightLeak != nil {
		t.Errorf("a recorded verdict was applied later: %v", lit.LightLeak)
	}
}
