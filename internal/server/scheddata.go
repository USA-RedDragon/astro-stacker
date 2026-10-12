package server

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/observatory"
	"github.com/USA-RedDragon/astro-stacker/internal/previewer"
	"github.com/USA-RedDragon/astro-stacker/internal/quality"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/store/tables"
	"github.com/USA-RedDragon/astro-stacker/internal/tsmigration"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	ticksThreshold  = 600000000000000000
	ticksUnixOffset = 621355968000000000
	ticksPerSecond  = 10000000
	tableProject    = "project"
	tableAcquired   = "acquiredimage"
)

type tsProjectRow struct {
	ID          int     `gorm:"column:Id;primaryKey"`
	ProfileID   string  `gorm:"column:profileId"`
	Name        string  `gorm:"column:name"`
	State       *int    `gorm:"column:state"`
	Priority    *int    `gorm:"column:priority"`
	MinimumTime *int    `gorm:"column:minimumtime"`
	IsMosaic    int     `gorm:"column:isMosaic"`
	GUID        *string `gorm:"column:guid"`
}

func (tsProjectRow) TableName() string { return tableProject }

type tsTargetRow struct {
	ID        int      `gorm:"column:Id;primaryKey"`
	Name      string   `gorm:"column:name"`
	Active    int      `gorm:"column:active"`
	RA        *float64 `gorm:"column:ra"`
	Dec       *float64 `gorm:"column:dec"`
	Rotation  *float64 `gorm:"column:rotation"`
	ProjectID *int     `gorm:"column:projectid"`
	GUID      *string  `gorm:"column:guid"`
}

func (tsTargetRow) TableName() string { return "target" }

type tsPlanRow struct {
	ID         int      `gorm:"column:Id;primaryKey"`
	Exposure   *float64 `gorm:"column:exposure"`
	Desired    *int     `gorm:"column:desired"`
	Acquired   *int     `gorm:"column:acquired"`
	Accepted   *int     `gorm:"column:accepted"`
	TargetID   *int     `gorm:"column:targetid"`
	TemplateID *int     `gorm:"column:exposureTemplateId"`
	Enabled    *int     `gorm:"column:enabled"`
	GUID       *string  `gorm:"column:guid"`
}

func (tsPlanRow) TableName() string { return "exposureplan" }

type tsTemplateRow struct {
	ID              int      `gorm:"column:Id;primaryKey"`
	Name            string   `gorm:"column:name"`
	FilterName      string   `gorm:"column:filtername"`
	DefaultExposure *float64 `gorm:"column:defaultexposure"`
}

func (tsTemplateRow) TableName() string { return "exposuretemplate" }

type SchedPlan struct {
	ID              int      `json:"id"`
	GUID            *string  `json:"guid"`
	Filter          *string  `json:"filter"`
	Template        *string  `json:"template"`
	Exposure        *float64 `json:"exposure"`
	ExposureSource  string   `json:"exposureSource,omitempty"`
	PlanExposure    *float64 `json:"planExposure"`
	DefaultExposure *float64 `json:"defaultExposure"`
	Desired         *int     `json:"desired"`
	Acquired        *int     `json:"acquired"`
	Accepted        *int     `json:"accepted"`
	Enabled         *bool    `json:"enabled"`
}

type SchedTarget struct {
	ID       int         `json:"id"`
	GUID     *string     `json:"guid"`
	Name     string      `json:"name"`
	Active   bool        `json:"active"`
	RA       *float64    `json:"ra"`
	Dec      *float64    `json:"dec"`
	Rotation *float64    `json:"rotation"`
	Plans    []SchedPlan `json:"plans"`
}

type SchedProject struct {
	ID          int           `json:"id"`
	GUID        *string       `json:"guid"`
	Name        string        `json:"name"`
	State       *int          `json:"state"`
	Priority    *int          `json:"priority"`
	MinimumTime *int          `json:"minimumtime"`
	IsMosaic    bool          `json:"isMosaic"`
	Targets     []SchedTarget `json:"targets"`
	LastImage   *time.Time    `json:"lastImage,omitempty"`
}

const (
	exposureFromPlan     = "plan"
	exposureFromTemplate = "template"
)

func effectiveExposure(plan, template *float64) (*float64, string) {
	if plan != nil && *plan > 0 {
		return plan, exposureFromPlan
	}
	if template != nil {
		return template, exposureFromTemplate
	}
	return nil, ""
}

func intBool(v *int) *bool {
	if v == nil {
		return nil
	}
	b := *v != 0
	return &b
}

func deref[T any](p *T) T {
	var z T
	if p == nil {
		return z
	}
	return *p
}

