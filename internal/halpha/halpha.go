package halpha

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/klauspost/compress/zstd"
	"gorm.io/gorm"
)

const (
	DefaultHiPS2FITS = "https://alasky.cds.unistra.fr/hips-image-services/hips2fits"
	SurveyID         = "CDS/P/Finkbeiner"
	SourceText       = "Finkbeiner 2003 H-α all-sky map via CDS hips2fits (CDS/P/Finkbeiner)"
	UserAgent        = "astro-stacker/1 (+https://github.com/USA-RedDragon/astro-stacker)"

	StateReady   = "ready"
	StateLoading = "loading"
	StateFailed  = "failed"
	StateOff     = "off"

	mapWidth     = 2880
	mapHeight    = 1440
	maxBody      = 64 << 20
	fetchTimeout = 5 * time.Minute
	firstRetry   = time.Hour
	maxRetry     = 24 * time.Hour
	asinhScale   = 6500.0
	maxSamples   = 40000

	ScoreFloorR = 2.0
	ScoreFullR  = 500.0
)

type Map struct {
	W, H           int
	CRPix1, CRPix2 float64
	CDelt1, CDelt2 float64
	CRVal1, CRVal2 float64
	Data           []float32
	FetchedAt      time.Time
}

type Sample struct {
	Rayleigh  float64 `json:"rayleigh"`
	Peak      float64 `json:"peak"`
	RadiusDeg float64 `json:"radiusDeg"`
}

func (m *Map) pixel(ra, dec float64) (int, int, bool) {
	dra := math.Mod(ra-m.CRVal1+540, 360) - 180
	x := m.CRPix1 + dra/m.CDelt1
	y := m.CRPix2 + (dec-m.CRVal2)/m.CDelt2
	i, j := int(math.Round(x))-1, m.H-int(math.Round(y))
	if j < 0 || j >= m.H {
		return 0, 0, false
	}
	i = ((i % m.W) + m.W) % m.W
	return i, j, true
}

func (m *Map) value(i, j int) (float64, bool) {
	v := float64(m.Data[j*m.W+i])
	return v, !math.IsNaN(v) && v >= 0
}

func (m *Map) Sample(ra, dec, radiusDeg float64) (Sample, bool) {
	if m == nil || m.W == 0 {
		return Sample{}, false
	}
	step := math.Abs(m.CDelt2)
	radiusDeg = math.Max(radiusDeg, step/2)
	ci, cj, ok := m.pixel(ra, dec)
	if !ok {
		return Sample{}, false
	}
	nj := int(math.Ceil(radiusDeg / step))
	cosDec := math.Max(math.Cos(dec*math.Pi/180), 1e-3)
	ni := min(m.W/2, int(math.Ceil(radiusDeg/(math.Abs(m.CDelt1)*cosDec))))
	var sum, peak float64
	n := 0
	for dj := -nj; dj <= nj; dj++ {
		j := cj + dj
		if j < 0 || j >= m.H {
			continue
		}
		for di := -ni; di <= ni; di++ {
			if n >= maxSamples {
				break
			}
			i := ((ci+di)%m.W + m.W) % m.W
			pd := float64(dj) * step
			pr := float64(di) * math.Abs(m.CDelt1) * cosDec
			if math.Hypot(pd, pr) > radiusDeg && (di != 0 || dj != 0) {
				continue
			}
			v, ok := m.value(i, j)
			if !ok {
				continue
			}
			sum += v
			peak = math.Max(peak, v)
			n++
		}
	}
	if n == 0 {
		return Sample{}, false
	}
	round := func(v float64) float64 { return math.Round(v*10) / 10 }
	return Sample{Rayleigh: round(sum / float64(n)), Peak: round(peak), RadiusDeg: math.Round(radiusDeg*1000) / 1000}, true
}

func Score(s *Sample) float64 {
	if s == nil || s.Rayleigh <= ScoreFloorR {
		return 0
	}
	return math.Min(1, math.Log10(s.Rayleigh/ScoreFloorR)/math.Log10(ScoreFullR/ScoreFloorR))
}

func encode(data []float32) ([]byte, error) {
	raw := make([]byte, 2*len(data))
	for i, v := range data {
		q := 0.0
		if !math.IsNaN(float64(v)) && v > 0 {
			q = math.Min(65535, math.Round(math.Asinh(float64(v))*asinhScale))
		}
		binary.LittleEndian.PutUint16(raw[2*i:], uint16(q))
	}
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBetterCompression))
	if err != nil {
		return nil, err
	}
	defer enc.Close()
	return enc.EncodeAll(raw, nil), nil
}

func decode(b []byte, n int) ([]float32, error) {
	dec, err := zstd.NewReader(nil)
	if err != nil {
		return nil, err
	}
	defer dec.Close()
	raw, err := dec.DecodeAll(b, nil)
	if err != nil {
		return nil, err
	}
	if len(raw) != 2*n {
		return nil, fmt.Errorf("halpha map: %d bytes, want %d", len(raw), 2*n)
	}
	out := make([]float32, n)
	for i := range out {
		out[i] = float32(math.Sinh(float64(binary.LittleEndian.Uint16(raw[2*i:])) / asinhScale))
	}
	return out, nil
}

