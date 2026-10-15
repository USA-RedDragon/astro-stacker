package surveys

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

const (
	// ATLASDilutionRadius is how close, in arcsec, a Gaia neighbour must
	// be to blend into ATLAS photometry; the ATLAS PSF is 4 to 5 arcsec.
	ATLASDilutionRadius = 5.0
	// ATLASMJDMin is the first MJD asked of ATLAS.
	ATLASMJDMin = 57000.0
	// ATLASRetryFailed is how long a failed ATLAS task is left before the
	// star is queued again.
	ATLASRetryFailed = 7 * 24 * time.Hour
	// DefaultATLASGap is the least time between two requests to ATLAS.
	DefaultATLASGap = 5 * time.Second
	maxATLASResult  = 64 << 20
	maxATLASJSON    = 4 << 20
	atlasMinRel     = 0.05
	fluxToMagErr    = 1.0857
)

// ATLASTemplateMJD are the dates ATLAS changed its reference templates;
// difference fluxes are put on one level separately in each span.
func ATLASTemplateMJD() []float64 { return []float64{58417, 58882} }

// ATLASTask is what the cache keeps of a queued ATLAS task.
type ATLASTask struct {
	URL      string    `json:"task_url"`
	Queued   time.Time `json:"queued_at"`
	RA       float64   `json:"ra"`
	Dec      float64   `json:"dec"`
	Error    string    `json:"error,omitempty"`
	FailedAt time.Time `json:"failed_at"`
}

type atlasTaskStatus struct {
	URL       string  `json:"url"`
	Finish    *string `json:"finishtimestamp"`
	Start     *string `json:"starttimestamp"`
	ResultURL *string `json:"result_url"`
	ErrorMsg  *string `json:"error_msg"`
	QueuePos  *int    `json:"queuepos"`
}

func (f *Fetcher) atlasBase() string {
	if f.ATLASURL != "" {
		return strings.TrimRight(f.ATLASURL, "/")
	}
	return DefaultATLASURL
}

func (f *Fetcher) atlasKey(field Field, t Target) string {
	return f.dir(field, "atlas") + starKey(t)
}

func (f *Fetcher) atlas(ctx context.Context, field Field, t Target) Fetch {
	if f.ATLASToken == "" {
		return failed(lc.ATLAS, "no ATLAS token")
	}
	base := f.atlasKey(field, t)
	raw, err := f.getZ(ctx, base+".txt.zst")
	if err == nil {
		return atlasFetch(t, raw)
	}
	if !isNotFound(err) {
		return failed(lc.ATLAS, "could not read the ATLAS cache: %v", err)
	}
	tj, err := f.getZ(ctx, base+".task.json.zst")
	if err != nil && !isNotFound(err) {
		return failed(lc.ATLAS, "could not read the ATLAS task: %v", err)
	}
	if err == nil {
		var task ATLASTask
		if err := json.Unmarshal(tj, &task); err != nil {
			return failed(lc.ATLAS, "damaged ATLAS task record: %v", err)
		}
		if task.Error == "" {
			return f.atlasPoll(ctx, field, t, task)
		}
		if f.now().Sub(task.FailedAt) < ATLASRetryFailed {
			return failed(lc.ATLAS, "the ATLAS task failed: %s", task.Error)
		}
	}
	return f.atlasQueue(ctx, field, t)
}

func (f *Fetcher) atlasWait(ctx context.Context) error {
	gap := f.atlasGap
	if gap == 0 {
		gap = DefaultATLASGap
	}
	f.mu.Lock()
	wait := gap - f.now().Sub(f.lastATLAS)
	f.mu.Unlock()
	if gap > 0 && wait > 0 {
		if err := sleepCtx(ctx, wait); err != nil {
			return err
		}
	}
	f.mu.Lock()
	f.lastATLAS = f.now()
	f.mu.Unlock()
	return nil
}