func fromTSDate(v int64) time.Time {
	if v > ticksThreshold {
		return time.Unix((v-ticksUnixOffset)/ticksPerSecond, 0).UTC()
	}
	return time.Unix(v, 0).UTC()
}

func activeProfile(rows []tsProjectRow) string {
	counts := map[string]int{}
	for _, r := range rows {
		counts[r.ProfileID]++
	}
	best, n := "", -1
	for p, c := range counts {
		if c > n || (c == n && p < best) {
			best, n = p, c
		}
	}
	return best
}

const (
	ProfileFromLastImage    = "last-image"
	ProfileFromMostProjects = "most-projects"
)

func schedProfile(ctx context.Context, db *gorm.DB, rows []tsProjectRow) (string, string, error) {
	has, err := tables.Has(ctx, db, tableAcquired)
	if err != nil {
		return "", "", err
	}
	if has {
		var ids []string
		if err := db.Table(tableAcquired).Select(`"profileId"`).Where(`"profileId" IS NOT NULL AND "profileId" <> ''`).
			Order(`"Id" DESC`).Limit(1).Scan(&ids).Error; err != nil {
			return "", "", err
		}
		if len(ids) == 1 {
			return ids[0], ProfileFromLastImage, nil
		}
	}
	return activeProfile(rows), ProfileFromMostProjects, nil
}

func LoadSchedProjects(ctx context.Context, db *gorm.DB) ([]SchedProject, string, error) {
	out := []SchedProject{}
	if db == nil {
		return out, "", nil
	}
	has, err := tables.Has(ctx, db, tableProject)
	if err != nil {
		return nil, "", err
	}
	if !has {
		return out, "", nil
	}
	db = db.WithContext(ctx)
	var projects []tsProjectRow
	if err := db.Order(`"Id"`).Find(&projects).Error; err != nil {
		return nil, "", err
	}
	profile, basis, err := schedProfile(ctx, db, projects)
	if err != nil {
		return nil, "", err
	}
	var targets []tsTargetRow
	if err := db.Order(`"Id"`).Find(&targets).Error; err != nil {
		return nil, "", err
	}
	var plans []tsPlanRow
	if err := db.Order(`"Id"`).Find(&plans).Error; err != nil {
		return nil, "", err
	}
	var templates []tsTemplateRow
	if err := db.Find(&templates).Error; err != nil {
		return nil, "", err
	}
	oldDefaults, err := tsmigration.OldDefaults(ctx, db)
	if err != nil {
		return nil, "", err
	}
	tmpl := make(map[int]tsTemplateRow, len(templates))
	for _, t := range templates {
		tmpl[t.ID] = t
	}
	plansBy := map[int][]SchedPlan{}
	for _, p := range plans {
		if p.TargetID == nil {
			continue
		}
		sp := SchedPlan{
			ID: p.ID, GUID: p.GUID, PlanExposure: p.Exposure,
			Desired: p.Desired, Acquired: p.Acquired, Accepted: p.Accepted, Enabled: intBool(p.Enabled),
		}
		if p.TemplateID != nil {
			if t, ok := tmpl[*p.TemplateID]; ok {
				sp.Filter, sp.Template, sp.DefaultExposure = &t.FilterName, &t.Name, t.DefaultExposure
			}
		}
		def := sp.DefaultExposure
		if old, ok := oldDefaults[p.ID]; ok {
			def = &old
		}
		sp.Exposure, sp.ExposureSource = effectiveExposure(p.Exposure, def)
		plansBy[*p.TargetID] = append(plansBy[*p.TargetID], sp)
	}
	targetsBy := map[int][]SchedTarget{}
	for _, t := range targets {
		if t.ProjectID == nil {
			continue
		}
		ps := plansBy[t.ID]
		if ps == nil {
			ps = []SchedPlan{}
		}
		targetsBy[*t.ProjectID] = append(targetsBy[*t.ProjectID], SchedTarget{
			ID: t.ID, GUID: t.GUID, Name: t.Name, Active: t.Active != 0,
			RA: t.RA, Dec: t.Dec, Rotation: t.Rotation, Plans: ps,
		})
	}
	lastBy := map[int]time.Time{}
	hasAcquired, err := tables.Has(ctx, db, tableAcquired)
	if err != nil {
		return nil, "", err
	}
	if hasAcquired {
		var lasts []struct {
			ProjectID int   `gorm:"column:project_id"`
			Last      int64 `gorm:"column:last"`
		}
		if err := db.Raw(`SELECT "projectId" AS project_id, max(acquireddate) AS last FROM acquiredimage GROUP BY "projectId"`).Scan(&lasts).Error; err != nil {
			return nil, "", err
		}
		for _, l := range lasts {
			if l.Last > 0 {
				lastBy[l.ProjectID] = fromTSDate(l.Last)
			}
		}
	}
	for _, p := range projects {
		if p.ProfileID != profile {
			continue
		}
		ts := targetsBy[p.ID]
		if ts == nil {
			ts = []SchedTarget{}
		}
		sp := SchedProject{
			ID: p.ID, GUID: p.GUID, Name: p.Name, State: p.State, Priority: p.Priority,
			MinimumTime: p.MinimumTime, IsMosaic: p.IsMosaic != 0, Targets: ts,
		}
		if l, ok := lastBy[p.ID]; ok {
			sp.LastImage = &l
		}
		out = append(out, sp)
	}
	return out, basis, nil
}

