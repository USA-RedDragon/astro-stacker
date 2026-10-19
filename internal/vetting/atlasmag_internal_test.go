package vetting

import "testing"

func TestBrightestReported(t *testing.T) {
	t.Parallel()
	reps := map[string]reported{"a": {mag: 18.6, ok: true}, "b": {mag: 19.2, ok: true}, "c": {mag: 12, ok: false}}
	got, ok := brightestReported(reps)
	if !ok || got != 18.6 {
		t.Fatalf("got %v %v", got, ok)
	}
	if 16.2 >= got-atlasMagSlack {
		t.Fatal("54898's ATLAS 16.2 against 18.6 reported must not confirm")
	}
}
