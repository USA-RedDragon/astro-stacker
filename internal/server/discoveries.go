package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// DiscoverySource runs the night comparisons and reads their cutouts.
type DiscoverySource interface {
	Live() discoveries.Live
	Request(object string, night time.Time, group string)
	Cutout(ctx context.Context, key string) ([]byte, error)
}

const (
	discoveryListLimit = 500
	discoveryMaxLimit  = 5000
	cutoutCache        = "private, max-age=86400"
	errNightFormat     = "night must be YYYY-MM-DD"
)

func cutoutType(name string) (string, bool) {
	switch name {
	case "night.png", "ref.png", "diff.png":
		return "image/png", true
	case "stamp.fits":
		return "application/fits", true
	}
	return "", false
}

// DiscoveryRun is one night of one target compared with its other nights.
type DiscoveryRun struct {
	ID            int                           `json:"id"`
	Object        string                        `json:"object"`
	Night         string                        `json:"night"`
	Group         string                        `json:"group"`
	Filters       []string                      `json:"filters"`
	State         string                        `json:"state"`
	Stage         string                        `json:"stage,omitempty"`
	Error         *string                       `json:"error,omitempty"`
	NightSubs     int                           `json:"night_subs"`
	RefSubs       int                           `json:"ref_subs"`
	RefNights     int                           `json:"ref_nights"`
	Depth         *float64                      `json:"depth"`
	SubDepth      *float64                      `json:"sub_depth"`
	ZeroPoint     *float64                      `json:"zero_point"`
	AstroRMS      *float64                      `json:"astrometry_rms_arcsec"`
	AstroStars    int                           `json:"astrometry_stars"`
	HFRNight      float64                       `json:"hfr_night_px"`
	HFRRef        float64                       `json:"hfr_ref_px"`
	PixelScale    float64                       `json:"pixel_scale"`
	FieldRA       float64                       `json:"field_ra"`
	FieldDec      float64                       `json:"field_dec"`
	Candidates    int                           `json:"candidates"`
	New           int                           `json:"new"`
	Tracks        int                           `json:"tracks"`
	KnownTracks   int                           `json:"known_tracks"`
	BodiesInField int                           `json:"known_bodies_in_field"`
	Checked       []string                      `json:"checked"`
	Failed        []string                      `json:"failed"`
	Funnel        map[string]discoveries.Funnel `json:"funnel,omitempty"`
	Classes       map[string]int                `json:"classes"`
	Vetted        int                           `json:"vetted"`
	MidTime       *time.Time                    `json:"mid_time"`
	FinishedAt    *time.Time                    `json:"finished_at"`
	Seconds       float64                       `json:"seconds"`
}

// DiscoveryCandidate is a change or a moving object found on a night.
type DiscoveryCandidate struct {
	ID         int                      `json:"id"`
	RunID      int                      `json:"run_id"`
	Object     string                   `json:"object"`
	Night      string                   `json:"night"`
	Group      string                   `json:"group"`
	Kind       string                   `json:"kind"`
	Class      string                   `json:"class"`
	X          float64                  `json:"x"`
	Y          float64                  `json:"y"`
	RA         float64                  `json:"ra"`
	Dec        float64                  `json:"dec"`
	Sign       int                      `json:"sign"`
	SNR        float64                  `json:"snr"`
	DeltaMag   *float64                 `json:"delta_mag"`
	NightMag   *float64                 `json:"night_mag"`
	RefMag     *float64                 `json:"ref_mag"`
	GaiaG      *float64                 `json:"gaia_g"`
	PresentIn  int                      `json:"present_in"`
	SubsTested int                      `json:"subs_tested"`
	Matches    []discoveries.Match      `json:"matches"`
	RateArcmin *float64                 `json:"rate_arcsec_per_min,omitempty"`
	OffsetArc  *float64                 `json:"offset_arcsec,omitempty"`
	Vetting    string                   `json:"vetting"`
	VetNote    string                   `json:"vet_note"`
	VettedAt   *time.Time               `json:"vetted_at"`
	Cutouts    *DiscoveryCutouts        `json:"cutouts"`
	Light      []discoveries.LightPoint `json:"light,omitempty"`
	Track      *discoveries.TrackRecord `json:"track,omitempty"`
	Run        *DiscoveryRun            `json:"run,omitempty"`
	BodyV      *float64                 `json:"body_v,omitempty"`
}