func (f *Fetcher) atlasRequest(ctx context.Context, method, u string, form url.Values, out any) error {
	return f.do(ctx, func() (*http.Request, error) {
		if err := f.atlasWait(ctx); err != nil {
			return nil, err
		}
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, err := http.NewRequestWithContext(ctx, method, u, body)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Token "+f.ATLASToken)
		req.Header.Set("Accept", "application/json")
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		return req, nil
	}, func(r *http.Response) error {
		if out == nil {
			return nil
		}
		b, err := io.ReadAll(io.LimitReader(r.Body, maxATLASJSON))
		if err != nil {
			return err
		}
		return json.Unmarshal(b, out)
	})
}

func (f *Fetcher) maxQueued() int {
	if f.ATLASMaxQueued > 0 {
		return f.ATLASMaxQueued
	}
	return DefaultATLASMaxQueued
}

func (f *Fetcher) atlasQueue(ctx context.Context, field Field, t Target) Fetch {
	var qp struct {
		Positions map[string]int `json:"queuepositions"`
	}
	if err := f.atlasRequest(ctx, http.MethodGet, f.atlasBase()+"/queuepositions.json", nil, &qp); err != nil {
		return f.atlasError("could not read the ATLAS queue", err)
	}
	if len(qp.Positions) >= f.maxQueued() {
		return Fetch{Survey: lc.ATLAS, State: Pending, Note: fmt.Sprintf("waiting for room: %d of our ATLAS tasks are queued", len(qp.Positions))}
	}
	form := url.Values{}
	form.Set("ra", strconv.FormatFloat(t.RA, 'f', 7, 64))
	form.Set("dec", strconv.FormatFloat(t.Dec, 'f', 7, 64))
	form.Set("radec_epoch_year", "2016.0")
	form.Set("propermotion_ra", strconv.FormatFloat(t.PMRA, 'f', 4, 64))
	form.Set("propermotion_dec", strconv.FormatFloat(t.PMDec, 'f', 4, 64))
	form.Set("mjd_min", strconv.FormatFloat(ATLASMJDMin, 'f', 1, 64))
	form.Set("use_reduced", "false")
	form.Set("send_email", "false")
	form.Set("comment", "astro-stacker vetting "+starKey(t))
	var st atlasTaskStatus
	if err := f.atlasRequest(ctx, http.MethodPost, f.atlasBase()+"/queue/", form, &st); err != nil {
		return f.atlasError("could not queue the ATLAS task", err)
	}
	if st.URL == "" {
		return failed(lc.ATLAS, "ATLAS queued the task but gave no task URL")
	}
	task := ATLASTask{URL: st.URL, Queued: f.now().UTC(), RA: t.RA, Dec: t.Dec}
	if err := f.putTask(ctx, field, t, task); err != nil {
		return failed(lc.ATLAS, "queued ATLAS task %s but could not record it: %v", st.URL, err)
	}
	return Fetch{Survey: lc.ATLAS, State: Pending, Note: "queued at ATLAS"}
}

func (f *Fetcher) atlasError(what string, err error) Fetch {
	var he *httpError
	if errors.As(err, &he) && he.Status == http.StatusTooManyRequests {
		return Fetch{Survey: lc.ATLAS, State: Pending, Note: "ATLAS is throttling us; asked again on a later run"}
	}
	return failed(lc.ATLAS, "%s: %v", what, err)
}

func (f *Fetcher) putTask(ctx context.Context, field Field, t Target, task ATLASTask) error {
	raw, err := json.Marshal(task)
	if err != nil {
		return err
	}
	return f.putZ(ctx, f.atlasKey(field, t)+".task.json.zst", raw)
}

