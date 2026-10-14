package morningreport

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestLogsRangePagesWithoutLosingLinesThatShareATime(t *testing.T) {
	t.Parallel()
	base := at("01:00:00")
	all := make([]Line, 0, pageSize+3)
	for i := range pageSize - 1 {
		all = append(all, Line{T: base.Add(time.Duration(i) * time.Millisecond), Text: strconv.Itoa(i)})
	}
	tie := base.Add(time.Hour)
	all = append(all, Line{T: tie, Text: "a"}, Line{T: tie, Text: "b"}, Line{T: tie, Text: "c"}, Line{T: tie.Add(time.Second), Text: "d"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		from, _ := strconv.ParseInt(r.URL.Query().Get("start"), 10, 64)
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		var vals [][2]string
		for _, l := range all {
			if l.T.UnixNano() >= from && len(vals) < limit {
				vals = append(vals, [2]string{strconv.FormatInt(l.T.UnixNano(), 10), l.Text})
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"result": []any{map[string]any{"values": vals}}}})
	}))
	t.Cleanup(srv.Close)
	got, err := Logs{URL: srv.URL}.Range(t.Context(), "{}", base, tie.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(all) {
		t.Fatalf("got %d lines, want %d", len(got), len(all))
	}
	seen := map[string]bool{}
	for _, l := range got {
		if seen[l.Text] {
			t.Fatalf("%q twice", l.Text)
		}
		seen[l.Text] = true
	}
}
