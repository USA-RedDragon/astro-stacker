package indexer

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/events"
	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/measure"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"gorm.io/gorm"
)

const lightFrame = "LIGHT"

// Lights without a pointing are read again when a new way of finding one
// comes (PointingRevision), once; lights with one, or read at the current
// revision, are left alone.
func TestPointingUnread(t *testing.T) {
	t.Parallel()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.Frame{}); err != nil {
		t.Fatal(err)
	}
	ra := 10.0
	for _, f := range []app.Frame{
		{Key: "nina", MountRA: &ra, MountDec: &ra, PointingRead: true},
		{Key: "telescope-live-old", PointingRead: true},
		{Key: "telescope-live-read", PointingRead: true, PointingWCS: true},
		{Key: "telescope-live-current", PointingRead: true, PointingWCS: true, PointingRev: PointingRevision},
		{Key: "never-read"},
	} {
		f.Type, f.LastModified = lightFrame, time.Now()
		if err := db.Create(&f).Error; err != nil {
			t.Fatal(err)
		}
	}
	var keys []string
	if err := pointingUnread(db).Order("key").Pluck("key", &keys).Error; err != nil {
		t.Fatal(err)
	}
	if len(keys) != 3 || keys[0] != "never-read" || keys[1] != "telescope-live-old" || keys[2] != "telescope-live-read" {
		t.Errorf("read again %v, want never-read, telescope-live-old and telescope-live-read", keys)
	}
}