func (f *Fetcher) atlasPoll(ctx context.Context, field Field, t Target, task ATLASTask) Fetch {
	var st atlasTaskStatus
	if err := f.atlasRequest(ctx, http.MethodGet, task.URL, nil, &st); err != nil {
		var he *httpError
		if errors.As(err, &he) && he.Status == http.StatusNotFound {
			task.Error, task.FailedAt = "ATLAS no longer has the task", f.now().UTC()
			_ = f.putTask(ctx, field, t, task)
			return failed(lc.ATLAS, "ATLAS lost the task; queued again after %s", ATLASRetryFailed)
		}
		return f.atlasError("could not read the ATLAS task", err)
	}
	if st.Finish == nil || *st.Finish == "" {
		note := "waiting in the ATLAS queue"
		if st.Start != nil && *st.Start != "" {
			note = "ATLAS is running the task"
		} else if st.QueuePos != nil {
			note = fmt.Sprintf("waiting in the ATLAS queue at place %d", *st.QueuePos)
		}
		return Fetch{Survey: lc.ATLAS, State: Pending, Note: note}
	}
	if (st.ErrorMsg != nil && *st.ErrorMsg != "") || st.ResultURL == nil || *st.ResultURL == "" {
		msg := "no result"
		if st.ErrorMsg != nil && *st.ErrorMsg != "" {
			msg = *st.ErrorMsg
		}
		task.Error, task.FailedAt = msg, f.now().UTC()
		if err := f.putTask(ctx, field, t, task); err != nil {
			return failed(lc.ATLAS, "the ATLAS task failed (%s) and could not be recorded: %v", msg, err)
		}
		_ = f.atlasRequest(ctx, http.MethodDelete, task.URL, nil, nil)
		return failed(lc.ATLAS, "the ATLAS task failed: %s", msg)
	}
	var text []byte
	err := f.do(ctx, func() (*http.Request, error) {
		if err := f.atlasWait(ctx); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, *st.ResultURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Token "+f.ATLASToken)
		return req, nil
	}, func(r *http.Response) error {
		b, err := io.ReadAll(io.LimitReader(r.Body, maxATLASResult))
		text = b
		return err
	})
	if err != nil {
		return f.atlasError("could not download the ATLAS result", err)
	}
	if _, err := parseATLAS(text); err != nil {
		return failed(lc.ATLAS, "could not read the ATLAS result: %v", err)
	}
	if err := f.putZ(ctx, f.atlasKey(field, t)+".txt.zst", text); err != nil {
		return failed(lc.ATLAS, "could not store the ATLAS result: %v", err)
	}
	_ = f.atlasRequest(ctx, http.MethodDelete, task.URL, nil, nil)
	return atlasFetch(t, text)
}

// ATLASRow is one line of an ATLAS forced photometry result.
type ATLASRow struct {
	MJD, UJy, DUJy                   float64
	Filter                           string
	Err                              int
	X, Y, Maj, Min, APFit, Mag5, Sky float64
	Obs                              string
}

// Good says whether a row passes the cuts the ATLAS FAQ suggests.
func (r ATLASRow) Good() bool {
	return r.DUJy < 10000 && r.Err == 0 &&
		r.X > 100 && r.X < 10460 && r.Y > 100 && r.Y < 10460 &&
		r.Maj > 1.6 && r.Maj < 5 && r.Min > 1.6 && r.Min < 5 &&
		r.APFit > -1 && r.APFit < -0.1 && r.Mag5 > 17 && r.Sky > 17 &&
		(r.Filter == "c" || r.Filter == "o")
}

