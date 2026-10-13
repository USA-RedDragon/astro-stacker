package collabswitch

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/events"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"gorm.io/gorm"
)

const (
	Author        = "stacker: collab switch"
	PollInterval  = time.Minute
	RetryInterval = 15 * time.Minute
	EventType     = "collab"
	valueOn       = "1"
	valueOff      = "0"

	reasonRestart = "NINA restarted, which sets the collab constant back to 0; the stored setting was sent again."
	reasonReload  = "The sequence was loaded again, which sets the collab constant back to its saved value; the stored setting was sent again."
	reasonDiffer  = "NINA's collab constant did not match the stored setting; the stored setting was sent again."
)

var (
	ErrUnconfigured = errors.New("the NINA Advanced API is not configured (scheduler.nina-url)")
	errNoDB         = errors.New("the stacker database is not connected")
)

type Commands interface {
	Submit(ctx context.Context, kind schedcmd.Kind, payload json.RawMessage, author string) (schedcmd.Record, error)
}

type RecordLog interface {
	Update(ctx context.Context, id string, fn func(*schedcmd.Record) error) (schedcmd.Record, error)
}

type Service struct {
	NINA     *NINA
	DB       *gorm.DB
	Commands Commands
	Log      RecordLog
	Notify   func(schedcmd.Record)
	Events   *events.Broker
	Now      func() time.Time

	op    sync.Mutex
	mu    sync.RWMutex
	state memo
}

type memo struct {
	read       *Read
	readAt     *time.Time
	readErr    string
	started    string
	reachErr   string
	checkedAt  *time.Time
	running    []string
	print      string
	fresh      bool
	lastSendAt *time.Time
	lastSend   string
	readFailAt *time.Time
	noSequence bool
}

type Desired struct {
	On    bool      `json:"on"`
	SetBy string    `json:"setBy"`
	SetAt time.Time `json:"setAt"`
}

type ReadBack struct {
	Present bool       `json:"present"`
	Value   *string    `json:"value"`
	At      *time.Time `json:"at"`
	Error   string     `json:"error,omitempty"`
}

type Status struct {
	Configured  bool       `json:"configured"`
	Desired     *Desired   `json:"desired"`
	ReadBack    ReadBack   `json:"readBack"`
	Matches     bool       `json:"matches"`
	Reachable   bool       `json:"reachable"`
	NINAError   string     `json:"ninaError,omitempty"`
	NINAStarted string     `json:"ninaStarted,omitempty"`
	CheckedAt   *time.Time `json:"checkedAt"`
	Running     []string   `json:"running"`
	LastSendAt  *time.Time `json:"lastSendAt"`
	LastSend    string     `json:"lastSend,omitempty"`
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func value(on bool) string {
	if on {
		return valueOn
	}
	return valueOff
}

func (s *Service) desired(ctx context.Context) (*Desired, error) {
	if s.DB == nil {
		return nil, errNoDB
	}
	row, ok, err := schedcmd.LoadCollabSwitch(ctx, s.DB)
	if err != nil || !ok {
		return nil, err
	}
	return &Desired{On: row.Enabled, SetBy: row.SetBy, SetAt: row.SetAt.UTC()}, nil
}

func (s *Service) Status(ctx context.Context) (Status, error) {
	st := Status{Configured: s.NINA.Configured(), Running: []string{}}
	d, err := s.desired(ctx)
	if err != nil {
		return st, err
	}
	st.Desired = d
	s.mu.RLock()
	m := s.state
	s.mu.RUnlock()
	st.Reachable = m.checkedAt != nil && m.reachErr == ""
	st.NINAError, st.NINAStarted, st.CheckedAt = m.reachErr, m.started, m.checkedAt
	if m.running != nil {
		st.Running = m.running
	}
	st.ReadBack = ReadBack{At: m.readAt, Error: m.readErr}
	if m.read != nil {
		st.ReadBack.Present = m.read.Present
		if m.read.Present {
			v := m.read.Value
			st.ReadBack.Value = &v
		}
	}
	st.Matches = d != nil && st.ReadBack.Value != nil && *st.ReadBack.Value == value(d.On)
	st.LastSendAt, st.LastSend = m.lastSendAt, m.lastSend
	return st, nil
}

func (s *Service) publish() {
	if s.Events != nil {
		s.Events.Publish(events.Event{Type: EventType})
	}
}

func (s *Service) readState(ctx context.Context) (Read, error) {
	r, err := s.NINA.State(ctx)
	at := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.state.read, s.state.readAt, s.state.readErr = nil, nil, err.Error()
		s.state.readFailAt, s.state.noSequence = &at, errors.Is(err, ErrNoSequence)
		return r, err
	}
	s.state.read, s.state.readAt, s.state.readErr = &r, &at, ""
	s.state.readFailAt, s.state.noSequence = nil, false
	s.state.running = r.Running
	return r, nil
}