type tonightMeta struct {
	FileName            string         `json:"FileName"`
	ExposureDuration    quality.Float  `json:"ExposureDuration"`
	HFR                 quality.Float  `json:"HFR"`
	GuidingRMS          quality.Float  `json:"GuidingRMS"`
	GuidingRMSArcSec    quality.Float  `json:"GuidingRMSArcSec"`
	GuidingRMSRAArcSec  quality.Float  `json:"GuidingRMSRAArcSec"`
	GuidingRMSDECArcSec quality.Float  `json:"GuidingRMSDECArcSec"`
	DetectedStars       *quality.Float `json:"DetectedStars"`
}

type TonightSub struct {
	ID            int        `json:"id"`
	Time          time.Time  `json:"time"`
	ProjectID     int        `json:"project_id"`
	Project       string     `json:"project"`
	TargetID      int        `json:"target_id"`
	Target        string     `json:"target"`
	Filter        string     `json:"filter"`
	Exposure      *float64   `json:"exposure,omitempty"`
	HFR           *float64   `json:"hfr,omitempty"`
	Stars         *float64   `json:"stars,omitempty"`
	GuidingRMS    *float64   `json:"guiding_rms,omitempty"`
	GuidingRMSRA  *float64   `json:"guiding_rms_ra,omitempty"`
	GuidingRMSDec *float64   `json:"guiding_rms_dec,omitempty"`
	Grading       string     `json:"grading"`
	File          string     `json:"file,omitempty"`
	Verdict       string     `json:"verdict,omitempty"`
	Score         *float64   `json:"score,omitempty"`
	Weight        *float64   `json:"weight,omitempty"`
	Gain          *float64   `json:"gain,omitempty"`
	CCDTemp       *float64   `json:"ccd_temp,omitempty"`
	ProcessedAt   *time.Time `json:"processed_at,omitempty"`
	UploadedAt    *time.Time `json:"uploaded_at,omitempty"`
	PreviewURL    string     `json:"preview_url,omitempty"`
	Width         *int       `json:"width"`
	Height        *int       `json:"height"`
	Indexed       bool       `json:"indexed"`
	previewKey    string
}

type TonightSubs struct {
	Since         time.Time                 `json:"since"`
	SinceBasis    string                    `json:"since_basis"`
	Subs          []TonightSub              `json:"subs"`
	Latest        *TonightSub               `json:"latest,omitempty"`
	LatestPreview *TonightSub               `json:"latest_preview,omitempty"`
	Grader        observatory.GraderSummary `json:"grader"`
}

func finite(f quality.Float) *float64 {
	v := float64(f)
	if math.IsNaN(v) || math.IsInf(v, 0) || v == 0 {
		return nil
	}
	return &v
}

func number(f *quality.Float) *float64 {
	if f == nil {
		return nil
	}
	v := float64(*f)
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return &v
}

func gradingName(s int) string {
	switch s {
	case quality.GradingAccepted:
		return "accepted"
	case quality.GradingRejected:
		return "rejected"
	}
	return "pending"
}

type subKey struct {
	object string
	file   string
}

