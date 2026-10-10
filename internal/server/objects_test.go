package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/config"
	"github.com/USA-RedDragon/astro-stacker/internal/server/middleware"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/gin-gonic/gin"
)

func TestObjectsRouteUsesFrameLinks(t *testing.T) {
	t.Parallel()
	sched := memStore(t)
	appStore := memStore(t)
	if err := appStore.DB().AutoMigrate(&app.Frame{}, &app.StackFrame{}, &app.FrameTarget{}, &app.MosaicPanel{}); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		`create table project ("Id" integer primary key, name text, "isMosaic" integer, minimumaltitude real, guid text)`,
		`create table target ("Id" integer primary key, name text, active integer, ra real, "dec" real, rotation real, projectid integer, guid text)`,
		`insert into project values (1,'M92',0,0,'p1'),(2,'Pleiades',0,0,'p2'),(3,'Orion',1,0,'p3')`,
		`insert into target values (10,'M92',1,0,0,0,1,'g10'),(11,'Pleiades',1,0,0,0,2,'g11'),(12,'Orion Panel 1',1,0,0,0,3,'g12'),(13,'Orion Panel 2',1,0,0,0,3,'g13')`,
	} {
		if err := sched.DB().Exec(s).Error; err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	id := 0
	frames := func(object string, n int, link *app.FrameTarget) {
		for range n {
			id++
			f := app.Frame{ID: id, Key: fmt.Sprintf("%s-%d", object, id), Type: lightType, Object: object, LastModified: now}
			if err := appStore.DB().Create(&f).Error; err != nil {
				t.Fatal(err)
			}
			if link != nil {
				l := *link
				l.FrameID, l.Object = id, object
				if err := appStore.DB().Create(&l).Error; err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	frames("M 92", 3, &app.FrameTarget{TargetGUID: "g10", Method: app.LinkHeader})
	frames("M 92", 2, nil)
	frames("Pleiades", 4, nil)
	frames("Pleiades", 1, &app.FrameTarget{TargetGUID: "g11", Method: app.LinkName})
	frames("Orion Panel 1", 2, nil)
	frames("Old Target", 2, &app.FrameTarget{TargetGUID: "gone", Method: app.LinkAcquiredImage})
	frames("Old Target", 1, nil)
	frames("Comet", 5, nil)

	r := gin.New()
	r.Use(middleware.Inject(&middleware.DepInjection{Config: &config.Config{}, AppStore: appStore, SchedulerDBStore: sched}))
	r.GET("/objects", objectsRoute)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/objects", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var got []objectOut
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]objectOut{
		"Comet":         {Name: "Comet", Lights: 5},
		"M 92":          {Name: "M 92", Lights: 5, Recorded: 3, Scheduled: true},
		"Old Target":    {Name: "Old Target", Lights: 3, Recorded: 2},
		"Orion Panel 1": {Name: "Orion Panel 1", Lights: 2, Scheduled: true},
		"Pleiades":      {Name: "Pleiades", Lights: 5, Scheduled: true},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for _, o := range got {
		if o != want[o.Name] {
			t.Errorf("%s: got %+v, want %+v", o.Name, o, want[o.Name])
		}
	}
}
