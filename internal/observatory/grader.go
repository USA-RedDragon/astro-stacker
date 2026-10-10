package observatory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"
)

const (
	pathGrader     = "/os/v1/grader"
	GraderFreshFor = time.Minute

	GraderOK           = "ok"
	GraderUnsupported  = "unsupported"
	GraderUnreachable  = "unreachable"
	GraderUnconfigured = "unconfigured"
	GraderError        = "error"

	GraderFromPlugin    = "plugin"
	GraderFromLastKnown = "last_known"
)

var ErrGraderUnsupported = errors.New("grader limit not reported by this plugin version")

type GraderHFRSettings struct {
	ProjectGrading        bool     `json:"project_grading"`
	Enabled               bool     `json:"enabled"`
	SigmaFactor           float64  `json:"sigma_factor"`
	AutoAcceptLevel       *float64 `json:"auto_accept_level,omitempty"`
	AcceptImprovement     bool     `json:"accept_improvement"`
	MaxSampleSize         int      `json:"max_sample_size"`
	DelayThresholdPercent float64  `json:"delay_threshold_percent"`
	Mode                  string   `json:"mode"`
}

type GraderPlanLimit struct {
	PlanID            int        `json:"plan_id"`
	Filter            string     `json:"filter"`
	ExposureSeconds   float64    `json:"exposure_seconds"`
	State             string     `json:"state"`
	Acquired          int        `json:"acquired"`
	Matching          int        `json:"matching"`
	Samples           int        `json:"samples"`
	ReferenceImageID  *int       `json:"reference_image_id,omitempty"`
	ReferenceAt       *time.Time `json:"reference_at,omitempty"`
	ReferenceExposure *float64   `json:"reference_exposure,omitempty"`
	Gain              *int       `json:"gain,omitempty"`
	Offset            *int       `json:"offset,omitempty"`
	Binning           string     `json:"binning,omitempty"`
	ROI               *float64   `json:"roi,omitempty"`
	Mean              *float64   `json:"mean,omitempty"`
	SD                *float64   `json:"sd,omitempty"`
	Lower             *float64   `json:"lower,omitempty"`
	Upper             *float64   `json:"upper,omitempty"`
	RejectAbove       *float64   `json:"reject_above,omitempty"`
	RejectBelow       *float64   `json:"reject_below,omitempty"`
	PopulationRule    string     `json:"population_rule,omitempty"`
}

type GraderReport struct {
	GeneratedAt *time.Time         `json:"generated_at,omitempty"`
	ProjectID   int                `json:"project_id"`
	TargetID    int                `json:"target_id"`
	TargetName  string             `json:"target_name"`
	HFR         *GraderHFRSettings `json:"hfr,omitempty"`
	Plans       []GraderPlanLimit  `json:"plans"`
}

type GraderTarget struct {
	TargetID  int           `json:"target_id"`
	Source    string        `json:"source"`
	FetchedAt time.Time     `json:"fetched_at"`
	Error     string        `json:"error,omitempty"`
	Report    *GraderReport `json:"report"`
}

type GraderSummary struct {
	State   string         `json:"state"`
	Note    string         `json:"note,omitempty"`
	Version string         `json:"version,omitempty"`
	Targets []GraderTarget `json:"targets"`
}

type notFoundError struct {
	message string
}

func (e *notFoundError) Error() string {
	if e.message == "" {
		return ErrNotFound.Error()
	}
	return ErrNotFound.Error() + ": " + e.message
}

func (e *notFoundError) Is(target error) bool { return target == ErrNotFound }

func notFound(body []byte) error {
	var b struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &b)
	return &notFoundError{message: b.Error}
}

func notFoundRoute(err error) bool {
	var nf *notFoundError
	return errors.As(err, &nf) && nf.message == "not found"
}