// DiscoveryCutouts link a candidate's night, reference and difference
// cutouts and its FITS stamp.
type DiscoveryCutouts struct {
	Night string `json:"night"`
	Ref   string `json:"ref"`
	Diff  string `json:"diff"`
	FITS  string `json:"fits"`
}

func splitList(s string) []string {
	if s == "" {
		return []string{}
	}
	return strings.Split(s, ",")
}

func runOf(r app.DiscoveryRun, classes map[string]int, vetted int) DiscoveryRun {
	out := DiscoveryRun{ID: r.ID, Object: r.Object, Night: r.Night.Format(time.DateOnly), Group: r.Group, Filters: splitList(r.Filters), State: r.State, Stage: r.Stage,
		Error: r.Error, NightSubs: r.NightSubs, RefSubs: r.RefSubs, RefNights: r.RefNights, Depth: r.Depth, SubDepth: r.SubDepth, ZeroPoint: r.ZeroPoint,
		AstroRMS: r.AstroRMS, AstroStars: r.AstroStars, HFRNight: r.HFRNight, HFRRef: r.HFRRef, PixelScale: r.PixelScale, FieldRA: r.FieldRA, FieldDec: r.FieldDec,
		Candidates: r.Candidates, New: r.NewCandidates, Tracks: r.Tracks, KnownTracks: r.KnownTracks, BodiesInField: r.BodiesInField,
		Checked: splitList(r.Checked), Failed: splitList(r.Failed), Classes: classes, Vetted: vetted, MidTime: r.MidTime, FinishedAt: r.FinishedAt, Seconds: r.Seconds}
	if out.Classes == nil {
		out.Classes = map[string]int{}
	}
	if r.Funnel != "" {
		_ = json.Unmarshal([]byte(r.Funnel), &out.Funnel)
	}
	return out
}

func candidateOf(c app.DiscoveryCandidate, detail bool) DiscoveryCandidate {
	out := DiscoveryCandidate{ID: c.ID, RunID: c.RunID, Object: c.Object, Night: c.Night.Format(time.DateOnly), Group: c.Group, Kind: c.Kind, Class: c.Class,
		X: c.X, Y: c.Y, RA: c.RA, Dec: c.Dec, Sign: c.Sign, SNR: c.SNR, DeltaMag: c.DeltaMag, NightMag: c.NightMag, RefMag: c.RefMag, GaiaG: c.GaiaG,
		PresentIn: c.PresentIn, SubsTested: c.SubsTested, RateArcmin: c.RateArcmin, OffsetArc: c.OffsetArc, Vetting: c.Vetting, VetNote: c.VetNote, VettedAt: c.VettedAt,
		Matches: []discoveries.Match{}}
	if c.Matches != "" {
		_ = json.Unmarshal([]byte(c.Matches), &out.Matches)
	}
	for _, m := range out.Matches {
		if m.Catalog == "SkyBoT" && m.Mag != nil {
			out.BodyV = m.Mag
		}
	}
	if c.CutoutKey != "" {
		base := "/api/v1/discoveries/candidates/" + strconv.Itoa(c.ID) + "/cutout/"
		out.Cutouts = &DiscoveryCutouts{Night: base + "night.png", Ref: base + "ref.png", Diff: base + "diff.png", FITS: base + "stamp.fits"}
	}
	if detail {
		if c.Light != "" {
			_ = json.Unmarshal([]byte(c.Light), &out.Light)
		}
		if c.Track != "" {
			var t discoveries.TrackRecord
			if json.Unmarshal([]byte(c.Track), &t) == nil {
				out.Track = &t
			}
		}
	}
	return out
}