// Every light is photometered once at the current revision, except those no
// master takes whatever their score.
func TestPhotometryPending(t *testing.T) {
	t.Parallel()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.Frame{}, &app.StackFrame{}); err != nil {
		t.Fatal(err)
	}
	old, cur := measure.PhotometryRevision-1, measure.PhotometryRevision
	for _, c := range []struct {
		key    string
		rev    *int
		status string
	}{
		{"new", nil, ""}, {"old-rev", &old, app.StackStatusAdded}, {"measured", &cur, app.StackStatusAdded},
		{"low", nil, app.StackStatusLowScore}, {"elsewhere", nil, app.StackStatusOffTarget},
		{"twice", nil, app.StackStatusDuplicate}, {"dead", nil, app.StackStatusDead},
	} {
		f := app.Frame{Key: c.key, Type: "LIGHT", LastModified: time.Now(), PhotometryRev: c.rev}
		if err := db.Create(&f).Error; err != nil {
			t.Fatal(err)
		}
		if c.status != "" {
			if err := db.Create(&app.StackFrame{FrameID: f.ID, Status: c.status}).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := db.Create(&app.Frame{Key: "flat", Type: "FLAT", LastModified: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	var keys []string
	if err := photometryPending(db).Order("key").Pluck("key", &keys).Error; err != nil {
		t.Fatal(err)
	}
	if want := []string{"low", "new", "old-rev"}; !slices.Equal(keys, want) {
		t.Errorf("pending %v, want %v", keys, want)
	}
}

func TestFillFrameKeepsTargetSchedulerGuids(t *testing.T) {
	t.Parallel()
	var f app.Frame
	fillFrame(&f, frameheader.Frame{TSProject: "p", TSTarget: "t", TSPanel: 3})
	if f.TSProject == nil || *f.TSProject != "p" || f.TSTarget == nil || *f.TSTarget != "t" || f.TSExposurePlan != nil || f.TSPanel == nil || *f.TSPanel != 3 {
		t.Errorf("frame %+v", f)
	}
	var g app.Frame
	fillFrame(&g, frameheader.Frame{})
	if g.TSProject != nil || g.TSTarget != nil || g.TSPanel != nil {
		t.Errorf("frame without guids %+v", g)
	}
}

func TestFillFrameTagsCollabLightsAndLeavesOthersNull(t *testing.T) {
	t.Parallel()
	var f app.Frame
	fillFrame(&f, frameheader.Frame{Type: lightFrame, Object: "M31 panel 2", CollabProject: collabID, CollabPanel: 2})
	if f.CollabProject == nil || *f.CollabProject != collabID || f.CollabPanel == nil || *f.CollabPanel != 2 || f.Object != "M31 panel 2" || f.Type != lightFrame {
		t.Errorf("collab light %+v", f)
	}
	var g app.Frame
	fillFrame(&g, frameheader.Frame{Type: lightFrame, Object: m31})
	if g.CollabProject != nil || g.CollabPanel != nil {
		t.Errorf("own light tagged %v %v", g.CollabProject, g.CollabPanel)
	}
	cols := geometryColumns(frameheader.Frame{CollabProject: "p", CollabPanel: 3})
	if p, ok := cols["collab_project"].(*string); !ok || p == nil || *p != "p" {
		t.Errorf("collab_project column %v", cols["collab_project"])
	}
	if n, ok := cols["collab_panel"].(*int); !ok || n == nil || *n != 3 {
		t.Errorf("collab_panel column %v", cols["collab_panel"])
	}
	if p, ok := geometryColumns(frameheader.Frame{})["collab_project"].(*string); !ok || p != nil {
		t.Errorf("untagged collab_project column %v", p)
	}
}

func TestGeometryUnreadAndFill(t *testing.T) {
	t.Parallel()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.Frame{}); err != nil {
		t.Fatal(err)
	}
	var read app.Frame
	fillFrame(&read, frameheader.Frame{Width: 6248, Height: 4176, FocalLength: 405, PixelSize: 3.76, Telescope: "FRA400"})
	if read.Width == nil || *read.Width != 6248 || *read.Height != 4176 || *read.FocalLength != 405 || *read.PixelSize != 3.76 || *read.Telescope != "FRA400" ||
		read.BayerPattern != nil || read.GeometryRev == nil || *read.GeometryRev != GeometryRevision {
		t.Fatalf("filled %+v", read)
	}
	broken := "bad header"
	for i, f := range []app.Frame{read, {}, {IndexError: &broken}} {
		f.Key, f.Type, f.LastModified = fmt.Sprintf("k%d", i), lightFrame, time.Now()
		if err := db.Create(&f).Error; err != nil {
			t.Fatal(err)
		}
	}
	var n int64
	if err := geometryUnread(db).Count(&n).Error; err != nil || n != 1 {
		t.Errorf("unread %d, %v", n, err)
	}
	cols := geometryColumns(frameheader.Frame{Width: 10, FocalLength: math.NaN(), PixelSize: math.NaN()})
	width, okW := cols["width"].(*int)
	height, okH := cols["height"].(*int)
	focal, okF := cols["focal_length"].(*float64)
	if !okW || !okH || !okF || width == nil || height != nil || focal != nil || cols["geometry_rev"] != GeometryRevision {
		t.Errorf("columns %+v", cols)
	}
}

// Measured lights raise one subs event per target, in name order, and a
// pass that measured nothing raises none.
func TestPublishSubs(t *testing.T) {
	t.Parallel()
	b := events.NewBroker()
	_, ch, cancel := b.Subscribe(0)
	defer cancel()
	ix := &Indexer{Events: b}
	ix.publishSubs(map[string]bool{})
	ix.publishSubs(map[string]bool{"M 33": true, "M 31": true})
	got := make([]string, 0, 2)
	for range 2 {
		msg := string(<-ch)
		if !strings.Contains(msg, `"type":"subs"`) {
			t.Fatalf("message %q is not a subs event", msg)
		}
		got = append(got, msg[strings.Index(msg, `"object":`):strings.Index(msg, `,"time"`)])
	}
	if want := []string{`"object":"M 31"`, `"object":"M 33"`}; !slices.Equal(got, want) {
		t.Errorf("events %v, want %v", got, want)
	}
	select {
	case msg := <-ch:
		t.Errorf("extra event %q", msg)
	default:
	}
	(&Indexer{}).publishSubs(map[string]bool{"M 31": true})
}

const (
	m31      = "M31"
	collabID = "d07d82eb861e"
)

func collabLight(t *testing.T, object, project string, panel int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := imagedata.WriteFITS(&buf, 8, 8, 1, make([]float32, 64), []imagedata.Card{
		imagedata.StringCard("IMAGETYP", "LIGHT", ""),
		imagedata.StringCard("OBJECT", object, ""),
		imagedata.StringCard("FILTER", "H-a", ""),
		imagedata.FloatCard("EXPTIME", 300, ""),
		imagedata.StringCard("DATE-OBS", "2026-10-13T04:00:00", ""),
		imagedata.StringCard("STARFRNT", project, "Starfront collaboration"),
		imagedata.IntCard("SFPANEL", panel, "Starfront panel number"),
	}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestIndexKeepsOurCollabLightsAndNeverIndexesSharedData(t *testing.T) {
	t.Parallel()
	bucket := &bucketServer{objs: map[string][]byte{
		"M31/LIGHT/own.fits":                                 fitsLight(t, m31),
		"M31 NINA TEST COLLAB panel 2/LIGHT/ours.fits":       collabLight(t, "M31 NINA TEST COLLAB panel 2", collabID, 2),
		"starfront-shared/d07d82eb861e/agent-7/theirs.fits":  collabLight(t, "M31 NINA TEST COLLAB panel 2", collabID, 2),
		"starfront-shared/d07d82eb861e/agent-7/theirs2.fits": fitsLight(t, m31),
	}}
	srv := httptest.NewServer(bucket)
	t.Cleanup(srv.Close)
	s3, err := minio.New(strings.TrimPrefix(srv.URL, "http://"), &minio.Options{Creds: credentials.NewStaticV4("k", "s", ""), Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&app.Frame{}); err != nil {
		t.Fatal(err)
	}
	stray := app.Frame{Key: "starfront-shared/d07d82eb861e/agent-7/theirs.fits", ETag: "old", Type: lightFrame, LastModified: time.Now()}
	if err := db.Create(&stray).Error; err != nil {
		t.Fatal(err)
	}
	stats, err := New(s3, "astro", db, 2).IndexOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Seen != 2 || stats.Indexed != 2 || stats.Removed != 1 {
		t.Errorf("stats %+v", stats)
	}
	var frames []app.Frame
	if err := db.Order("key").Find(&frames).Error; err != nil {
		t.Fatal(err)
	}
	if len(frames) != 2 {
		t.Fatalf("frames %+v", frames)
	}
	ours, own := frames[0], frames[1]
	if ours.CollabProject == nil || *ours.CollabProject != collabID || ours.CollabPanel == nil || *ours.CollabPanel != 2 ||
		ours.Object != "M31 NINA TEST COLLAB panel 2" || ours.Type != lightFrame || ours.Filter != "H-a" {
		t.Errorf("our collab light %+v", ours)
	}
	if own.CollabProject != nil || own.CollabPanel != nil || own.Object != m31 {
		t.Errorf("own light %+v", own)
	}
	for _, k := range []string{"starfront-shared/x.fits", "/starfront-shared/p/a.xisf"} {
		if Indexable(k) {
			t.Errorf("%s indexable", k)
		}
	}
	if !Indexable("M31/LIGHT/a.xisf") || Indexable("M31/LIGHT/a.txt") {
		t.Error("ordinary keys")
	}
}

func TestGeometryBackfillRereadsFramesFromBeforeCollabTags(t *testing.T) {
	t.Parallel()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.Frame{}); err != nil {
		t.Fatal(err)
	}
	one, cur := 1, GeometryRevision
	for i, rev := range []*int{&one, &cur, nil} {
		f := app.Frame{Key: string(rune('a' + i)), Type: lightFrame, LastModified: time.Now(), GeometryRev: rev}
		if err := db.Create(&f).Error; err != nil {
			t.Fatal(err)
		}
	}
	var keys []string
	if err := geometryUnread(db).Order("key").Pluck("key", &keys).Error; err != nil || strings.Join(keys, ",") != "a,c" {
		t.Errorf("unread %v %v", keys, err)
	}
}
