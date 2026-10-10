package goalmeasure

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	xpCatalogue = "I/360/syntphot"
	xpBrightR   = 8.0
	xpFaintR    = 15.0
)

type XPFetcher func(ctx context.Context, ra, dec, radius float64) ([]goals.XPStar, error)

func VizierXPFetcher(client *http.Client, endpoint string) XPFetcher {
	if client == nil {
		client = &http.Client{Timeout: gaiaTimeout}
	}
	if endpoint == "" {
		endpoint = VizierURL
	}
	return func(ctx context.Context, ra, dec, radius float64) ([]goals.XPStar, error) {
		q := url.Values{}
		q.Set("-source", xpCatalogue)
		q.Set("-c", fmt.Sprintf("%.6f %+.6f", ra, dec))
		q.Set("-c.rd", fmt.Sprintf("%.4f", radius))
		q.Set("-out", "RA_ICRS,DE_ICRS,FB,FV,FR,FI")
		q.Set("Rmag", fmt.Sprintf("%g..%g", xpBrightR, xpFaintR))
		q.Set("-out.max", strconv.Itoa(gaiaMaxRows))
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
			return nil, fmt.Errorf("vizier: %s", resp.Status)
		}
		return ParseXPTSV(resp.Body)
	}
}

func ParseXPTSV(r io.Reader) ([]goals.XPStar, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	cols := map[string]int{}
	inData := false
	var out []goals.XPStar
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		switch {
		case len(cols) == 0:
			for i, f := range fields {
				cols[strings.TrimSpace(f)] = i
			}
			for _, need := range []string{"RA_ICRS", "DE_ICRS", "FB", "FV", "FR", "FI"} {
				if _, ok := cols[need]; !ok {
					return nil, fmt.Errorf("vizier: no %s column", need)
				}
			}
		case !inData:
			if strings.HasPrefix(strings.TrimSpace(fields[0]), "-") {
				inData = true
			}
		default:
			get := func(name string) float64 {
				i := cols[name]
				if i >= len(fields) {
					return math.NaN()
				}
				v, err := strconv.ParseFloat(strings.TrimSpace(fields[i]), 64)
				if err != nil {
					return math.NaN()
				}
				return v
			}
			s := goals.XPStar{RA: get("RA_ICRS"), Dec: get("DE_ICRS"), FB: get("FB"), FV: get("FV"), FR: get("FR"), FI: get("FI")}
			if !math.IsNaN(s.RA) && !math.IsNaN(s.Dec) {
				out = append(out, s)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(cols) == 0 {
		return nil, errors.New("vizier: no table in the response")
	}
	return out, nil
}

type storedXP struct {
	RA  float64  `json:"ra"`
	Dec float64  `json:"dec"`
	FB  *float64 `json:"b"`
	FV  *float64 `json:"v"`
	FR  *float64 `json:"r"`
	FI  *float64 `json:"i"`
}

func finitePtr(v float64) *float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return &v
}

func orNaN(p *float64) float64 {
	if p == nil {
		return math.NaN()
	}
	return *p
}

func encodeXP(stars []goals.XPStar) (string, error) {
	rows := make([]storedXP, len(stars))
	for i, s := range stars {
		rows[i] = storedXP{RA: s.RA, Dec: s.Dec, FB: finitePtr(s.FB), FV: finitePtr(s.FV), FR: finitePtr(s.FR), FI: finitePtr(s.FI)}
	}
	b, err := json.Marshal(rows)
	return string(b), err
}

func decodeXP(s string) ([]goals.XPStar, error) {
	var rows []storedXP
	if err := json.Unmarshal([]byte(s), &rows); err != nil {
		return nil, err
	}
	out := make([]goals.XPStar, len(rows))
	for i, r := range rows {
		out[i] = goals.XPStar{RA: r.RA, Dec: r.Dec, FB: orNaN(r.FB), FV: orNaN(r.FV), FR: orNaN(r.FR), FI: orNaN(r.FI)}
	}
	return out, nil
}

func xpStars(ctx context.Context, db *gorm.DB, fetch XPFetcher, object string, ra, dec, radius float64) ([]goals.XPStar, error) {
	var cached app.XPField
	err := db.WithContext(ctx).Where(columnObject+" = ?", object).First(&cached).Error
	switch {
	case err == nil:
		if angularDistance(ra, dec, cached.RA, cached.Dec)+radius <= cached.Radius {
			if stars, err := decodeXP(cached.Stars); err == nil {
				return stars, nil
			}
		}
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return nil, err
	}
	if fetch == nil {
		return nil, errors.New("no Gaia XP catalogue")
	}
	radius *= gaiaFieldSlack
	stars, err := fetch(ctx, ra, dec, radius)
	if err != nil {
		return nil, err
	}
	body, err := encodeXP(stars)
	if err != nil {
		return nil, err
	}
	row := app.XPField{Object: object, RA: ra, Dec: dec, Radius: radius, Stars: body, FetchedAt: time.Now().UTC()}
	if err := db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: columnObject}},
		DoUpdates: clause.AssignmentColumns([]string{"ra", "dec", "radius", "stars", "fetched_at"}),
	}).Create(&row).Error; err != nil {
		return nil, err
	}
	return stars, nil
}
