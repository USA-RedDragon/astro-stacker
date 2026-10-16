package observatory_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/observatory"
)

const calibrationJSON = `{"kind":"darks","placement":"while_unsafe","since":"2026-10-15T11:00:00Z","updated_at":"2026-10-15T11:20:00Z",` +
	`"camera":{"connected":true,"temperature":-10.1,"setpoint":-10,"cooler_power":42.5},"ambient":12.3,` +
	`"sun_limit_at":"2026-10-15T12:10:00Z","sun_limit_degrees":-8,` +
	`"cooling":{"target":-10,"from":15.2,"started_at":"2026-10-15T10:40:00Z","ends_at":"2026-10-15T11:00:00Z","rate_c_per_min":1.5,"source":"darks"},` +
	`"set":{"combo_key":"300|0|50|1|-10","exposure":300,"gain":0,"offset":50,"binning":1,"readout_mode":"Normal","set_temp":-10,` +
	`"filter":"L","target_name":"M 31","target_id":7,"session_date":"2026-10-14T00:00:00Z","rotation":0,"panel_brightness":0,` +
	`"frames_total":25,"frames_before":3,"frames_done":0,"index":1,"count":2,"started_at":"2026-10-15T11:00:00Z","ends_at":"2026-10-15T13:00:00Z"},` +
	`"frame":{"number":5,"seconds":300,"started_at":"2026-10-15T11:20:00Z","ends_at":"2026-10-15T11:25:00Z"},` +
	`"queue":[{"kind":"darks","combo_key":"300|0|50|1|-10","exposure":300,"gain":0,"offset":50,"binning":1,"set_temp":-10,"filter":"L","target_name":"M 31",` +
	`"frames_total":25,"frames_have":3,"remaining":22,"lights_blocked":40,"lights_scaled":0,"status":"taking","reason_code":"sun","reason":"The sun is up.",` +
	`"planned_start":"2026-10-15T11:00:00Z","planned_end":"2026-10-15T12:10:00Z","fit_frames":0}],` +
	`"taken":[{"kind":"darks","combo_key":"300|0|50|1|-10","exposure":300,"gain":0,"offset":50,"binning":1,"set_temp":-10,"filter":"L","target_name":"M 31",` +
	`"panel_brightness":0,"frames":4,"started_at":"2026-10-15T11:00:00Z","ended_at":"2026-10-15T11:20:00Z"}],` +
	`"stop":{"at":"2026-10-15T12:10:00Z","code":"sun","reason":"The sun reached -8°."},` +
	`"log":[{"at":"2026-10-15T11:00:00Z","text":"Started darks."}]}`

func TestStatusCalibrationPassesThroughUnchanged(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r) {
			return
		}
		_, _ = io.WriteString(w, `{"version":"5.8.2.231","state":"calibrating","calibration":`+calibrationJSON+`}`)
	}))
	defer srv.Close()

	s, err := observatory.NewClient(srv.URL, token).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.State != "calibrating" || s.Calibration == nil || s.Calibration.Set == nil || s.Calibration.Set.FramesDone == nil || *s.Calibration.Set.FramesDone != 0 {
		t.Fatalf("status %+v", s)
	}
	b, err := json.Marshal(observatory.View{Status: &s, Reachable: observatory.Online})
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Calibration map[string]any `json:"calibration"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	var want map[string]any
	if err := json.Unmarshal([]byte(calibrationJSON), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out.Calibration, want) {
		t.Fatalf("calibration changed on the way through:\n got %s\nwant %s", b, calibrationJSON)
	}

	b, err = json.Marshal(observatory.View{Status: &observatory.Status{State: "calibrating", Calibration: &observatory.Calibration{Kind: "cooling"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"calibration":{"kind":"cooling"}`) {
		t.Fatalf("sparse calibration json %s", b)
	}
	b, err = json.Marshal(observatory.View{Status: &observatory.Status{State: stateImaging}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "calibration") {
		t.Fatalf("absent calibration appeared: %s", b)
	}
}
