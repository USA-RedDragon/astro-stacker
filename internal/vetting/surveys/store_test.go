package surveys

import (
	"net/http"
	"testing"
)

func TestSizes(t *testing.T) {
	t.Parallel()
	f, st := testFetcher(t, http.NotFoundHandler())
	ctx := t.Context()
	for k, n := range map[string]int{
		"vetting/cache/a/ztf/dr24/1/x.bin.zst": 10, "vetting/cache/a/ztf/manifest.json.zst": 5,
		"vetting/cache/a/atlas/1.txt.zst": 7, "vetting/cache/b/tess/1/s0016.csv.zst": 3, "vetting/tess/requests/1.json": 99,
	} {
		_ = st.Put(ctx, k, make([]byte, n), "")
	}
	s, err := f.Sizes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s["a"]["ztf"] != 15 || s["a"]["atlas"] != 7 || s["b"]["tess"] != 3 || len(s) != 2 {
		t.Errorf("%v", s)
	}
	if FieldSlug(Field{Object: "Cygnus Loop, Panel 2"}) != "cygnus-loop-panel-2" {
		t.Error(FieldSlug(Field{Object: "Cygnus Loop, Panel 2"}))
	}
}