func parseNight(s string) (time.Time, bool) {
	t, err := time.Parse(time.DateOnly, s)
	return t, err == nil
}

func applyDiscoveryRoutes(g *gin.RouterGroup, src DiscoverySource) {
	g.GET("/discoveries/status", discoveryStatus(src))
	g.GET("/discoveries/nights", discoveryNights)
	g.GET("/discoveries/runs", discoveryRuns)
	g.POST("/discoveries/runs", requestDiscoveryRun(src))
	g.GET("/discoveries/candidates", discoveryCandidates)
	g.GET("/discoveries/candidates/:id", discoveryCandidate)
	g.PUT("/discoveries/candidates/:id/vetting", vetDiscoveryCandidate)
	g.GET("/discoveries/candidates/:id/cutout/:name", discoveryCutout(src))
}

func discoveryStatus(src DiscoverySource) gin.HandlerFunc {
	return func(c *gin.Context) {
		if src == nil {
			c.JSON(http.StatusOK, discoveries.Live{State: "off"})
			return
		}
		c.JSON(http.StatusOK, src.Live())
	}
}

func discoveryNights(c *gin.Context) {
	db, ok := appDB(c)
	if !ok {
		return
	}
	type row struct {
		Night      time.Time
		Runs       int
		Candidates int
		New        int
	}
	var rows []row
	if err := db.Model(&app.DiscoveryRun{}).Select("night, COUNT(*) AS runs, SUM(candidates) AS candidates, SUM(new_candidates) AS new").
		Where("state = ?", app.DiscoveryDone).Group("night").Order("night DESC").Scan(&rows).Error; err != nil {
		serverError(c, err)
		return
	}
	type night struct {
		Night      string `json:"night"`
		Runs       int    `json:"runs"`
		Candidates int    `json:"candidates"`
		New        int    `json:"new"`
	}
	out := make([]night, len(rows))
	for i, r := range rows {
		out[i] = night{r.Night.Format(time.DateOnly), r.Runs, r.Candidates, r.New}
	}
	c.JSON(http.StatusOK, out)
}

func discoveryRuns(c *gin.Context) {
	db, ok := appDB(c)
	if !ok {
		return
	}
	q := db.Model(&app.DiscoveryRun{})
	if n := c.Query("night"); n != "" {
		t, ok := parseNight(n)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{errorKey: errNightFormat})
			return
		}
		q = q.Where("night >= ? AND night < ?", t, t.AddDate(0, 0, 1))
	}
	if o := c.Query("object"); o != "" {
		q = q.Where("object = ?", o)
	}
	if s := c.Query("state"); s != "" {
		q = q.Where("state IN ?", strings.Split(s, ","))
	}
	var runs []app.DiscoveryRun
	if err := q.Order("night DESC, object, filter_group").Limit(2000).Find(&runs).Error; err != nil {
		serverError(c, err)
		return
	}
	classes, vetted, err := runCounts(db, runs)
	if err != nil {
		serverError(c, err)
		return
	}
	out := make([]DiscoveryRun, len(runs))
	for i, r := range runs {
		out[i] = runOf(r, classes[r.ID], vetted[r.ID])
	}
	c.JSON(http.StatusOK, out)
}

func requestDiscoveryRun(src DiscoverySource) gin.HandlerFunc {
	return func(c *gin.Context) {
		if src == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{errorKey: "discoveries are off"})
			return
		}
		var body struct {
			Object string `json:"object"`
			Night  string `json:"night"`
			Group  string `json:"group"`
		}
		if err := c.ShouldBindJSON(&body); err != nil || body.Object == "" {
			c.JSON(http.StatusBadRequest, gin.H{errorKey: "object and night are required"})
			return
		}
		t, ok := parseNight(body.Night)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{errorKey: errNightFormat})
			return
		}
		db, ok := appDB(c)
		if !ok {
			return
		}
		var filters []string
		if err := db.Model(&app.Stack{}).Where("object = ?", body.Object).Distinct("filter").Pluck("filter", &filters).Error; err != nil {
			serverError(c, err)
			return
		}
		if len(filters) == 0 {
			c.JSON(http.StatusNotFound, gin.H{errorKey: "no stacked target of that name"})
			return
		}
		if body.Group != "" && !slices.ContainsFunc(filters, func(f string) bool { return discoveries.GroupOf(f) == body.Group }) {
			c.JSON(http.StatusBadRequest, gin.H{errorKey: "group must be broadband or one of the target's narrowband filters"})
			return
		}
		src.Request(body.Object, t, body.Group)
		c.JSON(http.StatusAccepted, gin.H{"queued": true})
	}
}

