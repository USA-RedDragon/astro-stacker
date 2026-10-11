package schedcmd_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
)

const abortPayload = `{"plan_id":40,"started_at":"2026-10-10T06:45:00.1234567Z","target_name":"Panel 2","filter":"Ha"}`

type deferringTransport struct{ fakeTransport }

func (*deferringTransport) Defers() bool { return true }

type refusingTransport struct{ fakeTransport }

func (*refusingTransport) Send(context.Context, schedcmd.Envelope) (schedcmd.Result, error) {
	return schedcmd.Result{}, errors.New("POST /os/v1/commands: 400 Bad Request: unknown command kind exposure.abort")
}

func TestAbortExposureValidates(t *testing.T) {
	t.Parallel()
	spec, err := schedcmd.Default().Get(schedcmd.KindAbortExposure)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Category() != schedcmd.CategoryControl || spec.Destination() != schedcmd.DestinationObservatory {
		t.Fatalf("category %s destination %s", spec.Category(), spec.Destination())
	}
	for _, p := range []string{``, `null`, `{}`, `{"plan_id":40}`, `{"started_at":"2026-10-10T06:45:00Z"}`, `{"plan_id":0,"started_at":"2026-10-10T06:45:00Z"}`, `{"plan_id":40,"started_at":"soon"}`} {
		if err := spec.Validate(json.RawMessage(p)); !errors.Is(err, schedcmd.ErrInvalid) {
			t.Fatalf("%q: %v", p, err)
		}
	}
	if err := spec.Validate(json.RawMessage(abortPayload)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := spec.Inverse(json.RawMessage(abortPayload)); !errors.Is(err, schedcmd.ErrNoInverse) {
		t.Fatalf("inverse %v", err)
	}
}

func TestAbortExposureDescribesTheFrameItDiscards(t *testing.T) {
	t.Parallel()
	spec, _ := schedcmd.Default().Get(schedcmd.KindAbortExposure)
	d, err := spec.Describe(json.RawMessage(abortPayload))
	if err != nil {
		t.Fatal(err)
	}
	if d.Title != "Abort the exposure on Panel 2" {
		t.Fatalf("title %q", d.Title)
	}
	if len(d.Diffs) != 1 || string(d.Diffs[0].Before) != `"Ha exposure on Panel 2, started 2026-10-10 06:45:00 UTC"` || string(d.Diffs[0].After) != `"Aborted, partial frame discarded"` {
		t.Fatalf("diffs %+v", d.Diffs)
	}
	if !strings.Contains(d.Note, "not saved") {
		t.Fatalf("note %q", d.Note)
	}
}

func TestAbortExposureAppliesAtOnceOverTheAPI(t *testing.T) {
	t.Parallel()
	api := &fakeTransport{name: apiName, up: true, status: schedcmd.StatusApplied}
	queue := &deferringTransport{fakeTransport{name: queueName, up: true, status: schedcmd.StatusQueued}}
	s := newService(t, api, queue)
	r, err := s.Submit(context.Background(), schedcmd.KindAbortExposure, json.RawMessage(abortPayload), author)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != schedcmd.StatusApplied || r.Transport != apiName {
		t.Fatalf("got %+v", r)
	}
	if len(api.sent) != 1 || api.sent[0].Kind != schedcmd.KindAbortExposure || len(queue.sent) != 0 {
		t.Fatalf("api %d queue %d", len(api.sent), len(queue.sent))
	}
	got, _ := s.Log.List(context.Background(), schedcmd.Filter{Category: schedcmd.CategoryControl})
	if len(got) != 1 || got[0].ID != r.ID {
		t.Fatalf("history %+v", got)
	}
	if _, err := s.Undo(context.Background(), r.ID, author); !errors.Is(err, schedcmd.ErrNoInverse) {
		t.Fatalf("undo %v", err)
	}
}

func TestAbortExposureIsNeverQueuedForLater(t *testing.T) {
	t.Parallel()
	api := &fakeTransport{name: apiName}
	queue := &deferringTransport{fakeTransport{name: queueName, up: true, status: schedcmd.StatusQueued}}
	s := newService(t, api, queue)
	r, err := s.Submit(context.Background(), schedcmd.KindAbortExposure, json.RawMessage(abortPayload), author)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != schedcmd.StatusFailed || !strings.Contains(r.Message, "did not answer") || len(queue.sent) != 0 {
		t.Fatalf("got %+v, queue %d", r, len(queue.sent))
	}
	api.up = true
	if n := s.Redeliver(context.Background()); n != 0 || len(api.sent) != 0 {
		t.Fatalf("redelivered %d", n)
	}
}

func TestAbortExposureReportsARefusal(t *testing.T) {
	t.Parallel()
	s := newService(t, &refusingTransport{fakeTransport{name: apiName, up: true}})
	r, err := s.Submit(context.Background(), schedcmd.KindAbortExposure, json.RawMessage(abortPayload), author)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != schedcmd.StatusFailed || !strings.Contains(r.Message, "unknown command kind") {
		t.Fatalf("got %+v", r)
	}
}

func TestRedeliverFailsAQueuedAbortInsteadOfSendingItLate(t *testing.T) {
	t.Parallel()
	api := &fakeTransport{name: apiName, up: true, status: schedcmd.StatusApplied}
	s := newService(t, api)
	ctx := context.Background()
	rec := schedcmd.Record{ID: "stale", Kind: schedcmd.KindAbortExposure, Payload: json.RawMessage(abortPayload), Author: author, Title: "Abort the exposure", Category: schedcmd.CategoryControl, Destination: schedcmd.DestinationObservatory, Status: schedcmd.StatusQueued}
	if err := s.Log.Create(ctx, &rec); err != nil {
		t.Fatal(err)
	}
	s.Redeliver(ctx)
	got, err := s.Log.Get(ctx, "stale")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != schedcmd.StatusFailed || len(api.sent) != 0 {
		t.Fatalf("got %s, sent %d", got.Status, len(api.sent))
	}
}