func (s *Service) Read(ctx context.Context) (Status, error) {
	if !s.NINA.Configured() {
		return s.Status(ctx)
	}
	s.op.Lock()
	_, err := s.readState(ctx)
	s.op.Unlock()
	s.publish()
	st, serr := s.Status(ctx)
	if serr != nil {
		return st, serr
	}
	return st, err
}

type outcome struct {
	applied bool
	message string
}

func (s *Service) send(ctx context.Context, on bool, known *Read) outcome {
	var r Read
	if known != nil {
		r = *known
	} else {
		var err error
		if r, err = s.readState(ctx); err != nil {
			return outcome{message: "NINA was not read: " + err.Error() + ". The setting is kept and sent again when NINA answers."}
		}
	}
	if !r.Present {
		return outcome{message: "The running sequence has no Powerups constant named collab, so nothing changed in NINA. The setting is kept and sent once a sequence with it runs."}
	}
	if err := s.NINA.Edit(ctx, r.Path, value(on)); err != nil {
		return outcome{message: "NINA refused the change: " + err.Error() + ". The setting is kept and sent again later."}
	}
	back, err := s.readState(ctx)
	switch {
	case err != nil:
		return outcome{message: "Sent to NINA, but the read-back failed: " + err.Error()}
	case !back.Present:
		return outcome{message: "Sent to NINA, but the collab constant was gone on the read-back."}
	case back.Value != value(on):
		return outcome{message: "Sent to NINA, but it reads back collab = " + back.Value + "."}
	}
	return outcome{applied: true, message: "NINA reads back collab = " + back.Value + "."}
}

func (s *Service) finish(ctx context.Context, rec schedcmd.Record, o outcome) schedcmd.Record {
	if s.Log == nil || rec.ID == "" {
		return rec
	}
	at := s.now()
	r, err := s.Log.Update(ctx, rec.ID, func(r *schedcmd.Record) error {
		r.Message = o.message
		if o.applied {
			r.Status, r.AppliedAt = schedcmd.StatusApplied, &at
		} else {
			r.Status, r.AppliedAt = schedcmd.StatusFailed, nil
		}
		return nil
	})
	if err != nil {
		slog.Warn("Could not record the collab switch outcome", "id", rec.ID, "error", err)
		return rec
	}
	if s.Notify != nil {
		s.Notify(r)
	}
	return r
}

func (s *Service) replan(ctx context.Context, author string) {
	if s.Commands == nil {
		return
	}
	if _, err := s.Commands.Submit(ctx, schedcmd.KindReplan, json.RawMessage(`{}`), author); err != nil {
		slog.Warn("Could not ask the scheduler to re-plan after the collab switch", "error", err)
	}
}

func (s *Service) Set(ctx context.Context, on bool, author string) (Status, schedcmd.Record, error) {
	if s.Commands == nil {
		return Status{}, schedcmd.Record{}, ErrUnconfigured
	}
	s.op.Lock()
	defer s.op.Unlock()
	raw, err := json.Marshal(schedcmd.CollabSwitchPayload{On: on})
	if err != nil {
		return Status{}, schedcmd.Record{}, err
	}
	rec, err := s.Commands.Submit(ctx, schedcmd.KindCollabSwitch, raw, author)
	if err != nil {
		return Status{}, rec, err
	}
	ctx = context.WithoutCancel(ctx)
	o := outcome{message: ErrUnconfigured.Error() + ", so nothing changed in NINA. The setting is kept."}
	if s.NINA.Configured() {
		o = s.send(ctx, on, nil)
	}
	s.sent(o)
	rec = s.finish(ctx, rec, o)
	if o.applied && on {
		s.replan(ctx, author)
	}
	s.publish()
	st, err := s.Status(ctx)
	return st, rec, err
}