func LoadTonightSubs(ctx context.Context, sched, appDB *gorm.DB, signer *previewer.Signer, since time.Time) (TonightSubs, error) {
	out := TonightSubs{Since: since, Subs: []TonightSub{}}
	if sched == nil {
		return out, nil
	}
	has, err := tables.Has(ctx, sched, tableAcquired)
	if err != nil || !has {
		return out, err
	}
	sinceTicks := since.Unix()*ticksPerSecond + ticksUnixOffset
	rows, err := sched.WithContext(ctx).Table("acquiredimage a").
		Select(`a."Id", coalesce(a."projectId", 0), coalesce(a."targetId", 0), coalesce(a.acquireddate, 0), coalesce(a.filtername, ''), `+
			`coalesce(a."gradingStatus", 0), coalesce(a.metadata, ''), coalesce(t.name, ''), coalesce(p.name, '')`).
		Joins(`LEFT JOIN target t ON t."Id" = a."targetId"`).
		Joins(`LEFT JOIN project p ON p."Id" = a."projectId"`).
		Where(`CAST(a.acquireddate AS BIGINT) >= ? AND (CAST(a.acquireddate AS BIGINT) < ? OR CAST(a.acquireddate AS BIGINT) >= ?)`, since.Unix(), int64(ticksThreshold), sinceTicks).
		Order("a.acquireddate").Rows()
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var s TonightSub
		var date int64
		var grading int
		var meta string
		if err := rows.Scan(&s.ID, &s.ProjectID, &s.TargetID, &date, &s.Filter, &grading, &meta, &s.Target, &s.Project); err != nil {
			return out, err
		}
		s.Time = fromTSDate(date)
		if s.Time.Before(since) {
			continue
		}
		s.Grading = gradingName(grading)
		var m tonightMeta
		if err := json.Unmarshal([]byte(meta), &m); err == nil {
			s.File = m.FileName[strings.LastIndexAny(m.FileName, `\/`)+1:]
			s.Exposure = finite(m.ExposureDuration)
			s.HFR = finite(m.HFR)
			s.Stars = number(m.DetectedStars)
			s.GuidingRMS = finite(m.GuidingRMSArcSec)
			s.GuidingRMSRA = finite(m.GuidingRMSRAArcSec)
			s.GuidingRMSDec = finite(m.GuidingRMSDECArcSec)
		}
		out.Subs = append(out.Subs, s)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	sort.SliceStable(out.Subs, func(i, j int) bool { return out.Subs[i].Time.Before(out.Subs[j].Time) })
	if appDB != nil && len(out.Subs) > 0 {
		if err := linkStacker(ctx, appDB, out.Subs, since); err != nil {
			return out, err
		}
	}
	for i := range out.Subs {
		if out.Subs[i].previewKey == "" || signer == nil {
			continue
		}
		if u, err := signer.URL(ctx, out.Subs[i].previewKey); err == nil {
			out.Subs[i].PreviewURL = u
		}
	}
	if n := len(out.Subs); n > 0 {
		l := out.Subs[n-1]
		out.Latest = &l
	}
	for i := len(out.Subs) - 1; i >= 0; i-- {
		if out.Subs[i].PreviewURL != "" {
			p := out.Subs[i]
			out.LatestPreview = &p
			break
		}
	}
	if out.LatestPreview == nil && appDB != nil && signer != nil {
		p, ok, err := newestPreview(ctx, appDB, signer)
		if err != nil {
			return out, err
		}
		if ok {
			out.LatestPreview = &p
		}
	}
	return out, nil
}

// newestPreview is the newest light with a preview, from any night, for when
// no sub of tonight has one yet. Its ID is 0: it has no acquiredimage row here.
func newestPreview(ctx context.Context, db *gorm.DB, signer *previewer.Signer) (TonightSub, bool, error) {
	if has, err := tables.Has(ctx, db, &app.Frame{}); err != nil || !has {
		return TonightSub{}, false, err
	}
	var f app.Frame
	err := db.WithContext(ctx).Select("key", "last_modified", "object", "filter", "exposure", "gain", "ccd_temp", "width", "height", "date_obs", "preview_key").
		Where("type = ? AND preview_key IS NOT NULL AND date_obs IS NOT NULL", lightType).
		Order("date_obs DESC").Limit(1).Take(&f).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return TonightSub{}, false, nil
	}
	if err != nil {
		return TonightSub{}, false, err
	}
	u, err := signer.URL(ctx, *f.PreviewKey)
	if err != nil {
		return TonightSub{}, false, err
	}
	return TonightSub{
		Time:       f.DateObs.UTC(),
		Target:     f.Object,
		Filter:     f.Filter,
		Exposure:   f.Exposure,
		Gain:       f.Gain,
		CCDTemp:    f.CCDTemp,
		File:       path.Base(f.Key),
		UploadedAt: uploadedAt(f.LastModified),
		PreviewURL: u,
		Width:      f.Width,
		Height:     f.Height,
		Indexed:    true,
	}, true, nil
}

