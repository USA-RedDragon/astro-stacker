//go:build vettingdev

package ownimage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/starcat"
	"github.com/minio/minio-go/v7"
	miniocreds "github.com/minio/minio-go/v7/pkg/credentials"
)

type devData struct {
	host   string
	mc     *minio.Client
	gaia   *starcat.Gaia
	keys   map[string]string
	lon    float64
	loads  int
	bytes  int64
	loadMu sync.Mutex
	load   time.Duration
}

func home() string { h, _ := os.UserHomeDir(); return h }

func newDev(t *testing.T) *devData {
	t.Helper()
	hb, err := os.ReadFile(filepath.Join(home(), "work/vetting/host"))
	if err != nil {
		t.Fatal(err)
	}
	cb, err := os.ReadFile(filepath.Join(home(), ".mcli/config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Aliases map[string]struct {
			URL       string `json:"url"`
			AccessKey string `json:"accessKey"`
			SecretKey string `json:"secretKey"`
		} `json:"aliases"`
	}
	if err := json.Unmarshal(cb, &cfg); err != nil {
		t.Fatal(err)
	}
	a := cfg.Aliases["home"]
	u, err := url.Parse(a.URL)
	if err != nil {
		t.Fatal(err)
	}
	mc, err := minio.New(u.Host, &minio.Options{Creds: miniocreds.NewStaticV4(a.AccessKey, a.SecretKey, ""), Secure: u.Scheme == "https", Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	return &devData{host: strings.TrimSpace(string(hb)), mc: mc, gaia: starcat.NewGaia(starcat.MinioBucket{Client: mc, Name: "sky-survey"}, "catalogs/gaia-dr3/v1")}
}

func (d *devData) api(ctx context.Context, p string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+d.host+p, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", p, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

func (d *devData) object(ctx context.Context, key string, n int64) ([]byte, error) {
	opts := minio.GetObjectOptions{}
	if n > 0 {
		if err := opts.SetRange(0, n-1); err != nil {
			return nil, err
		}
	}
	o, err := d.mc.GetObject(ctx, "astro-processed", key, opts)
	if err != nil {
		return nil, err
	}
	defer o.Close()
	return io.ReadAll(o)
}

func (d *devData) header(ctx context.Context, key string) (frameheader.Keywords, error) {
	b, err := d.object(ctx, key, 65536)
	if err != nil {
		return nil, err
	}
	return frameheader.Parse(b)
}

func (d *devData) Subs(ctx context.Context, object string) ([]Sub, error) {
	d.keys = map[string]string{}
	for o := range d.mc.ListObjects(ctx, "astro-processed", minio.ListObjectsOptions{Prefix: "registered/" + object + "/", Recursive: true}) {
		if o.Err != nil {
			return nil, o.Err
		}
		d.keys[path.Base(o.Key)] = o.Key
	}
	var rows []struct {
		File     string     `json:"file"`
		Filter   string     `json:"filter"`
		Exposure *float64   `json:"exposure"`
		DateObs  *time.Time `json:"date_obs"`
		Status   string     `json:"status"`
		Weight   *float64   `json:"weight"`
		Done     *time.Time `json:"processed_at"`
	}
	if err := d.api(ctx, "/api/v1/subs?object="+url.QueryEscape(object), &rows); err != nil {
		return nil, err
	}
	var out []Sub
	for i, r := range rows {
		key, ok := d.keys[r.File]
		if r.Status != "added" || !ok || r.Weight == nil || !(*r.Weight > 0) || r.DateObs == nil || r.Exposure == nil {
			continue
		}
		if math.IsNaN(d.lon) || d.lon == 0 {
			k, err := d.header(ctx, key)
			if err != nil {
				return nil, err
			}
			d.lon = frameheader.FromKeywords(k).Longitude
		}
		f := frameheader.Frame{HasDate: true, DateObs: r.DateObs.UTC(), Longitude: d.lon}
		night, _ := f.Night()
		s := Sub{FrameID: 1000000 + i, Filter: r.Filter, Night: night, Start: r.DateObs.UTC(), Exposure: *r.Exposure, Weight: *r.Weight, Key: key}
		if r.Done != nil {
			s.AddedAt = r.Done.UTC()
		}
		out = append(out, s)
	}
	return out, nil
}

func (d *devData) Load(ctx context.Context, key string) (*discoveries.Plane, error) {
	start := time.Now()
	b, err := d.object(ctx, key, 0)
	if err != nil {
		return nil, err
	}
	im, err := imagedata.Decode(b)
	if err != nil {
		return nil, err
	}
	d.loadMu.Lock()
	d.loads++
	d.bytes += int64(len(b))
	d.load += time.Since(start)
	d.loadMu.Unlock()
	return discoveries.PlaneOf(im), nil
}

func (d *devData) Get(ctx context.Context, key string) ([]byte, error) {
	if p, ok := strings.CutPrefix(key, "api:"); ok {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+d.host+p, nil)
		if err != nil {
			return nil, err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("GET %s: %s", p, resp.Status)
		}
		return io.ReadAll(resp.Body)
	}
	return d.object(ctx, key, 0)
}

func (d *devData) Frame(ctx context.Context, object string) (*discoveries.WCS, int, int, error) {
	for _, f := range []string{"Luminance", "H-a", "Red", "S-II", "O-III", "Green", "Blue"} {
		k, err := d.header(ctx, "stacks/"+object+"/"+f+"/master.fit")
		if err != nil {
			continue
		}
		w, h := int(k.Float("NAXIS1")), int(k.Float("NAXIS2"))
		wcs, err := discoveries.ParseWCS(k, h)
		if err != nil {
			return nil, 0, 0, err
		}
		return wcs, w, h, nil
	}
	return nil, 0, 0, fmt.Errorf("no master for %s", object)
}

func (d *devData) Stars(ctx context.Context, ra, dec, radius float64) ([]Star, error) {
	rows, err := d.gaia.Cone(ctx, ra, dec, radius, nil)
	if err != nil {
		return nil, err
	}
	return starsOf(rows, julianYear(time.Now())), nil
}

type apiCand struct {
	ID       int                      `json:"id"`
	RunID    int                      `json:"run_id"`
	Night    string                   `json:"night"`
	Group    string                   `json:"group"`
	X        float64                  `json:"x"`
	Y        float64                  `json:"y"`
	Sign     int                      `json:"sign"`
	DeltaMag *float64                 `json:"delta_mag"`
	SNR      float64                  `json:"snr"`
	Present  int                      `json:"present_in"`
	Tested   int                      `json:"subs_tested"`
	Light    []discoveries.LightPoint `json:"light"`
	Run      struct {
		Finished time.Time `json:"finished_at"`
		Seconds  float64   `json:"seconds"`
	} `json:"run"`
}

func (d *devData) sourceSpot(ctx context.Context, id int) (Spot, error) {
	var src struct {
		ID     int     `json:"id"`
		Object string  `json:"object"`
		Kind   string  `json:"kind"`
		RA     float64 `json:"ra"`
		Dec    float64 `json:"dec"`
		Best   struct {
			GaiaG *float64 `json:"gaia_g"`
		} `json:"best"`
		Nights []struct {
			Candidates []struct {
				ID int `json:"id"`
			} `json:"candidates"`
		} `json:"nights"`
	}
	if err := d.api(ctx, fmt.Sprintf("/api/v1/discoveries/sources/%d", id), &src); err != nil {
		return Spot{}, err
	}
	s := Spot{SourceID: src.ID, Object: src.Object, Kind: src.Kind, RA: src.RA, Dec: src.Dec, GaiaG: src.Best.GaiaG}
	for _, n := range src.Nights {
		for _, c := range n.Candidates {
			var cd apiCand
			if err := d.api(ctx, fmt.Sprintf("/api/v1/discoveries/candidates/%d", c.ID), &cd); err != nil {
				return Spot{}, err
			}
			night, _ := time.Parse(dateLayout, cd.Night)
			s.Nights = append(s.Nights, Night{Night: night, Group: cd.Group, CandidateID: cd.ID, RunID: cd.RunID, X: cd.X, Y: cd.Y, Sign: cd.Sign,
				DeltaMag: cd.DeltaMag, SNR: cd.SNR, PresentIn: cd.Present, SubsTested: cd.Tested,
				CutoutKey: fmt.Sprintf("api:/api/v1/discoveries/candidates/%d/cutout/", cd.ID),
				RunAt:     cd.Run.Finished.Add(-time.Duration(cd.Run.Seconds * float64(time.Second)))})
		}
	}
	return s, nil
}

func cygnusSpot(ctx context.Context, t *testing.T, d *devData) Spot {
	t.Helper()
	g := 18.06
	s := Spot{SourceID: -1, Object: "Cygnis Loop Panel 2", Kind: "change", RA: 314.32090, Dec: 28.71857, GaiaG: &g}
	w, _, _, err := d.Frame(ctx, s.Object)
	if err != nil {
		t.Fatal(err)
	}
	x, y, _ := w.PixelOf(s.RA, s.Dec)
	runAt := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	for i, dm := range []float64{0.58, 0.67, 0.41} {
		night := time.Date(2026, 10, 5+i, 0, 0, 0, 0, time.UTC)
		for _, grp := range []string{"S-II", "H-a"} {
			v := dm
			s.Nights = append(s.Nights, Night{Night: night, Group: grp, X: x, Y: y, Sign: -1, DeltaMag: &v, RunAt: runAt})
		}
	}
	return s
}

func maxRSS() int64 {
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	return ru.Maxrss
}

func TestDevMeasure(t *testing.T) {
	t.Parallel()
	devCase := os.Getenv("OWNIMAGE_CASE")
	ctx := context.Background()
	d := newDev(t)
	var s Spot
	var err error
	switch devCase {
	case "andromeda":
		s, err = d.sourceSpot(ctx, 236)
	case "ic1396":
		s, err = d.sourceSpot(ctx, 3754)
	case "cygnus":
		s = cygnusSpot(ctx, t, d)
	default:
		t.Skip("set OWNIMAGE_CASE to andromeda, cygnus or ic1396")
	}
	if err != nil {
		t.Fatal(err)
	}
	if near, err := d.Stars(ctx, s.RA, s.Dec, 10.0/3600); err == nil {
		for _, n := range near {
			t.Logf("gaia %d G=%.2f BP-RP=%.2f sep=%.1f\" variable=%v", n.SourceID, n.G, n.BPRP, discoveries.Separation(s.RA, s.Dec, n.RA, n.Dec)*3600, n.Variable)
		}
	}
	start := time.Now()
	res, err := Measure(ctx, d, s.Object, []Spot{s}, Options{Threads: 6})
	if err != nil {
		t.Fatal(err)
	}
	el := time.Since(start)
	r := res[0]
	for _, c := range r.Checks {
		t.Logf("%-10s %-4s %s", c.ID, c.Status, c.Evidence)
	}
	t.Logf("flags %+v", r.Flags)
	if c := r.Curve; c != nil {
		t.Logf("curve comps=%d dropped=%d points=%d scatter=%.4f change=%s %.4f sigma=%.1f quiet=%.4f", c.Comps, c.Dropped, len(c.Points), c.Scatter, c.ChangeNight, c.Change, c.ChangeSigma, c.Quiet)
		for _, n := range c.Nights {
			t.Logf("  %s %-9s n=%3d %+.4f +- %.4f", n.Night, n.Filter, n.N, n.DMag, n.Err)
		}
	}
	t.Logf("subs read %d, %.1f MB, load %.1fs summed, wall %.1fs, %.2fs per sub, max RSS %d MB", d.loads, float64(d.bytes)/1e6, d.load.Seconds(), el.Seconds(), el.Seconds()/float64(max(d.loads, 1)), maxRSS()/1024)
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetIndent("", " ")
	if err := enc.Encode(r); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(home(), "work/vetting/ownimage", devCase+".json"), b.Bytes(), 0o600)
}