func FromFITS(b []byte) (*Map, error) {
	kw, err := frameheader.Parse(b)
	if err != nil {
		return nil, err
	}
	im, err := imagedata.Decode(b)
	if err != nil {
		return nil, err
	}
	m := &Map{W: im.W, H: im.H, CRPix1: kw.Float("CRPIX1"), CRPix2: kw.Float("CRPIX2"), CDelt1: kw.Float("CDELT1"), CDelt2: kw.Float("CDELT2"),
		CRVal1: kw.Float("CRVAL1"), CRVal2: kw.Float("CRVAL2"), Data: im.Plane(0)}
	for _, v := range []float64{m.CRPix1, m.CRPix2, m.CDelt1, m.CDelt2, m.CRVal1, m.CRVal2} {
		if math.IsNaN(v) {
			return nil, errors.New("halpha map: no CAR projection in the header")
		}
	}
	if m.CDelt1 == 0 || m.CDelt2 == 0 {
		return nil, errors.New("halpha map: zero pixel size")
	}
	return m, nil
}

func Fetch(ctx context.Context, client *http.Client, endpoint string) (*Map, error) {
	if client == nil {
		client = &http.Client{Timeout: fetchTimeout}
	}
	if endpoint == "" {
		endpoint = DefaultHiPS2FITS
	}
	q := url.Values{}
	q.Set("hips", SurveyID)
	q.Set("width", strconv.Itoa(mapWidth))
	q.Set("height", strconv.Itoa(mapHeight))
	q.Set("projection", "CAR")
	q.Set("fov", "360")
	q.Set("ra", "180")
	q.Set("dec", "0")
	q.Set("coordsys", "icrs")
	q.Set("format", "fits")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hips2fits: %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, err
	}
	return FromFITS(b)
}

func save(ctx context.Context, db *gorm.DB, m *Map) error {
	body, err := encode(m.Data)
	if err != nil {
		return err
	}
	row := app.HAlphaMap{Source: SurveyID, Width: m.W, Height: m.H, CRPix1: m.CRPix1, CRPix2: m.CRPix2, CDelt1: m.CDelt1, CDelt2: m.CDelt2,
		CRVal1: m.CRVal1, CRVal2: m.CRVal2, Data: body, FetchedAt: m.FetchedAt}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("source = ?", SurveyID).Delete(&app.HAlphaMap{}).Error; err != nil {
			return err
		}
		return tx.Create(&row).Error
	})
}

func load(ctx context.Context, db *gorm.DB) (*Map, error) {
	var row app.HAlphaMap
	err := db.WithContext(ctx).Where("source = ?", SurveyID).Order("id DESC").First(&row).Error
	if err != nil {
		return nil, err
	}
	data, err := decode(row.Data, row.Width*row.Height)
	if err != nil {
		return nil, err
	}
	return &Map{W: row.Width, H: row.Height, CRPix1: row.CRPix1, CRPix2: row.CRPix2, CDelt1: row.CDelt1, CDelt2: row.CDelt2,
		CRVal1: row.CRVal1, CRVal2: row.CRVal2, Data: data, FetchedAt: row.FetchedAt}, nil
}

type Status struct {
	State     string     `json:"state"`
	Source    string     `json:"source"`
	FetchedAt *time.Time `json:"fetchedAt"`
	Error     *string    `json:"error"`
}

type Provider struct {
	DB       *gorm.DB
	Client   *http.Client
	Endpoint string
	Off      bool

	mu    sync.Mutex
	m     *Map
	state string
	err   string
}

func (p *Provider) Get() (*Map, Status) {
	p.mu.Lock()
	defer p.mu.Unlock()
	st := Status{State: p.state, Source: SourceText}
	switch {
	case p.Off:
		st.State = StateOff
	case st.State == "":
		st.State = StateLoading
	}
	if p.m != nil {
		at := p.m.FetchedAt
		st.FetchedAt = &at
	}
	if p.err != "" {
		e := p.err
		st.Error = &e
	}
	return p.m, st
}

func (p *Provider) set(m *Map, state, errText string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if m != nil {
		p.m = m
	}
	p.state, p.err = state, errText
}

func (p *Provider) Run(ctx context.Context) {
	if p.Off {
		return
	}
	p.set(nil, StateLoading, "")
	wait := firstRetry
	for {
		if p.loadOnce(ctx) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(2*wait, maxRetry)
	}
}

func (p *Provider) loadOnce(ctx context.Context) bool {
	if p.DB != nil {
		m, err := load(ctx, p.DB)
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			slog.Warn("Could not read the cached H-α map", "error", err)
		}
		if err == nil {
			p.set(m, StateReady, "")
			return true
		}
	}
	m, err := Fetch(ctx, p.Client, p.Endpoint)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("Could not fetch the H-α map; the Finder shows H-α as unknown until it loads", "error", err)
			p.set(nil, StateFailed, err.Error())
		}
		return false
	}
	m.FetchedAt = time.Now().UTC()
	if p.DB != nil {
		if err := save(ctx, p.DB, m); err != nil {
			slog.Warn("Could not cache the H-α map", "error", err)
		}
	}
	p.set(m, StateReady, "")
	slog.Info("H-α map loaded", "source", SurveyID, "size", fmt.Sprintf("%dx%d", m.W, m.H))
	return true
}