func uploadedAt(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

func linkStacker(ctx context.Context, db *gorm.DB, subs []TonightSub, since time.Time) error {
	objects := map[string]bool{}
	for _, s := range subs {
		if s.Target != "" {
			objects[s.Target] = true
		}
	}
	names := make([]string, 0, len(objects))
	for o := range objects {
		names = append(names, o)
	}
	if len(names) == 0 {
		return nil
	}
	if has, err := tables.Has(ctx, db, &app.Frame{}); err != nil || !has {
		return err
	}
	var rows []struct {
		Key          string
		Object       string
		LastModified time.Time
		Gain         *float64
		CCDTemp      *float64
		Width        *int
		Height       *int
		PreviewKey   *string
		Status       *string
		Score        *float64
		Weight       *float64
		ProcessedAt  *time.Time
	}
	q := db.WithContext(ctx).Table("frames f").
		Select("f.key, f.object, f.last_modified, f.gain, f.ccd_temp, f.width, f.height, f.preview_key, sf.status, sf.score, sf.weight, sf.processed_at").
		Where("f.type = ? AND f.object IN ? AND (f.date_obs IS NULL OR f.date_obs >= ?)", lightType, names, since.Add(-12*time.Hour))
	hasStack, err := tables.Has(ctx, db, &app.StackFrame{})
	if err != nil {
		return err
	}
	if hasStack {
		q = q.Joins("LEFT JOIN stack_frames sf ON sf.frame_id = f.id")
	} else {
		q = q.Select("f.key, f.object, f.last_modified, f.gain, f.ccd_temp, f.width, f.height, f.preview_key")
	}
	if err := q.Scan(&rows).Error; err != nil {
		return err
	}
	by := make(map[subKey]int, len(rows))
	for i, r := range rows {
		by[subKey{r.Object, path.Base(r.Key)}] = i
	}
	for i := range subs {
		j, ok := by[subKey{subs[i].Target, subs[i].File}]
		if !ok || subs[i].File == "" {
			continue
		}
		r := rows[j]
		subs[i].Gain = r.Gain
		subs[i].CCDTemp = r.CCDTemp
		subs[i].Width, subs[i].Height = r.Width, r.Height
		subs[i].previewKey = deref(r.PreviewKey)
		subs[i].UploadedAt = uploadedAt(r.LastModified)
		subs[i].Indexed = true
		if r.Status != nil {
			subs[i].Verdict = *r.Status
			subs[i].Score = r.Score
			subs[i].Weight = r.Weight
			subs[i].ProcessedAt = r.ProcessedAt
		}
	}
	return nil
}

type GraderSource interface {
	Limits(ctx context.Context, targetIDs []int) observatory.GraderSummary
}

func graderFor(ctx context.Context, src GraderSource, subs []TonightSub) observatory.GraderSummary {
	if src == nil {
		return observatory.GraderSummary{State: observatory.GraderUnconfigured, Note: "the scheduler API is not configured", Targets: []observatory.GraderTarget{}}
	}
	ids := make([]int, 0, len(subs))
	for _, s := range subs {
		ids = append(ids, s.TargetID)
	}
	return src.Limits(ctx, ids)
}

func applySchedDataRoutes(g *gin.RouterGroup, signer *previewer.Signer, grader GraderSource, site *siteMemo, now func() time.Time) {
	g.GET("/scheduler/projects", func(c *gin.Context) {
		di, ok := depInjection(c)
		if !ok {
			return
		}
		var db *gorm.DB
		if di.SchedulerDBStore != nil {
			db = di.SchedulerDBStore.DB()
		}
		out, basis, err := LoadSchedProjects(c.Request.Context(), db)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
			return
		}
		if basis != "" {
			c.Header("X-Profile-Basis", basis)
		}
		c.JSON(http.StatusOK, out)
	})
	g.GET("/scheduler/tonight-subs", func(c *gin.Context) {
		di, ok := depInjection(c)
		if !ok {
			return
		}
		var since nightSince
		if s := c.Query("since"); s != "" {
			t, err := time.Parse(time.RFC3339, s)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{errorKey: "since must be RFC3339"})
				return
			}
			since = nightSince{At: t, Basis: "the requested time"}
		} else {
			since = tonightSince(c.Request.Context(), site, now())
		}
		var sched, appDB *gorm.DB
		if di.SchedulerDBStore != nil {
			sched = di.SchedulerDBStore.DB()
		}
		if di.AppStore != nil {
			appDB = di.AppStore.DB()
		}
		out, err := LoadTonightSubs(c.Request.Context(), sched, appDB, signer, since.At.UTC())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
			return
		}
		out.SinceBasis = since.Basis
		out.Grader = graderFor(c.Request.Context(), grader, out.Subs)
		c.JSON(http.StatusOK, out)
	})
}