func discoveryCandidates(c *gin.Context) {
	db, ok := appDB(c)
	if !ok {
		return
	}
	q := db.Model(&app.DiscoveryCandidate{})
	if n := c.Query("night"); n != "" {
		t, ok := parseNight(n)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{errorKey: errNightFormat})
			return
		}
		q = q.Where("night >= ? AND night < ?", t, t.AddDate(0, 0, 1))
	}
	if o := c.Query("object"); o != "" {
		q = q.Where("object = ?", o)
	}
	if gr := c.Query("group"); gr != "" {
		q = q.Where("filter_group = ?", gr)
	}
	if k := c.Query("kind"); k != "" {
		q = q.Where("kind = ?", k)
	}
	if cl := c.Query("class"); cl != "" {
		list := strings.Split(cl, ",")
		for _, x := range list {
			if !slices.Contains(discoveries.Classes(), x) {
				c.JSON(http.StatusBadRequest, gin.H{errorKey: "unknown class " + x})
				return
			}
		}
		q = q.Where("class IN ?", list)
	}
	if s := c.Query("min_snr"); s != "" {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{errorKey: "min_snr must be a number"})
			return
		}
		q = q.Where("snr >= ?", v)
	}
	switch v := c.Query("vetting"); v {
	case "":
	case "none":
		q = q.Where("vetting = ''")
	case "any":
		q = q.Where("vetting <> ''")
	default:
		if !validVetting(v) {
			c.JSON(http.StatusBadRequest, gin.H{errorKey: "vetting must be none, any, real, artifact, known or unsure"})
			return
		}
		q = q.Where("vetting = ?", v)
	}
	limit := discoveryListLimit
	if s := c.Query("limit"); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v > 0 {
			limit = min(v, discoveryMaxLimit)
		}
	}
	var rows []app.DiscoveryCandidate
	if err := q.Omit("light", "track").Order("night DESC, snr DESC").Limit(limit).Find(&rows).Error; err != nil {
		serverError(c, err)
		return
	}
	out := make([]DiscoveryCandidate, len(rows))
	for i, r := range rows {
		out[i] = candidateOf(r, false)
	}
	c.JSON(http.StatusOK, out)
}

func discoveryCandidate(c *gin.Context) {
	db, ok := appDB(c)
	if !ok {
		return
	}
	cand, ok := candidateByID(c, db)
	if !ok {
		return
	}
	out := candidateOf(cand, true)
	var run app.DiscoveryRun
	if err := db.First(&run, cand.RunID).Error; err == nil {
		classes, vetted, _ := runCounts(db, []app.DiscoveryRun{run})
		r := runOf(run, classes[run.ID], vetted[run.ID])
		out.Run = &r
	}
	c.JSON(http.StatusOK, out)
}

