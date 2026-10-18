package cmd

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/activity"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/surveys"
)

func tessQ(gaia, object string, claimed bool, st *surveys.TESSStatus) tessQueued {
	q := tessQueued{key: surveys.TESSRequestPrefix + gaia + ".json", gaiaID: gaia, status: st}
	q.req.Field = surveys.Field{Object: object, Slug: "slug"}
	if claimed {
		q.claim = &surveys.TESSClaim{Worker: "pod-" + gaia, ClaimedAt: time.Date(2026, 10, 17, 3, 0, 0, 0, time.UTC)}
	}
	return q
}

func TestTESSItemsOnePerClaimedStar(t *testing.T) {
	t.Parallel()
	running := activity.JobInfo{Known: true, Running: true, Jobs: 1, Workers: 4}
	queue := []tessQueued{
		tessQ("1", objM13, true, &surveys.TESSStatus{SectorsAvailable: []int{24, 25, 51}, SectorsDone: []int{24},
			SectorsFailed: map[string]string{"51": "x"}}),
		tessQ("2", objAbell, false, nil),
		tessQ("3", objAbell, true, nil),
		tessQ("4", objAbell, false, nil),
		tessQ("5", objM13, true, &surveys.TESSStatus{SectorsAvailable: []int{24}, SectorsDone: []int{24}}),
	}
	items := tessItems(queue, running)
	if len(items) != 3 {
		t.Fatalf("want 3 items, got %+v", items)
	}
	a, b, c := items[0], items[1], items[2]
	if a.ID != "tess:1" || a.State != activity.StateRunning || a.Done != 2 || a.Total != 3 || a.Current != "M 13, sector 25" {
		t.Fatalf("first %+v", a)
	}
	if b.ID != "tess:3" || b.Current != "Abell 85: looking up its sectors" || b.Total != 0 {
		t.Fatalf("second %+v", b)
	}
	if c.Done != 1 || c.Total != 1 || c.Current != "M 13, Gaia 5" {
		t.Fatalf("third %+v", c)
	}
	for _, it := range items {
		if it.Queued != 2 || it.Started == nil || !it.Started.Equal(time.Date(2026, 10, 17, 3, 0, 0, 0, time.UTC)) {
			t.Fatalf("queued and started %+v", it)
		}
	}
}

func TestTESSItemsWithoutClaims(t *testing.T) {
	t.Parallel()
	queue := []tessQueued{tessQ("1", objM13, false, &surveys.TESSStatus{SectorsAvailable: []int{24, 25}, SectorsDone: []int{24}}),
		tessQ("2", objAbell, false, nil)}
	items := tessItems(queue, activity.JobInfo{Known: true})
	if len(items) != 1 || items[0].State != activity.StateQueued || items[0].Queued != 2 || items[0].Current != "M 13, Gaia 1" ||
		items[0].Done != 1 || items[0].Total != 2 || items[0].ID != "" {
		t.Fatalf("queued %+v", items)
	}
	items = tessItems(queue, activity.JobInfo{Known: true, Running: true})
	if len(items) != 1 || items[0].State != activity.StateRunning || items[0].Current != stepStarting || items[0].Queued != 1 {
		t.Fatalf("starting %+v", items)
	}
	items = tessItems(nil, activity.JobInfo{Known: true, Running: true})
	if len(items) != 1 || items[0].Current != "reading the queue" || items[0].Queued != 0 {
		t.Fatalf("empty queue %+v", items)
	}
	items = tessItems(nil, activity.JobInfo{Known: true})
	if len(items) != 1 || items[0].State != activity.StateIdle {
		t.Fatalf("idle %+v", items)
	}
}

func TestTESSClaimLive(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 17, 3, 0, 0, 0, time.UTC)
	c := surveys.TESSClaim{Expires: float64(now.Unix()) + 0.5}
	if !c.Live(now) || c.Live(now.Add(time.Second)) {
		t.Fatalf("live %+v", c)
	}
	doc := `{
 "gaia_id": "2179309875368546432",
 "worker": "discoveries-vetting-tess-29871587-7vl5r",
 "claimed_at": "2026-10-17T03:00:00Z",
 "renewed_at": "2026-10-17T03:00:00Z",
 "expires_at": "2026-10-17T03:15:00Z",
 "expires": 1792206900.25
}`
	var py surveys.TESSClaim
	if err := json.Unmarshal([]byte(doc), &py); err != nil {
		t.Fatal(err)
	}
	if py.GaiaID != 2179309875368546432 || !py.ClaimedAt.Equal(now) || !py.Live(now.Add(15*time.Minute)) ||
		py.Live(now.Add(15*time.Minute+time.Second)) {
		t.Fatalf("tessjob claim %+v", py)
	}
}
