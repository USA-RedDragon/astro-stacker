package middleware_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/server/middleware"
	"github.com/USA-RedDragon/astro-stacker/internal/store"
	"github.com/gin-gonic/gin"
	gormlib "gorm.io/gorm"
)

type requestKey struct{}

type taggedStore struct{ request string }

func (s taggedStore) WithContext(ctx context.Context) store.Store {
	name, _ := ctx.Value(requestKey{}).(string)
	return taggedStore{request: name}
}

func (s taggedStore) DB() *gormlib.DB { return nil }

func request(t *testing.T, s store.Store) string {
	t.Helper()
	ts, ok := s.(taggedStore)
	if !ok {
		t.Fatalf("store %T", s)
	}
	return ts.request
}

func TestInjectKeepsEachRequestsOwnContext(t *testing.T) {
	t.Parallel()
	shared := &middleware.DepInjection{AppStore: taggedStore{}, SchedulerDBStore: taggedStore{}}
	inject := middleware.Inject(shared)
	run := func(name string) *middleware.DepInjection {
		ctx := context.WithValue(context.Background(), requestKey{}, name)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil)
		inject(c)
		di, ok := c.MustGet(middleware.DepInjectionKey).(*middleware.DepInjection)
		if !ok {
			t.Fatal("no injection")
		}
		return di
	}
	first := run("first")
	run("second")
	if got := request(t, first.AppStore); got != "first" {
		t.Fatalf("app store bound to the %q request", got)
	}
	if got := request(t, first.SchedulerDBStore); got != "first" {
		t.Fatalf("scheduler store bound to the %q request", got)
	}
	if request(t, shared.AppStore) != "" || request(t, shared.SchedulerDBStore) != "" {
		t.Fatal("the shared injection was changed")
	}
}