func (c *Client) Grader(ctx context.Context, targetID int) (GraderReport, error) {
	var r GraderReport
	err := c.do(ctx, http.MethodGet, pathGrader+"?target_id="+strconv.Itoa(targetID), nil, &r)
	if errors.Is(err, ErrNotFound) && notFoundRoute(err) {
		return r, ErrGraderUnsupported
	}
	return r, err
}

type GraderCache struct {
	Client  *Client
	Monitor *Monitor
	Now     func() time.Time

	mu          sync.Mutex
	known       map[int]GraderTarget
	unsupported time.Time
}

func (g *GraderCache) now() time.Time {
	if g.Now != nil {
		return g.Now().UTC()
	}
	return time.Now().UTC()
}

func (g *GraderCache) Limits(ctx context.Context, targetIDs []int) GraderSummary {
	out := GraderSummary{State: GraderOK, Targets: []GraderTarget{}}
	if g == nil || !g.Client.Configured() {
		out.State = GraderUnconfigured
		out.Note = "the scheduler API is not configured"
		return out
	}
	if g.Monitor != nil {
		v := g.Monitor.View()
		if v.Status != nil {
			out.Version = v.Version
		}
		if v.Reachable != Online {
			return g.lastKnown(out, targetIDs, "plugin unreachable")
		}
	}
	now := g.now()
	g.mu.Lock()
	unsupportedFresh := !g.unsupported.IsZero() && now.Sub(g.unsupported) < GraderFreshFor
	g.mu.Unlock()
	if unsupportedFresh {
		out.State = GraderUnsupported
		out.Note = ErrGraderUnsupported.Error()
		return out
	}
	ids := unique(targetIDs)
	results := make([]GraderTarget, len(ids))
	errs := make([]error, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		if t, ok := g.fresh(id, now); ok {
			results[i] = t
			continue
		}
		wg.Go(func() {
			r, err := g.Client.Grader(ctx, id)
			if err != nil {
				errs[i] = err
				return
			}
			t := GraderTarget{TargetID: id, Source: GraderFromPlugin, FetchedAt: now, Report: &r}
			g.remember(t)
			results[i] = t
		})
	}
	wg.Wait()
	var firstErr error
	for i, id := range ids {
		err := errs[i]
		if err == nil {
			out.Targets = append(out.Targets, results[i])
			continue
		}
		if errors.Is(err, ErrGraderUnsupported) {
			g.mu.Lock()
			g.unsupported = now
			g.mu.Unlock()
			out.State = GraderUnsupported
			out.Note = err.Error()
			out.Targets = []GraderTarget{}
			return out
		}
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if firstErr == nil {
			firstErr = err
		}
		if t, ok := g.known1(id); ok {
			t.Source = GraderFromLastKnown
			t.Error = err.Error()
			out.Targets = append(out.Targets, t)
		}
	}
	if firstErr != nil {
		out.State = GraderError
		out.Note = firstErr.Error()
	}
	return out
}

func (g *GraderCache) lastKnown(out GraderSummary, targetIDs []int, why string) GraderSummary {
	out.State = GraderUnreachable
	out.Note = why
	for _, id := range unique(targetIDs) {
		if t, ok := g.known1(id); ok {
			t.Source = GraderFromLastKnown
			t.Error = why
			out.Targets = append(out.Targets, t)
		}
	}
	return out
}

func (g *GraderCache) fresh(id int, now time.Time) (GraderTarget, bool) {
	t, ok := g.known1(id)
	if !ok || now.Sub(t.FetchedAt) >= GraderFreshFor {
		return GraderTarget{}, false
	}
	return t, true
}

func (g *GraderCache) known1(id int) (GraderTarget, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	t, ok := g.known[id]
	return t, ok
}

func (g *GraderCache) remember(t GraderTarget) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.known == nil {
		g.known = map[int]GraderTarget{}
	}
	g.known[t.TargetID] = t
}

func unique(ids []int) []int {
	seen := map[int]bool{}
	out := []int{}
	for _, id := range ids {
		if id > 0 && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Ints(out)
	return out
}
