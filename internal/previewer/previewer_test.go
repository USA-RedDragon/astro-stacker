package previewer_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/preview"
	"github.com/USA-RedDragon/astro-stacker/internal/previewer"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/wake"
	"github.com/glebarez/sqlite"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"gorm.io/gorm"
)

// A light indexed while the renderer is idle is taken up when Wake fires,
// not at the end of the interval.
func TestRunWakes(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no such key", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	s3, err := minio.New(strings.TrimPrefix(srv.URL, "http://"), &minio.Options{
		Creds: credentials.NewStaticV4("k", "s", ""), Region: "us-east-1"})
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
	p := previewer.New(s3, "astro", "processed", db, 1, preview.Options{MaxWidth: 64, Quality: 80})
	p.Wake = &wake.Signal{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		p.Run(ctx, time.Hour)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	time.Sleep(50 * time.Millisecond)
	f := app.Frame{Key: "M31/LIGHT/new.fits", Type: "LIGHT", LastModified: time.Now()}
	if err := db.Create(&f).Error; err != nil {
		t.Fatal(err)
	}
	p.Wake.Notify()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var got app.Frame
		if err := db.First(&got, f.ID).Error; err != nil {
			t.Fatal(err)
		}
		if got.PreviewAt != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the new light was not taken up after Wake")
}
