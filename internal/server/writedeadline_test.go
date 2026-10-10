package server_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/server"
	"github.com/gin-gonic/gin"
)

func TestExtendWriteDeadlineOutlastsServerWriteTimeout(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	slow := func(c *gin.Context) {
		time.Sleep(200 * time.Millisecond)
		c.String(http.StatusOK, "ok")
	}
	r.GET("/short", slow)
	g := r.Group("/long")
	g.Use(server.ExtendWriteDeadline(2 * time.Second))
	g.GET("", slow)

	srv := httptest.NewUnstartedServer(r)
	srv.Config.WriteTimeout = 50 * time.Millisecond
	srv.Start()
	defer srv.Close()

	get := func(path string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		return http.DefaultClient.Do(req)
	}

	res, err := get("/long")
	if err != nil {
		t.Fatalf("extended route: %v", err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("extended route: %d %q", res.StatusCode, body)
	}

	res, err = get("/short")
	if err == nil {
		body, _ = io.ReadAll(res.Body)
		_ = res.Body.Close()
		if string(body) == "ok" {
			t.Fatal("route without the extension answered after the server write timeout")
		}
	}
}
