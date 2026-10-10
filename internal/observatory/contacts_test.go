package observatory_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/observatory"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type memContacts struct {
	mu    sync.Mutex
	at    time.Time
	saves int
}

func (m *memContacts) LoadLastAnswer(context.Context) (time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.at, nil
}

func (m *memContacts) SaveLastAnswer(_ context.Context, t time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.at = t
	m.saves++
	return nil
}

func TestSinceIsTheLastAnswerAcrossRestarts(t *testing.T) {
	t.Parallel()
	answered := time.Date(2026, 10, 9, 22, 0, 0, 0, time.UTC)
	store := &memContacts{at: answered}
	now := answered.Add(3 * time.Hour)
	m := observatory.NewMonitor(observatory.NewClient("http://127.0.0.1:1", "x"), nil, nil)
	m.Contacts = store
	m.Now = func() time.Time { return now }
	m.LoadLastAnswer(context.Background())
	m.MarkDown(errors.New("dial"))
	v := m.View()
	if v.Since == nil || !v.Since.Equal(answered) || v.LastAnswer == nil || !v.LastAnswer.Equal(answered) {
		t.Fatalf("want not answering since %s: %+v", answered, v)
	}
	m.SetStatus(observatory.Status{State: "imaging"}, false)
	if v = m.View(); v.Since == nil || !v.Since.Equal(now) {
		t.Fatalf("back online since %s: %+v", now, v)
	}
	m.SaveLastAnswer(context.Background())
	m.SaveLastAnswer(context.Background())
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.saves != 1 || !store.at.Equal(now) {
		t.Fatalf("saves %d at %s", store.saves, store.at)
	}
}

func TestGormContacts(t *testing.T) {
	t.Parallel()
	db, err := gorm.Open(sqlite.Open("file:contacts?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.ObservatoryContact{}); err != nil {
		t.Fatal(err)
	}
	g := observatory.GormContacts{DB: db}
	ctx := context.Background()
	if at, err := g.LoadLastAnswer(ctx); err != nil || !at.IsZero() {
		t.Fatalf("empty: %s %v", at, err)
	}
	for _, at := range []time.Time{time.Date(2026, 10, 9, 1, 0, 0, 0, time.UTC), time.Date(2026, 10, 9, 2, 0, 0, 0, time.UTC)} {
		if err := g.SaveLastAnswer(ctx, at); err != nil {
			t.Fatal(err)
		}
		if got, err := g.LoadLastAnswer(ctx); err != nil || !got.Equal(at) {
			t.Fatalf("got %s %v want %s", got, err, at)
		}
	}
}
