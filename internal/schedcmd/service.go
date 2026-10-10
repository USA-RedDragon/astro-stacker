package schedcmd

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"
)

var (
	ErrUnreachable = errors.New("observatory unreachable")
	ErrNotWaiting  = errors.New("command is no longer waiting")
	ErrNotUndoable = errors.New("command cannot be undone")
)

type authorKey struct{}

func WithAuthor(ctx context.Context, author string) context.Context {
	return context.WithValue(ctx, authorKey{}, author)
}

func AuthorFrom(ctx context.Context) string {
	a, _ := ctx.Value(authorKey{}).(string)
	return a
}

type Transport interface {
	Name() string
	Send(ctx context.Context, e Envelope) (Result, error)
	Cancel(ctx context.Context, id string) (Result, error)
}

type Service struct {
	Registry   *Registry
	Log        Log
	AppDB      *gorm.DB
	Transports []Transport
	Notify     func(Record)
	Now        func() time.Time
	NewID      func() string
}

func NewID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) newID() string {
	if s.NewID != nil {
		return s.NewID()
	}
	return NewID()
}

func (s *Service) registry() *Registry {
	if s.Registry == nil {
		s.Registry = Default()
	}
	return s.Registry
}

func (s *Service) notify(r Record) {
	if s.Notify != nil {
		s.Notify(r)
	}
}

func (s *Service) Submit(ctx context.Context, kind Kind, payload json.RawMessage, author string) (Record, error) {
	return s.submit(ctx, kind, payload, author, "")
}

