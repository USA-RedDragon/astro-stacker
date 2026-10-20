package cmd

import (
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/activity"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/surveys"
)

func TestATLASItem(t *testing.T) {
	t.Parallel()
	if it := atlasItem(0, surveys.ATLASRuntimes{}); it.State != activity.StateIdle || it.Detail != "" {
		t.Fatalf("empty line %+v", it)
	}
	if it := atlasItem(223, surveys.ATLASRuntimes{}); it.State != activity.StateQueued || it.Queued != 223 || it.Detail != "time to clear not measured yet" {
		t.Fatalf("unmeasured %+v", it)
	}
	rt := surveys.ATLASRuntimes{Seconds: []float64{1271, 768, 5047, 1928, 983}}
	it := atlasItem(223, rt)
	if want := "about 5.2 days to clear at ATLAS's pace of one of our tasks every 33 min over the last 5"; it.Detail != want {
		t.Fatalf("%q, want %q", it.Detail, want)
	}
	if it := atlasItem(3, rt); it.Detail != "about 2 h to clear at ATLAS's pace of one of our tasks every 33 min over the last 5" {
		t.Fatalf("%q", it.Detail)
	}
}
