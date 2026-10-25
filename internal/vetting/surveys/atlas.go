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
	// ATLASTaskMaxAge is how long a task may wait at ATLAS before it is
	// given up and the star queued again later.
	ATLASTaskMaxAge = 7 * 24 * time.Hour
	// DefaultATLASGap is the least time between two requests to ATLAS.
	DefaultATLASGap = 5 * time.Second
	maxATLASResult  = 64 << 20
	maxATLASJSON    = 4 << 20
	atlasMinRel     = 0.05
	fluxToMagErr    = 1.0857
	putTries        = 3
	atlasListPage   = 100
)

// ATLASTemplateMJD are the dates ATLAS changed its reference templates;
// difference fluxes are put on one level separately in each span.
func ATLASTemplateMJD() []float64 { return []float64{58417, 58882} }

// ATLASTask is what the cache keeps of a queued ATLAS task. Unsure is set
// when the queue request got no clear answer, so the task may exist at
// ATLAS without its URL known here.
type ATLASTask struct {
	URL      string    `json:"task_url"`
	Comment  string    `json:"comment"`
	Queued   time.Time `json:"queued_at"`
	RA       float64   `json:"ra"`
	Dec      float64   `json:"dec"`
	Unsure   bool      `json:"unsure,omitempty"`
	Error    string    `json:"error,omitempty"`
	FailedAt time.Time `json:"failed_at"`
	// Refresh says the task fetches newer photometry for a star whose
	// result is already cached, and DoneAt when its result was stored.
	Refresh bool      `json:"refresh,omitempty"`
	DoneAt  time.Time `json:"done_at"`
}

