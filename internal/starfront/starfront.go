package starfront

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	UserAgent      = "astro-stacker-collabs/1 (+https://github.com/USA-RedDragon/astro-stacker; read-only)"
	StatusOpen     = "open"
	keyProjects    = "projects"
	keySky         = "sky"
	keyProject     = "project:"
	recentClosed   = 30 * 24 * time.Hour
	requestSpacing = 2 * time.Second
	maxBody        = 8 << 20
)

type Region struct {
	RA       float64 `json:"ra"`
	Dec      float64 `json:"dec"`
	Width    float64 `json:"width"`
	Height   float64 `json:"height"`
	Rotation float64 `json:"rotation"`
}

type Requirements struct {
	MinFocalLength      *float64            `json:"minFocalLength"`
	MaxFocalLength      *float64            `json:"maxFocalLength"`
	MinScale            *float64            `json:"minScale"`
	MaxScale            *float64            `json:"maxScale"`
	AcceptColour        *bool               `json:"acceptColour"`
	ColourMaxMoon       *float64            `json:"colourMaxMoon"`
	MaxHFR              *float64            `json:"maxHfr"`
	MaxGuideRMS         *float64            `json:"maxGuideRms"`
	MinExposure         *float64            `json:"minExposure"`
	MaxExposure         *float64            `json:"maxExposure"`
	Filters             map[string]*float64 `json:"filters"`
	MaxMoonIllumination *float64            `json:"maxMoonIllumination"`
	MinMoonSeparation   *float64            `json:"minMoonSeparation"`
	MinAltitude         *float64            `json:"minAltitude"`
	RequireCalibrated   bool                `json:"requireCalibrated"`
	MinFramesPerVisit   *int                `json:"minFramesPerVisit"`
}

type Payload struct {
	Region       Region             `json:"region"`
	Kind         string             `json:"kind"`
	Requirements Requirements       `json:"requirements"`
	Goals        map[string]float64 `json:"goals"`
	Notes        string             `json:"notes"`
}

type Project struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Coordinator string  `json:"coordinator"`
	Created     float64 `json:"created"`
	Status      string  `json:"status"`
	Payload     Payload `json:"payload"`
}

func (p Project) CreatedAt() time.Time {
	sec := int64(p.Created)
	return time.Unix(sec, int64((p.Created-float64(sec))*1e9)).UTC()
}

type task struct {
	Agent string `json:"agent"`
	State string `json:"state"`
}

type contribution struct {
	Agent    string  `json:"agent"`
	Night    string  `json:"night"`
	Filter   string  `json:"filter"`
	Seconds  float64 `json:"seconds"`
	Accepted bool    `json:"accepted"`
}

type detail struct {
	Project       Project        `json:"project"`
	Tasks         []task         `json:"tasks"`
	Contributions []contribution `json:"contributions"`
}

type Summary struct {
	Joined        int                `json:"joined"`
	Declined      int                `json:"declined"`
	Reporters     int                `json:"reporters"`
	Contributions int                `json:"contributions"`
	Hours         map[string]float64 `json:"hours"`
	FirstNight    string             `json:"firstNight,omitempty"`
	LastNight     string             `json:"lastNight,omitempty"`
}

func summarise(d detail) Summary {
	s := Summary{Hours: map[string]float64{}}
	joined, declined, reporters := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, t := range d.Tasks {
		switch t.State {
		case "declined":
			declined[t.Agent] = true
		default:
			joined[t.Agent] = true
		}
	}
	for _, c := range d.Contributions {
		reporters[c.Agent] = true
		s.Contributions++
		if c.Accepted {
			s.Hours[c.Filter] += c.Seconds / 3600
		}
		if c.Night != "" && (s.FirstNight == "" || c.Night < s.FirstNight) {
			s.FirstNight = c.Night
		}
		if c.Night > s.LastNight {
			s.LastNight = c.Night
		}
	}
	for a := range declined {
		if joined[a] {
			delete(declined, a)
		}
	}
	s.Joined, s.Declined, s.Reporters = len(joined), len(declined), len(reporters)
	return s
}

func summariseBody(body []byte) ([]byte, error) {
	var d detail
	if err := json.Unmarshal(body, &d); err != nil {
		return nil, err
	}
	return json.Marshal(summarise(d))
}

func summariseSky(body []byte) ([]byte, error) {
	var s struct {
		Telescopes []json.RawMessage `json:"telescopes"`
		Online     int               `json:"online"`
		Imaging    int               `json:"imaging"`
	}
	if err := json.Unmarshal(body, &s); err != nil {
		return nil, err
	}
	return json.Marshal(SkySummary{Telescopes: len(s.Telescopes), Online: s.Online, Imaging: s.Imaging})
}

type SkySummary struct {
	Telescopes int `json:"telescopes"`
	Online     int `json:"online"`
	Imaging    int `json:"imaging"`
}

type State struct {
	Projects  []Project          `json:"projects"`
	Summaries map[string]Summary `json:"summaries"`
	Sky       *SkySummary        `json:"sky,omitempty"`
	FetchedAt *time.Time         `json:"fetchedAt,omitempty"`
	Error     string             `json:"error,omitempty"`
	Enabled   bool               `json:"enabled"`
}

type Poller struct {
	BaseURL  string
	Interval time.Duration
	Client   *http.Client
	DB       *gorm.DB
	Now      func() time.Time
	Pause    func(time.Duration)

	mu    sync.RWMutex
	state State
}

