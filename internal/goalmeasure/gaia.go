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
	VizierURL      = "https://vizier.cds.unistra.fr/viz-bin/asu-tsv"
	gaiaCatalogue  = "I/355/gaiadr3"
	gaiaMaxRows    = 20000
	gaiaTimeout    = 90 * time.Second
	gaiaFieldSlack = 1.05
)

type StarFetcher func(ctx context.Context, ra, dec, radius float64) ([]goals.CatalogStar, error)

func VizierFetcher(client *http.Client, endpoint string) StarFetcher {
	if client == nil {
		client = &http.Client{Timeout: gaiaTimeout}
	}
	if endpoint == "" {
		endpoint = VizierURL
	}
	return func(ctx context.Context, ra, dec, radius float64) ([]goals.CatalogStar, error) {
		q := url.Values{}
		q.Set("-source", gaiaCatalogue)
		q.Set("-c", fmt.Sprintf("%.6f %+.6f", ra, dec))
		q.Set("-c.rd", fmt.Sprintf("%.4f", radius))
		q.Set("-out", "RA_ICRS,DE_ICRS,Gmag")
		q.Set("Gmag", fmt.Sprintf("%g..%g", goals.CatalogBrightG, goals.CatalogFaintG))
		q.Set("-out.max", strconv.Itoa(gaiaMaxRows))
		q.Set("-sort", "Gmag")
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("vizier: %s", resp.Status)
		}
		return ParseVizierTSV(resp.Body)
	}
}

func ParseVizierTSV(r io.Reader) ([]goals.CatalogStar, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	cols := map[string]int{}
	inData := false
	var out []goals.CatalogStar
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
			for _, need := range []string{"RA_ICRS", "DE_ICRS", "Gmag"} {
				if _, ok := cols[need]; !ok {
					return nil, fmt.Errorf("vizier: no %s column", need)
				}
			}
		case !inData:
			if strings.HasPrefix(strings.TrimSpace(fields[0]), "-") {
				inData = true
			}
		default:
			get := func(name string) (float64, bool) {
				i := cols[name]
				if i >= len(fields) {
					return 0, false
				}
				v, err := strconv.ParseFloat(strings.TrimSpace(fields[i]), 64)
				return v, err == nil
			}
			ra, ok1 := get("RA_ICRS")
			dec, ok2 := get("DE_ICRS")
			g, ok3 := get("Gmag")
			if ok1 && ok2 && ok3 {
				out = append(out, goals.CatalogStar{RA: ra, Dec: dec, G: g})
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

func angularDistance(ra1, dec1, ra2, dec2 float64) float64 {
	const d = math.Pi / 180
	c := math.Sin(dec1*d)*math.Sin(dec2*d) + math.Cos(dec1*d)*math.Cos(dec2*d)*math.Cos((ra1-ra2)*d)
	return math.Acos(math.Max(-1, math.Min(1, c))) / d
}

func catalogStars(ctx context.Context, db *gorm.DB, fetch StarFetcher, object string, ra, dec, radius float64) ([]goals.CatalogStar, error) {
	var cached app.GaiaField
	err := db.WithContext(ctx).Where(columnObject+" = ?", object).First(&cached).Error
	switch {
	case err == nil:
		if angularDistance(ra, dec, cached.RA, cached.Dec)+radius <= cached.Radius {
			var stars []goals.CatalogStar
			if err := json.Unmarshal([]byte(cached.Stars), &stars); err == nil {
				return stars, nil
			}
		}
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return nil, err
	}
	if fetch == nil {
		return nil, errors.New("no star catalogue")
	}
	radius *= gaiaFieldSlack
	stars, err := fetch(ctx, ra, dec, radius)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(stars)
	if err != nil {
		return nil, err
	}
	row := app.GaiaField{Object: object, RA: ra, Dec: dec, Radius: radius, Stars: string(b), FetchedAt: time.Now().UTC()}
	if err := db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: columnObject}},
		DoUpdates: clause.AssignmentColumns([]string{"ra", "dec", "radius", "stars", "fetched_at"}),
	}).Create(&row).Error; err != nil {
		return nil, err
	}
	return stars, nil
}