type atlasTaskStatus struct {
	URL       string  `json:"url"`
	Comment   *string `json:"comment"`
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

func atlasComment(t Target) string { return "astro-stacker vetting " + starKey(t) }

func (f *Fetcher) atlas(ctx context.Context, field Field, t Target, done func([]byte) Fetch) Fetch {
	base := f.atlasKey(field, t)
	raw, err := f.getZ(ctx, base+".txt.zst")
	if err == nil {
		if !isFresh(ctx) || f.ATLASToken == "" {
			return done(raw)
		}
		return f.atlasRefresh(ctx, field, t, raw, done)
	}
	if !isNotFound(err) {
		return failed(lc.ATLAS, "could not read the ATLAS cache: %v", err)
	}
	if f.ATLASToken == "" {
		return failed(lc.ATLAS, "no ATLAS token")
	}
	f.atlasMu.Lock()
	defer f.atlasMu.Unlock()
	tj, err := f.getZ(ctx, base+".task.json.zst")
	if err != nil && !isNotFound(err) {
		return failed(lc.ATLAS, "could not read the ATLAS task: %v", err)
	}
	if err == nil {
		var task ATLASTask
		if err := json.Unmarshal(tj, &task); err != nil {
			return failed(lc.ATLAS, "damaged ATLAS task record: %v", err)
		}
		switch {
		case task.Unsure:
			return f.atlasFind(ctx, field, t, task, done)
		case task.Error == "":
			return f.atlasPoll(ctx, field, t, task, done)
		case f.now().Sub(task.FailedAt) < ATLASRetryFailed:
			return failed(lc.ATLAS, "the ATLAS task failed: %s", task.Error)
		}
	}
	return f.atlasQueue(ctx, field, t, false)
}

// atlasRefresh answers from the cached result raw and, when that result is
// older than ATLASMaxAge, queues the star at ATLAS again. While the new
// task waits the old result is used and the answer stays Pending, so a
// later run picks the new result up; a failed refresh keeps the old result
// and tries again after ATLASRetryFailed.
func (f *Fetcher) atlasRefresh(ctx context.Context, field Field, t Target, raw []byte, done func([]byte) Fetch) Fetch {
	f.atlasMu.Lock()
	defer f.atlasMu.Unlock()
	var task ATLASTask
	tj, err := f.getZ(ctx, f.atlasKey(field, t)+".task.json.zst")
	switch {
	case err == nil:
		if json.Unmarshal(tj, &task) != nil {
			task = ATLASTask{}
		}
	case !isNotFound(err):
		return done(raw)
	}
	keep := func(fe Fetch) Fetch {
		if fe.State == Done {
			return fe
		}
		out := done(raw)
		if fe.State == Pending {
			out.State = Pending
			out.Note = joinNotes("newer ATLAS photometry asked for: "+fe.Note, out.Note)
		}
		return out
	}
	switch {
	case task.Refresh && task.DoneAt.IsZero() && task.Error == "" && task.Unsure:
		return keep(f.atlasFind(ctx, field, t, task, done))
	case task.Refresh && task.DoneAt.IsZero() && task.Error == "":
		return keep(f.atlasPoll(ctx, field, t, task, done))
	case task.Refresh && task.Error != "" && f.now().Sub(task.FailedAt) < ATLASRetryFailed:
		return done(raw)
	}
	at := task.DoneAt
	if at.IsZero() && task.Error == "" {
		at = task.Queued
	}
	if !expired(at, f.now(), ATLASMaxAge) {
		return done(raw)
	}
	return keep(f.atlasQueue(ctx, field, t, true))
}

// atlasWait keeps DefaultATLASGap between requests; callers hold atlasMu.
func (f *Fetcher) atlasWait(ctx context.Context) error {
	gap := f.atlasGap
	if gap == 0 {
		gap = DefaultATLASGap
	}
	if wait := gap - f.now().Sub(f.lastATLAS); gap > 0 && wait > 0 {
		if err := sleepCtx(ctx, wait); err != nil {
			return err
		}
	}
	f.lastATLAS = f.now()
	return nil
}

func (f *Fetcher) atlasRequest(ctx context.Context, method, u string, form url.Values, out any) error {
	return f.send(ctx, method != http.MethodPost, func() (*http.Request, error) {
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
		b, err := readAll(r.Body, maxATLASJSON)
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

func (f *Fetcher) atlasQueue(ctx context.Context, field Field, t Target, refresh bool) Fetch {
	key := f.atlasKey(field, t)
	f.addWant(key, atlasWant{field: field, t: t, refresh: refresh, prio: priorityOf(ctx)})
	sv, err := f.atlasServe(ctx, key)
	if err != nil {
		return f.atlasError("could not read the ATLAS queue", err)
	}
	if sv.got {
		return sv.fe
	}
	return Fetch{Survey: lc.ATLAS, State: Pending, Note: fmt.Sprintf("waiting for room: %d of our ATLAS tasks are queued and %d stars wait before this one", sv.queued, f.wantsBefore(key))}
}

// atlasPost queues one star at ATLAS and records its task. Throttled says
// ATLAS refused it for asking too often.
func (f *Fetcher) atlasPost(ctx context.Context, field Field, t Target, refresh bool) (fe Fetch, throttled bool) {
	form := url.Values{}
	form.Set("ra", strconv.FormatFloat(t.RA, 'f', 7, 64))
	form.Set("dec", strconv.FormatFloat(t.Dec, 'f', 7, 64))
	form.Set("radec_epoch_year", "2016.0")
	form.Set("propermotion_ra", strconv.FormatFloat(t.PMRA, 'f', 4, 64))
	form.Set("propermotion_dec", strconv.FormatFloat(t.PMDec, 'f', 4, 64))
	form.Set("mjd_min", strconv.FormatFloat(ATLASMJDMin, 'f', 1, 64))
	form.Set("use_reduced", "false")
	form.Set("send_email", "false")
	form.Set("comment", atlasComment(t))
	task := ATLASTask{Comment: atlasComment(t), Queued: f.now().UTC(), RA: t.RA, Dec: t.Dec, Refresh: refresh}
	var st atlasTaskStatus
	err := f.atlasRequest(ctx, http.MethodPost, f.atlasBase()+"/queue/", form, &st)
	if errors.Is(err, errUnsure) {
		task.Unsure = true
		if perr := f.putTask(ctx, field, t, task); perr != nil {
			return failed(lc.ATLAS, "the ATLAS queue request got no clear answer (%v) and could not be recorded: %v", err, perr), false
		}
		return Fetch{Survey: lc.ATLAS, State: Pending, Note: "the ATLAS queue request got no clear answer; looked up on the next run"}, false
	}
	if err != nil {
		var he *httpError
		return f.atlasError("could not queue the ATLAS task", err), errors.As(err, &he) && he.Status == http.StatusTooManyRequests
	}
	if st.URL == "" {
		return failed(lc.ATLAS, "ATLAS queued the task but gave no task URL"), false
	}
	task.URL = st.URL
	if err := f.putTask(ctx, field, t, task); err != nil {
		_ = f.atlasRequest(ctx, http.MethodDelete, st.URL, nil, nil)
		return failed(lc.ATLAS, "could not record the ATLAS task, so it was taken back: %v", err), false
	}
	return Fetch{Survey: lc.ATLAS, State: Pending, Note: "queued at ATLAS"}, false
}

// atlasFind looks for a task whose queue request got no clear answer
// among our newest tasks at ATLAS, by its comment.
func (f *Fetcher) atlasFind(ctx context.Context, field Field, t Target, task ATLASTask, done func([]byte) Fetch) Fetch {
	var page struct {
		Results []atlasTaskStatus `json:"results"`
	}
	u := fmt.Sprintf("%s/queue/?format=json&ordering=-timestamp&pagesize=%d", f.atlasBase(), atlasListPage)
	if err := f.atlasRequest(ctx, http.MethodGet, u, nil, &page); err != nil {
		return f.atlasError("could not list our ATLAS tasks", err)
	}
	for _, r := range page.Results {
		if r.Comment != nil && *r.Comment == task.Comment && r.URL != "" {
			task.URL, task.Unsure = r.URL, false
			if err := f.putTask(ctx, field, t, task); err != nil {
				return failed(lc.ATLAS, "found the ATLAS task but could not record it: %v", err)
			}
			return f.atlasPoll(ctx, field, t, task, done)
		}
	}
	return f.atlasQueue(ctx, field, t, task.Refresh)
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
	for try := 1; ; try++ {
		err = f.putZ(ctx, f.atlasKey(field, t)+".task.json.zst", raw)
		if err == nil || try == putTries {
			return err
		}
		if err := sleepCtx(ctx, time.Duration(try)*time.Second); err != nil {
			return err
		}
	}
}

// atlasGiveUp records a task as failed, takes it off ATLAS and says why.
func (f *Fetcher) atlasGiveUp(ctx context.Context, field Field, t Target, task ATLASTask, why string) Fetch {
	task.Error, task.FailedAt = why, f.now().UTC()
	if err := f.putTask(ctx, field, t, task); err != nil {
		return failed(lc.ATLAS, "the ATLAS task failed (%s) and could not be recorded: %v", why, err)
	}
	_ = f.atlasRequest(ctx, http.MethodDelete, task.URL, nil, nil)
	return failed(lc.ATLAS, "the ATLAS task failed: %s; queued again after %s", why, ATLASRetryFailed)
}

func (f *Fetcher) atlasPoll(ctx context.Context, field Field, t Target, task ATLASTask, done func([]byte) Fetch) Fetch {
	var st atlasTaskStatus
	if err := f.atlasRequest(ctx, http.MethodGet, task.URL, nil, &st); err != nil {
		var he *httpError
		if errors.As(err, &he) && he.Status == http.StatusNotFound {
			return f.atlasGiveUp(ctx, field, t, task, "ATLAS no longer has the task")
		}
		return f.atlasError("could not read the ATLAS task", err)
	}
	if st.Finish == nil || *st.Finish == "" {
		if f.now().Sub(task.Queued) > ATLASTaskMaxAge {
			return f.atlasGiveUp(ctx, field, t, task, fmt.Sprintf("not finished after %s", ATLASTaskMaxAge))
		}
		note := "waiting in the ATLAS queue"
		if st.Start != nil && *st.Start != "" {
			note = "ATLAS is running the task"
		} else if st.QueuePos != nil {
			note = fmt.Sprintf("waiting in the ATLAS queue at place %d", *st.QueuePos)
		}
		return Fetch{Survey: lc.ATLAS, State: Pending, Note: note}
	}
	if st.ErrorMsg != nil && *st.ErrorMsg != "" {
		return f.atlasGiveUp(ctx, field, t, task, *st.ErrorMsg)
	}
	if st.ResultURL == nil || *st.ResultURL == "" {
		return f.atlasGiveUp(ctx, field, t, task, "finished with no result")
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
		b, err := readAll(r.Body, maxATLASResult)
		text = b
		return err
	})
	var he *httpError
	if errors.As(err, &he) && (he.Status == http.StatusNotFound || he.Status == http.StatusGone) {
		return f.atlasGiveUp(ctx, field, t, task, "the result is gone")
	}
	if err != nil {
		return f.atlasError("could not download the ATLAS result", err)
	}
	if _, err := parseATLAS(text); err != nil {
		return f.atlasGiveUp(ctx, field, t, task, "unreadable result: "+err.Error())
	}
	if err := f.putZ(ctx, f.atlasKey(field, t)+".txt.zst", text); err != nil {
		return failed(lc.ATLAS, "could not store the ATLAS result: %v", err)
	}
	task.DoneAt = f.now().UTC()
	_ = f.putTask(ctx, field, t, task)
	f.noteRuntime(ctx, st)
	_ = f.atlasRequest(ctx, http.MethodDelete, task.URL, nil, nil)
	return done(text)
}

// ATLASRow is one line of an ATLAS forced photometry result.
type ATLASRow struct {
	MJD, UJy, DUJy                   float64
	ChiN                             float64
	Filter                           string
	Err                              int
	X, Y, Maj, Min, APFit, Mag5, Sky float64
	Obs                              string
}

// atlasMaxChiN is the largest PSF fit chi/N a row may have: the cut
// ATClean (S. Rest et al. 2025, ApJ 979, 114) takes by default. A worse fit is a bad
// subtraction or a neighbour's residual, and often comes with too small an
// error: two nights of source 3635 (camera 01a, 2020-08-12 and 2021-09-08
// UT) at chi/N 14 to 262 and errors of a quarter the usual made one eclipse
// look 0.03 mag deeper, and one frame at 54893 at chi/N 17 showed light
// that was not there. In 3635's ATLAS data the cut drops 10% of the points
// in and out of eclipse alike, so it does not bias eclipse depths.
const atlasMaxChiN = 10

// Good says whether a row passes the cuts the ATLAS FAQ suggests and the
// chi/N cut. A result without a chi/N column passes that cut.
func (r ATLASRow) Good() bool {
	return r.Fair() && !r.ChiCut()
}

// ChiCut says whether the row's PSF fit is worse than the chi/N cut.
func (r ATLASRow) ChiCut() bool { return r.ChiN >= atlasMaxChiN }

// Fair says whether a row passes the cuts the ATLAS FAQ suggests, chi/N
// aside: a frame of a night whose subtraction failed at the spot.
func (r ATLASRow) Fair() bool {
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
			MJD: num("MJD"), UJy: num("uJy"), DUJy: num("duJy"), ChiN: math.NaN(), Filter: fs[col["F"]],
			X: num("x"), Y: num("y"), Maj: num("maj"), Min: num("min"),
			APFit: num("apfit"), Mag5: num("mag5sig"), Sky: num("Sky"), Obs: fs[col["Obs"]],
		}
		if _, ok := col["chi/N"]; ok {
			r.ChiN = num("chi/N")
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

// atlasFetch turns an ATLAS result into one relative series per filter
// and camera unit, since each unit has its own reference images.
// Difference fluxes are put on one level per template span (their median
// taken as the normal level) and scaled by the light
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
	cams := map[string]bool{}
	for _, r := range rows {
		if len(r.Obs) >= 3 {
			cams[r.Obs[:3]] = true
		}
	}
	camList := make([]string, 0, len(cams))
	for c := range cams {
		camList = append(camList, c)
	}
	sort.Strings(camList)
	for _, band := range []string{"c", "o"} {
		for _, cam := range camList {
			if s, ok := atlasSeries(t, rows, band, cam, f0, d, n); ok {
				fe.Series = append(fe.Series, s)
			}
		}
	}
	if len(fe.Series) == 0 {
		fe.Note = "ATLAS has no epochs passing the quality cuts"
	}
	return fe
}

// atlasStart is how much later than the MJD ATLAS gives (the exposure
// start) the middle of its 30 s exposures is, in days.
const atlasStart = 15.0 / 86400

func atlasSeries(t Target, rows []ATLASRow, band, cam string, f0, d float64, n int) (lc.Series, bool) {
	groups := map[int][]float64{}
	var good []ATLASRow
	for _, r := range rows {
		if r.Filter != band || !r.Good() || math.IsNaN(r.UJy) || len(r.Obs) < 3 || r.Obs[:3] != cam {
			continue
		}
		groups[templateSpan(r.MJD)] = append(groups[templateSpan(r.MJD)], r.UJy)
		good = append(good, r)
	}
	base := map[int]float64{}
	for k, v := range groups {
		base[k] = median(v)
	}
	var pts []lc.Point
	for _, r := range good {
		rel := 1 + (r.UJy-base[templateSpan(r.MJD)])/f0
		if rel <= atlasMinRel {
			continue
		}
		pts = append(pts, lc.Point{
			BJD: lc.BJD(lc.MJDTime(r.MJD+atlasStart), t.RA, t.Dec),
			Mag: -2.5 * math.Log10(rel),
			Err: fluxToMagErr * r.DUJy / (rel * f0),
		})
	}
	if len(pts) == 0 {
		return lc.Series{}, false
	}
	sort.Slice(pts, func(i, j int) bool { return pts[i].BJD < pts[j].BJD })
	note := "camera " + cam + "; flux scale from Gaia G"
	if n > 0 {
		note = joinNotes(note, blendNote(d, n, ATLASDilutionRadius, "ATLAS"))
	}
	return lc.Series{Survey: lc.ATLAS, Band: band, Points: pts, Relative: true, Dilution: d, Note: note}, true
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