func (p *Poller) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *Poller) State() State {
	p.mu.RLock()
	defer p.mu.RUnlock()
	st := p.state
	st.Enabled = true
	return st
}

var errStatus = errors.New("unexpected status")

func (p *Poller) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(p.BaseURL, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "application/json")
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: GET %s: %s", errStatus, path, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxBody))
}

func (p *Poller) pause() {
	if p.Pause != nil {
		p.Pause(requestSpacing)
		return
	}
	time.Sleep(requestSpacing)
}

func (p *Poller) store(ctx context.Context, key string, body []byte) {
	if p.DB == nil {
		return
	}
	row := app.StarfrontCache{Key: key, Body: string(body), FetchedAt: p.now().UTC()}
	err := p.DB.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"body", "fetched_at"}),
	}).Create(&row).Error
	if err != nil {
		slog.Warn("Could not cache Starfront response", "key", key, "error", err)
	}
}

func (p *Poller) cached(ctx context.Context) map[string]app.StarfrontCache {
	out := map[string]app.StarfrontCache{}
	if p.DB == nil {
		return out
	}
	var rows []app.StarfrontCache
	if err := p.DB.WithContext(ctx).Find(&rows).Error; err != nil {
		return out
	}
	for _, r := range rows {
		out[r.Key] = r
	}
	return out
}

func (p *Poller) Load(ctx context.Context) {
	cache := p.cached(ctx)
	pr, ok := cache[keyProjects]
	if !ok {
		return
	}
	st, err := build(cache)
	if err != nil {
		slog.Warn("Could not read cached Starfront projects", "error", err)
		return
	}
	at := pr.FetchedAt
	st.FetchedAt = &at
	p.mu.Lock()
	p.state = st
	p.mu.Unlock()
}

func build(cache map[string]app.StarfrontCache) (State, error) {
	var list struct {
		Projects []Project `json:"projects"`
	}
	if err := json.Unmarshal([]byte(cache[keyProjects].Body), &list); err != nil {
		return State{}, err
	}
	st := State{Projects: list.Projects, Summaries: map[string]Summary{}}
	for _, pr := range list.Projects {
		row, ok := cache[keyProject+pr.ID]
		if !ok {
			continue
		}
		var sum Summary
		if err := json.Unmarshal([]byte(row.Body), &sum); err != nil {
			continue
		}
		st.Summaries[pr.ID] = sum
	}
	if row, ok := cache[keySky]; ok {
		var sky SkySummary
		if err := json.Unmarshal([]byte(row.Body), &sky); err == nil {
			st.Sky = &sky
		}
	}
	return st, nil
}

func (p *Poller) wanted(pr Project, cache map[string]app.StarfrontCache) bool {
	if pr.Status == StatusOpen {
		return true
	}
	if p.now().Sub(pr.CreatedAt()) > recentClosed {
		return false
	}
	_, have := cache[keyProject+pr.ID]
	return !have
}

func (p *Poller) Once(ctx context.Context) error {
	body, err := p.get(ctx, "/api/v1/projects")
	if err != nil {
		p.fail(err)
		return err
	}
	var list struct {
		Projects []Project `json:"projects"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		p.fail(err)
		return err
	}
	trimmed, err := json.Marshal(list)
	if err != nil {
		p.fail(err)
		return err
	}
	p.store(ctx, keyProjects, trimmed)
	cache := p.cached(ctx)
	cache[keyProjects] = app.StarfrontCache{Key: keyProjects, Body: string(trimmed)}
	for _, pr := range list.Projects {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !p.wanted(pr, cache) {
			continue
		}
		p.pause()
		d, err := p.get(ctx, "/api/v1/projects/"+pr.ID)
		if err != nil {
			slog.Warn("Could not fetch Starfront project", "project", pr.ID, "error", err)
			continue
		}
		sum, err := summariseBody(d)
		if err != nil {
			slog.Warn("Could not read Starfront project", "project", pr.ID, "error", err)
			continue
		}
		p.store(ctx, keyProject+pr.ID, sum)
		cache[keyProject+pr.ID] = app.StarfrontCache{Key: keyProject + pr.ID, Body: string(sum)}
	}
	p.pause()
	if body, err := p.get(ctx, "/api/v1/sky"); err == nil {
		if sum, err := summariseSky(body); err == nil {
			p.store(ctx, keySky, sum)
			cache[keySky] = app.StarfrontCache{Key: keySky, Body: string(sum)}
		}
	}
	st, err := build(cache)
	if err != nil {
		p.fail(err)
		return err
	}
	at := p.now().UTC()
	st.FetchedAt = &at
	p.mu.Lock()
	p.state = st
	p.mu.Unlock()
	return nil
}

func (p *Poller) fail(err error) {
	p.mu.Lock()
	p.state.Error = err.Error()
	p.mu.Unlock()
}

func (p *Poller) Run(ctx context.Context) {
	p.Load(ctx)
	interval := p.Interval
	if interval < 5*time.Minute {
		interval = 30 * time.Minute
	}
	st := p.State()
	wait := time.Duration(0)
	if st.FetchedAt != nil {
		if age := p.now().Sub(*st.FetchedAt); age < interval {
			wait = interval - age
		}
	}
	backoff := interval
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if err := p.Once(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("Fetching Starfront collaborations failed", "error", err)
			backoff = min(backoff*2, 6*time.Hour)
			wait = backoff
			continue
		}
		backoff = interval
		wait = interval
	}
}