func parseATLAS(b []byte) ([]ATLASRow, error) {
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	var col map[string]int
	var out []ATLASRow
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if col == nil {
			if !strings.HasPrefix(line, "###") {
				return nil, errors.New("no ### header line")
			}
			col = map[string]int{}
			for i, h := range strings.Fields(strings.TrimPrefix(line, "###")) {
				col[h] = i
			}
			for _, c := range []string{"MJD", "uJy", "duJy", "F", "err", "x", "y", "maj", "min", "apfit", "mag5sig", "Sky", "Obs"} {
				if _, ok := col[c]; !ok {
					return nil, fmt.Errorf("no %s column", c)
				}
			}
			continue
		}
		fs := strings.Fields(line)
		if len(fs) < len(col) {
			continue
		}
		num := func(c string) float64 {
			v, err := strconv.ParseFloat(fs[col[c]], 64)
			if err != nil {
				return math.NaN()
			}
			return v
		}
		r := ATLASRow{
			MJD: num("MJD"), UJy: num("uJy"), DUJy: num("duJy"), Filter: fs[col["F"]],
			X: num("x"), Y: num("y"), Maj: num("maj"), Min: num("min"),
			APFit: num("apfit"), Mag5: num("mag5sig"), Sky: num("Sky"), Obs: fs[col["Obs"]],
		}
		e, err := strconv.Atoi(fs[col["err"]])
		if err != nil {
			e = -1
		}
		r.Err = e
		out = append(out, r)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if col == nil {
		return nil, errors.New("empty result")
	}
	return out, nil
}

func templateSpan(mjd float64) int {
	n := 0
	for _, s := range ATLASTemplateMJD() {
		if mjd >= s {
			n++
		}
	}
	return n
}

// atlasFetch turns an ATLAS result into one relative series per filter.
// Difference fluxes are put on one level per camera unit and template
// span (their median taken as the normal level) and scaled by the light
// of the star and its Gaia neighbours within ATLASDilutionRadius, from
// their G magnitudes, since ATLAS cannot split them.
func atlasFetch(t Target, text []byte) Fetch {
	rows, err := parseATLAS(text)
	if err != nil {
		return failed(lc.ATLAS, "could not read the ATLAS result: %v", err)
	}
	d, n := dilution(t, ATLASDilutionRadius)
	fe := Fetch{Survey: lc.ATLAS, State: Done}
	if t.G == 0 || math.IsNaN(t.G) {
		return failed(lc.ATLAS, "no Gaia G magnitude to scale the ATLAS fluxes by")
	}
	f0 := math.Pow(10, -0.4*(t.G-23.9)) / d
	for _, band := range []string{"c", "o"} {
		groups := map[string][]float64{}
		var good []ATLASRow
		for _, r := range rows {
			if r.Filter != band || !r.Good() || math.IsNaN(r.UJy) || len(r.Obs) < 3 {
				continue
			}
			k := r.Obs[:3] + "/" + strconv.Itoa(templateSpan(r.MJD))
			groups[k] = append(groups[k], r.UJy)
			good = append(good, r)
		}
		base := map[string]float64{}
		for k, v := range groups {
			base[k] = median(v)
		}
		var pts []lc.Point
		for _, r := range good {
			rel := 1 + (r.UJy-base[r.Obs[:3]+"/"+strconv.Itoa(templateSpan(r.MJD))])/f0
			if rel <= atlasMinRel {
				continue
			}
			pts = append(pts, lc.Point{
				BJD: lc.BJD(lc.MJDTime(r.MJD), t.RA, t.Dec),
				Mag: -2.5 * math.Log10(rel),
				Err: fluxToMagErr * r.DUJy / f0,
			})
		}
		if len(pts) == 0 {
			continue
		}
		sort.Slice(pts, func(i, j int) bool { return pts[i].BJD < pts[j].BJD })
		note := "flux scale from Gaia G"
		if n > 0 {
			note = joinNotes(note, blendNote(d, n, ATLASDilutionRadius, "ATLAS"))
		}
		fe.Series = append(fe.Series, lc.Series{Survey: lc.ATLAS, Band: band, Points: pts, Relative: true, Dilution: d, Note: note})
	}
	if len(fe.Series) == 0 {
		fe.Note = "ATLAS has no epochs passing the quality cuts"
	}
	return fe
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	m := len(s) / 2
	if len(s)%2 == 1 {
		return s[m]
	}
	return (s[m-1] + s[m]) / 2
}
