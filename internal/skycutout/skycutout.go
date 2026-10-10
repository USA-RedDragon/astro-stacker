package skycutout

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"golang.org/x/sync/singleflight"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	DefaultHiPS2FITS = "https://alasky.cds.unistra.fr/hips-image-services/hips2fits"
	Survey           = "CDS/P/DSS2/color"
	UserAgent        = "astro-stacker/1 (+https://github.com/USA-RedDragon/astro-stacker)"
	ContentType      = "image/jpeg"

	MinFOV        = 0.01
	MaxFOV        = 30.0
	MinSize       = 64
	MaxSize       = 1024
	DefaultWidth  = 600
	DefaultHeight = 400

	defaultPerMinute  = 20
	defaultMaxEntries = 2000
	fetchTimeout      = 60 * time.Second
	maxBody           = 8 << 20
	touchEvery        = time.Hour
)

var (
	ErrBadRequest  = errors.New("bad cutout request")
	ErrRateLimited = errors.New("too many new survey cutouts this minute; try again shortly")
	ErrUpstream    = errors.New("the survey service did not return a cutout")
	ErrOff         = errors.New("survey cutouts are switched off")
)

type Params struct {
	RA, Dec, FOV, Rotation float64
	Width, Height          int
}

func Parse(ra, dec, fov, rotation, width, height string) (Params, error) {
	num := func(name, s string, def float64, required bool) (float64, error) {
		if s == "" {
			if required {
				return 0, fmt.Errorf("%w: %s is required", ErrBadRequest, name)
			}
			return def, nil
		}
		v, err := strconv.ParseFloat(s, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return 0, fmt.Errorf("%w: %s is not a number", ErrBadRequest, name)
		}
		return v, nil
	}
	var p Params
	var err error
	if p.RA, err = num("ra", ra, 0, true); err != nil {
		return p, err
	}
	if p.Dec, err = num("dec", dec, 0, true); err != nil {
		return p, err
	}
	if p.FOV, err = num("fov", fov, 0, true); err != nil {
		return p, err
	}
	if p.Rotation, err = num("rotation", rotation, 0, false); err != nil {
		return p, err
	}
	w, err := num("width", width, DefaultWidth, false)
	if err != nil {
		return p, err
	}
	h, err := num("height", height, DefaultHeight, false)
	if err != nil {
		return p, err
	}
	if p.Dec < -90 || p.Dec > 90 {
		return p, fmt.Errorf("%w: dec must be within ±90°", ErrBadRequest)
	}
	if p.FOV < MinFOV || p.FOV > MaxFOV {
		return p, fmt.Errorf("%w: fov must be %g–%g°", ErrBadRequest, MinFOV, MaxFOV)
	}
	clampSize := func(v float64) int { return int(math.Max(MinSize, math.Min(MaxSize, math.Round(v)))) }
	p.Width, p.Height = clampSize(w), clampSize(h)
	round := func(v, step float64) float64 { return math.Round(v/step) * step }
	p.RA = round(math.Mod(math.Mod(p.RA, 360)+360, 360), 0.001)
	if p.RA >= 360 {
		p.RA = 0
	}
	p.Dec = round(p.Dec, 0.001)
	p.FOV = round(p.FOV, 0.001)
	p.Rotation = round(math.Mod(math.Mod(p.Rotation, 360)+360, 360), 0.1)
	return p, nil
}

func (p Params) Key() string {
	return fmt.Sprintf("%s|%.3f|%.3f|%.3f|%.1f|%dx%d", Survey, p.RA, p.Dec, p.FOV, p.Rotation, p.Width, p.Height)
}

func (p Params) query() url.Values {
	q := url.Values{}
	q.Set("hips", Survey)
	q.Set("width", strconv.Itoa(p.Width))
	q.Set("height", strconv.Itoa(p.Height))
	q.Set("fov", strconv.FormatFloat(p.FOV, 'f', 3, 64))
	q.Set("projection", "TAN")
	q.Set("coordsys", "icrs")
	q.Set("ra", strconv.FormatFloat(p.RA, 'f', 3, 64))
	q.Set("dec", strconv.FormatFloat(p.Dec, 'f', 3, 64))
	q.Set("rotation_angle", strconv.FormatFloat(-p.Rotation, 'f', 1, 64))
	q.Set("format", "jpg")
	return q
}