func (s *Service) sent(o outcome) {
	at := s.now()
	s.mu.Lock()
	s.state.lastSendAt, s.state.lastSend = &at, o.message
	s.mu.Unlock()
}

func (s *Service) Run(ctx context.Context) {
	if !s.NINA.Configured() {
		return
	}
	t := time.NewTicker(PollInterval)
	defer t.Stop()
	for {
		s.Check(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Service) snapshot() memo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

func (m memo) sig() string {
	t := func(v *time.Time) string {
		if v == nil {
			return ""
		}
		return v.Format(time.RFC3339Nano)
	}
	return strings.Join([]string{m.started, m.reachErr, m.readErr, t(m.readAt), t(m.lastSendAt), strings.Join(m.running, ">")}, "|")
}

func (s *Service) Check(ctx context.Context) {
	if !s.NINA.Configured() {
		return
	}
	s.op.Lock()
	defer s.op.Unlock()
	d, err := s.desired(ctx)
	if err != nil {
		slog.Warn("Could not load the collab switch setting", "error", err)
		return
	}
	before := s.snapshot().sig()
	defer func() {
		if s.snapshot().sig() != before {
			s.publish()
		}
	}()
	reason, changed := s.poll(ctx)
	if d == nil {
		return
	}
	m := s.snapshot()
	if m.reachErr != "" {
		return
	}
	now := s.now()
	due := func(t *time.Time) bool { return t == nil || now.Sub(*t) >= RetryInterval }
	switch {
	case changed:
	case m.read == nil:
		if !m.noSequence && !due(m.readFailAt) {
			return
		}
		reason = reasonDiffer
	case m.fresh && due(m.readAt):
		reason = reasonReload
	case m.read.Present && m.read.Value != value(d.On) && due(m.readAt):
		reason = reasonDiffer
	default:
		return
	}
	r, err := s.readState(ctx)
	if err != nil || !r.Present || r.Value == value(d.On) {
		return
	}
	s.resend(ctx, *d, reason, &r)
}

func (s *Service) poll(ctx context.Context) (string, bool) {
	started, err := s.NINA.Started(ctx)
	at := s.now()
	var o Outline
	if err == nil {
		o, err = s.NINA.Outline(ctx)
		if errors.Is(err, ErrNoSequence) {
			err = nil
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	prev := s.state
	s.state.checkedAt = &at
	if err != nil {
		s.state.reachErr = err.Error()
		return "", false
	}
	s.state.reachErr, s.state.started, s.state.running = "", started, o.Running
	s.state.print, s.state.fresh = o.Fingerprint, o.Fresh
	switch {
	case prev.started != "" && prev.started != started:
		return reasonRestart, true
	case prev.print != "" && (prev.print != o.Fingerprint || (!prev.fresh && o.Fresh)):
		return reasonReload, true
	}
	return "", false
}

func (s *Service) resend(ctx context.Context, d Desired, reason string, r *Read) {
	raw, err := json.Marshal(schedcmd.CollabSwitchPayload{On: d.On, Resend: reason})
	if err != nil {
		return
	}
	var rec schedcmd.Record
	if s.Commands != nil {
		if rec, err = s.Commands.Submit(ctx, schedcmd.KindCollabSwitch, raw, Author); err != nil {
			slog.Warn("Could not record the collab switch re-send", "error", err)
		}
	}
	o := s.send(ctx, d.On, r)
	s.sent(o)
	s.finish(ctx, rec, o)
	slog.Info("Sent the collab switch to NINA again", "on", d.On, "reason", reason, "applied", o.applied, "result", o.message)
	if o.applied && d.On {
		s.replan(ctx, Author)
	}
	s.publish()
}