func (s *Service) submit(ctx context.Context, kind Kind, payload json.RawMessage, author, undoOf string) (Record, error) {
	ctx = WithAuthor(ctx, author)
	spec, err := s.registry().Get(kind)
	if err != nil {
		return Record{}, err
	}
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	if err := spec.Validate(payload); err != nil {
		return Record{}, err
	}
	desc, err := spec.Describe(payload)
	if err != nil {
		return Record{}, err
	}
	if fx, ok := spec.(AppSideEffect); ok && spec.Destination() != DestinationApp {
		if err := fx.ApplyAppSide(ctx, s.AppDB, payload); err != nil {
			return Record{}, err
		}
	}
	now := s.now()
	r := Record{
		ID:          s.newID(),
		Kind:        kind,
		Payload:     payload,
		Author:      author,
		UndoOf:      undoOf,
		Title:       desc.Title,
		Category:    spec.Category(),
		Destination: spec.Destination(),
		Diffs:       desc.Diffs,
		Objects:     desc.Objects,
		Search:      searchText(desc, author),
		Note:        desc.Note,
		Status:      StatusQueued,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if undoOf != "" {
		r.Title = "Undo: " + r.Title
		r.Note = "Sends the reverse change. The original stays in history."
	}
	if spec.Destination() == DestinationApp {
		applier, _ := spec.(AppApplier)
		if err := applier.ApplyApp(ctx, s.AppDB, payload); err != nil {
			return Record{}, err
		}
		r.Status = StatusSaved
		r.Transport = "app"
		r.AppliedAt = &now
	}
	if err := s.Log.Create(ctx, &r); err != nil {
		return Record{}, err
	}
	if r.Destination == DestinationObservatory {
		if d, _ := s.deliver(ctx, r); d.ID != "" {
			r = d
		}
	}
	s.notify(r)
	return r, nil
}

func (s *Service) deliver(ctx context.Context, r Record) (Record, error) {
	env := r.Envelope()
	lastErr := ErrUnreachable
	for _, t := range s.Transports {
		res, err := t.Send(ctx, env)
		if err != nil {
			lastErr = err
			if !errors.Is(err, ErrUnreachable) {
				slog.Warn("Scheduler command transport failed", "transport", t.Name(), "id", r.ID, "error", err)
			}
			continue
		}
		res.ID = r.ID
		return s.record(ctx, res, t.Name())
	}
	updated, err := s.Log.Update(ctx, r.ID, func(rec *Record) error {
		rec.Attempts++
		rec.UpdatedAt = s.now()
		return nil
	})
	if err != nil {
		return r, err
	}
	return updated, lastErr
}

func (s *Service) Undo(ctx context.Context, id, author string) (Record, error) {
	r, err := s.Log.Get(ctx, id)
	if err != nil {
		return Record{}, err
	}
	if r.Status.Waiting() {
		return s.Cancel(ctx, id)
	}
	if !r.Undoable() {
		return Record{}, ErrNotUndoable
	}
	spec, err := s.registry().Get(r.Kind)
	if err != nil {
		return Record{}, err
	}
	kind, inv, err := spec.Inverse(r.Payload)
	if err != nil {
		return Record{}, err
	}
	nr, err := s.submit(ctx, kind, inv, author, r.ID)
	if err != nil {
		return Record{}, err
	}
	orig, err := s.Log.Update(ctx, r.ID, func(rec *Record) error {
		rec.UndoneBy = nr.ID
		rec.UpdatedAt = s.now()
		return nil
	})
	if err == nil {
		s.notify(orig)
	}
	return nr, nil
}

func (s *Service) Cancel(ctx context.Context, id string) (Record, error) {
	r, err := s.Log.Get(ctx, id)
	if err != nil {
		return Record{}, err
	}
	if !r.Status.Waiting() {
		return r, ErrNotWaiting
	}
	cancelled, cancelledBy, message := false, "", ""
	for _, t := range s.Transports {
		res, err := t.Cancel(ctx, id)
		if err != nil {
			continue
		}
		res.ID = id
		if res.Status != StatusCancelled {
			rec, err := s.record(ctx, res, "")
			if err != nil {
				return Record{}, err
			}
			return rec, ErrNotWaiting
		}
		if !cancelled {
			cancelled, cancelledBy, message = true, t.Name(), res.Message
		}
	}
	if !cancelled && r.Status == StatusPending {
		return r, ErrUnreachable
	}
	if message == "" {
		message = cancelMessage(r.Status, cancelled, cancelledBy)
	}
	rec, err := s.record(ctx, Result{ID: id, Status: StatusCancelled, Message: message, UpdatedAt: s.now()}, "")
	return rec, err
}

func cancelMessage(was Status, cancelled bool, by string) string {
	switch {
	case cancelled && by == "queue":
		return "Withdrawn from the database queue before the scheduler applied it."
	case cancelled:
		return "Cancelled on the observatory PC while it waited to apply."
	case was == StatusQueued:
		return "Cancelled before it was sent to the observatory."
	}
	return "Cancelled."
}

func (s *Service) ApplyResult(ctx context.Context, res Result) (Record, error) {
	return s.record(ctx, res, "")
}

func (s *Service) record(ctx context.Context, res Result, transport string) (Record, error) {
	changed := false
	var was Status
	rec, err := s.Log.Update(ctx, res.ID, func(r *Record) error {
		was = r.Status
		if !acceptResult(r.Status, res.Status) {
			return nil
		}
		changed = r.Status != res.Status || r.Message != res.Message || (transport != "" && r.Transport != transport)
		r.Status = res.Status
		r.Message = res.Message
		if len(res.Detail) > 0 {
			r.Detail = res.Detail
		}
		if res.Status == StatusPending && !res.Untimed {
			if !sameTime(r.AppliesAt, res.AppliesAt) {
				changed = true
			}
			r.AppliesAt = res.AppliesAt
		} else if res.AppliesAt != nil {
			r.AppliesAt = res.AppliesAt
		}
		if transport != "" && r.Transport == "" {
			r.Transport = transport
		}
		if res.Status == StatusApplied && r.AppliedAt == nil && !res.UpdatedAt.IsZero() {
			t := res.UpdatedAt
			r.AppliedAt = &t
		}
		r.UpdatedAt = s.now()
		return nil
	})
	if err != nil {
		return rec, err
	}
	if was.Waiting() && rec.Status.Final() && rec.Status != StatusApplied && rec.Status != StatusSaved {
		s.revertSideEffect(ctx, rec)
	}
	if changed {
		s.notify(rec)
	}
	return rec, nil
}

func (s *Service) revertSideEffect(ctx context.Context, r Record) {
	spec, err := s.registry().Get(r.Kind)
	if err != nil {
		return
	}
	if _, ok := spec.(AppSideEffect); !ok {
		return
	}
	kind, inv, err := spec.Inverse(r.Payload)
	if err != nil {
		return
	}
	ispec, err := s.registry().Get(kind)
	if err != nil {
		return
	}
	if fx, ok := ispec.(AppSideEffect); ok {
		if err := fx.ApplyAppSide(ctx, s.AppDB, inv); err != nil {
			slog.Warn("Reverting a command's app-side change failed", "id", r.ID, "error", err)
		}
	}
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

func acceptResult(current, next Status) bool {
	if next == "" {
		return false
	}
	if current == StatusSaved {
		return false
	}
	if next == StatusApplied {
		return true
	}
	if current.Final() {
		return false
	}
	if current == StatusPending && next == StatusQueued {
		return false
	}
	return true
}

func (s *Service) Redeliver(ctx context.Context) int {
	waiting, err := s.Log.Waiting(ctx)
	if err != nil {
		slog.Warn("Listing waiting scheduler commands failed", "error", err)
		return 0
	}
	n := 0
	for _, r := range waiting {
		if r.Destination != DestinationObservatory || r.Status != StatusQueued {
			continue
		}
		if _, err := s.deliver(ctx, r); err == nil {
			n++
		}
	}
	return n
}