func vetDiscoveryCandidate(c *gin.Context) {
	db, ok := appDB(c)
	if !ok {
		return
	}
	cand, ok := candidateByID(c, db)
	if !ok {
		return
	}
	var body struct {
		State string `json:"state"`
		Note  string `json:"note"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{errorKey: err.Error()})
		return
	}
	if body.State != "" && !validVetting(body.State) {
		c.JSON(http.StatusBadRequest, gin.H{errorKey: "state must be real, artifact, known, unsure or empty"})
		return
	}
	var at *time.Time
	if body.State != "" {
		now := time.Now().UTC()
		at = &now
	}
	if err := db.Model(&cand).Updates(map[string]any{"vetting": body.State, "vet_note": strings.TrimSpace(body.Note), "vetted_at": at}).Error; err != nil {
		serverError(c, err)
		return
	}
	cand.Vetting, cand.VetNote, cand.VettedAt = body.State, strings.TrimSpace(body.Note), at
	c.JSON(http.StatusOK, candidateOf(cand, false))
}

func discoveryCutout(src DiscoverySource) gin.HandlerFunc {
	return func(c *gin.Context) {
		db, ok := appDB(c)
		if !ok {
			return
		}
		ct, known := cutoutType(c.Param("name"))
		if !known {
			c.JSON(http.StatusNotFound, gin.H{errorKey: "no such cutout"})
			return
		}
		cand, ok := candidateByID(c, db)
		if !ok {
			return
		}
		if cand.CutoutKey == "" || src == nil {
			c.JSON(http.StatusNotFound, gin.H{errorKey: "no cutouts for this candidate"})
			return
		}
		b, err := src.Cutout(c.Request.Context(), path.Join(cand.CutoutKey, c.Param("name")))
		if err != nil {
			if discoveries.IsNotFound(err) {
				c.JSON(http.StatusNotFound, gin.H{errorKey: "cutout not found"})
				return
			}
			c.JSON(http.StatusBadGateway, gin.H{errorKey: err.Error()})
			return
		}
		c.Header(cacheControl, cutoutCache)
		if strings.HasSuffix(c.Param("name"), ".fits") {
			c.Header("Content-Disposition", "attachment; filename=\"candidate-"+strconv.Itoa(cand.ID)+".fits\"")
		}
		c.Data(http.StatusOK, ct, b)
	}
}

func validVetting(s string) bool {
	return s == app.VetReal || s == app.VetArtifact || s == app.VetKnown || s == app.VetUnsure
}

func appDB(c *gin.Context) (*gorm.DB, bool) {
	di, ok := depInjection(c)
	if !ok {
		return nil, false
	}
	return di.AppStore.DB().WithContext(c.Request.Context()), true
}

func candidateByID(c *gin.Context, db *gorm.DB) (app.DiscoveryCandidate, bool) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{errorKey: "id must be a number"})
		return app.DiscoveryCandidate{}, false
	}
	var cand app.DiscoveryCandidate
	if err := db.First(&cand, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{errorKey: "no such candidate"})
		} else {
			serverError(c, err)
		}
		return app.DiscoveryCandidate{}, false
	}
	return cand, true
}

// runCounts counts each run's candidates by class, and those vetted.
func runCounts(db *gorm.DB, runs []app.DiscoveryRun) (map[int]map[string]int, map[int]int, error) {
	ids := make([]int, len(runs))
	for i, r := range runs {
		ids[i] = r.ID
	}
	classes, vetted := map[int]map[string]int{}, map[int]int{}
	if len(ids) == 0 {
		return classes, vetted, nil
	}
	type row struct {
		RunID  int
		Class  string
		Vetted bool
		N      int
	}
	var rows []row
	if err := db.Model(&app.DiscoveryCandidate{}).Select("run_id, class, vetting <> '' AS vetted, COUNT(*) AS n").
		Where("run_id IN ?", ids).Group("run_id, class, vetting <> ''").Scan(&rows).Error; err != nil {
		return nil, nil, err
	}
	for _, r := range rows {
		if classes[r.RunID] == nil {
			classes[r.RunID] = map[string]int{}
		}
		classes[r.RunID][r.Class] += r.N
		if r.Vetted {
			vetted[r.RunID] += r.N
		}
	}
	return classes, vetted, nil
}

// statusClientClosed is nginx's code for a request the client gave up on.
const statusClientClosed = 499

// serverError answers 500 with the error, unless the client has gone, as
// when a page is closed with its images still loading: then the error is
// only the cancelled request and is answered 499.
func serverError(c *gin.Context, err error) {
	if c.Request.Context().Err() != nil {
		c.AbortWithStatus(statusClientClosed)
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
}