type Cutout struct {
	Data []byte
	ETag string
}

type Service struct {
	DB         *gorm.DB
	Client     *http.Client
	Endpoint   string
	PerMinute  int
	MaxEntries int
	Off        bool
	Now        func() time.Time

	mu     sync.Mutex
	recent []time.Time
	group  singleflight.Group
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) allow() (bool, time.Duration) {
	limit := s.PerMinute
	if limit <= 0 {
		limit = defaultPerMinute
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.recent[:0]
	for _, t := range s.recent {
		if now.Sub(t) < time.Minute {
			kept = append(kept, t)
		}
	}
	s.recent = kept
	if len(s.recent) >= limit {
		return false, time.Minute - now.Sub(s.recent[0])
	}
	s.recent = append(s.recent, now)
	return true, 0
}

type RateLimitError struct{ RetryAfter time.Duration }

func (e *RateLimitError) Error() string { return ErrRateLimited.Error() }

func (e *RateLimitError) Unwrap() error { return ErrRateLimited }

func etag(b []byte) string {
	sum := sha256.Sum256(b)
	return `"` + hex.EncodeToString(sum[:12]) + `"`
}

func (s *Service) cached(ctx context.Context, key string) (*app.SkyCutout, error) {
	if s.DB == nil {
		return nil, gorm.ErrRecordNotFound
	}
	var rows []app.SkyCutout
	if err := s.DB.WithContext(ctx).Where(&app.SkyCutout{Key: key}).Limit(1).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	row := rows[0]
	if s.now().Sub(row.UsedAt) > touchEvery {
		_ = s.DB.WithContext(ctx).Model(&app.SkyCutout{}).Where("id = ?", row.ID).Update("used_at", s.now()).Error
	}
	return &row, nil
}

func (s *Service) Get(ctx context.Context, p Params) (Cutout, error) {
	if s.Off {
		return Cutout{}, ErrOff
	}
	key := p.Key()
	row, err := s.cached(ctx, key)
	if err == nil {
		return Cutout{Data: row.Data, ETag: row.ETag}, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return Cutout{}, err
	}
	v, err, _ := s.group.Do(key, func() (any, error) {
		if row, err := s.cached(ctx, key); err == nil {
			return Cutout{Data: row.Data, ETag: row.ETag}, nil
		}
		if ok, wait := s.allow(); !ok {
			return Cutout{}, &RateLimitError{RetryAfter: wait}
		}
		data, err := s.fetch(ctx, p)
		if err != nil {
			return Cutout{}, err
		}
		c := Cutout{Data: data, ETag: etag(data)}
		s.store(ctx, key, c)
		return c, nil
	})
	if err != nil {
		return Cutout{}, err
	}
	c, ok := v.(Cutout)
	if !ok {
		return Cutout{}, ErrUpstream
	}
	return c, nil
}

func (s *Service) fetch(ctx context.Context, p Params) ([]byte, error) {
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: fetchTimeout}
	}
	endpoint := s.Endpoint
	if endpoint == "" {
		endpoint = DefaultHiPS2FITS
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+p.query().Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUpstream, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, &RateLimitError{RetryAfter: time.Minute}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: %s", ErrUpstream, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUpstream, err)
	}
	if !bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}) {
		return nil, fmt.Errorf("%w: not a JPEG", ErrUpstream)
	}
	return b, nil
}

func (s *Service) store(ctx context.Context, key string, c Cutout) {
	if s.DB == nil {
		return
	}
	now := s.now()
	row := app.SkyCutout{Key: key, ContentType: ContentType, ETag: c.ETag, Data: c.Data, FetchedAt: now, UsedAt: now}
	db := s.DB.WithContext(ctx)
	if err := db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoNothing: true}).Create(&row).Error; err != nil {
		return
	}
	limit := s.MaxEntries
	if limit <= 0 {
		limit = defaultMaxEntries
	}
	var n int64
	if err := db.Model(&app.SkyCutout{}).Count(&n).Error; err != nil || n <= int64(limit) {
		return
	}
	var old []int
	if err := db.Model(&app.SkyCutout{}).Order("used_at").Limit(int(n)-limit).Pluck("id", &old).Error; err == nil && len(old) > 0 {
		_ = db.Where("id IN ?", old).Delete(&app.SkyCutout{}).Error
	}
}
