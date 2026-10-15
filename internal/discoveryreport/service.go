package discoveryreport

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/discoveryfollowup"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

const (
	unitArcsec = "arcsec"
	colFlux    = "flux"
)

// Spot measurement states.
const (
	SpotNone      = "none"
	SpotQueued    = "queued"
	SpotMeasuring = "measuring"
	SpotDone      = "done"
	SpotFailed    = "failed"
)

const (
	// SpotNights and SpotPerNight bound the forced photometry: the most
	// recent nights before the find, and the best subs of each night and
	// filter.
	SpotNights   = 12
	SpotPerNight = 6
	// MatchArcsec is how close a saved report must be to a candidate.
	MatchArcsec  = 2.0
	tnsSearchAge = time.Hour
)

// SpotMeasurer measures a spot on earlier nights; the discoveries runner
// is one.
type SpotMeasurer interface {
	SpotNights(ctx context.Context, object string, ra, dec float64, opts discoveries.SpotOptions) ([]discoveries.SpotNight, error)
}

// CutoutReader reads a candidate's stored cutouts.
type CutoutReader interface {
	Cutout(ctx context.Context, key string) ([]byte, error)
}

// Searcher asks TNS about a position.
type Searcher func(ctx context.Context, ra, dec, radius float64) ([]string, error)

// Service drafts reports and keeps what they add.
type Service struct {
	DB         *gorm.DB
	Measurer   SpotMeasurer
	Cutouts    CutoutReader
	Search     Searcher
	TNS        TNS
	Settings   Settings
	Instrument func(ctx context.Context, c app.DiscoveryCandidate) string
	Now        func() time.Time

	mu      sync.Mutex
	running bool
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

var errNoReport = errors.New("no saved report")

// ErrNotFound says there is no such candidate.
var ErrNotFound = errors.New("no such candidate")

func (s *Service) load(ctx context.Context, id int) (app.DiscoveryCandidate, app.DiscoveryRun, error) {
	var c app.DiscoveryCandidate
	if err := s.DB.WithContext(ctx).First(&c, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return c, app.DiscoveryRun{}, ErrNotFound
		}
		return c, app.DiscoveryRun{}, err
	}
	var run app.DiscoveryRun
	if err := s.DB.WithContext(ctx).First(&run, c.RunID).Error; err != nil {
		return c, run, err
	}
	return c, run, nil
}

// saved is the report row of a candidate, made when make is set.
func (s *Service) saved(ctx context.Context, c app.DiscoveryCandidate, create bool) (*app.DiscoveryReport, error) {
	var rows []app.DiscoveryReport
	if err := s.DB.WithContext(ctx).Where("object = ? AND night = ? AND class = ?", c.Object, c.Night, c.Class).Find(&rows).Error; err != nil {
		return nil, err
	}
	for i, r := range rows {
		if discoveries.Separation(r.RA, r.Dec, c.RA, c.Dec)*3600 <= MatchArcsec {
			return &rows[i], nil
		}
	}
	if !create {
		return nil, errNoReport
	}
	r := app.DiscoveryReport{Object: c.Object, Night: c.Night, Class: c.Class, RA: c.RA, Dec: c.Dec, SpotState: SpotNone}
	if err := s.DB.WithContext(ctx).Create(&r).Error; err != nil {
		return nil, err
	}
	return &r, nil
}

// Report drafts a candidate's report, searching TNS first when a key is
// set and the last search is old.
func (s *Service) Report(ctx context.Context, id int) (Report, error) {
	c, run, err := s.load(ctx, id)
	if err != nil {
		return Report{}, err
	}
	saved, err := s.saved(ctx, c, s.Search != nil)
	if errors.Is(err, errNoReport) {
		saved, err = nil, nil
	}
	if err != nil {
		return Report{}, err
	}
	if s.Search != nil && saved != nil && c.Kind == app.DiscoveryKindChange && (saved.TNSAt == nil || s.now().Sub(*saved.TNSAt) > tnsSearchAge) {
		s.search(ctx, c, saved)
	}
	in := Input{Cand: c, Run: run, Saved: saved, Settings: s.Settings}
	in.Settings.TNSKey = s.TNS.Enabled()
	if f, err := discoveryfollowup.ForCandidate(ctx, s.DB, c); err == nil {
		in.Followup = f
	}
	if saved != nil {
		if saved.Spot != "" {
			_ = json.Unmarshal([]byte(saved.Spot), &in.Spot)
		}
		if saved.TNSFound != nil {
			in.TNSFound = []string{}
			_ = json.Unmarshal([]byte(*saved.TNSFound), &in.TNSFound)
		}
		in.TNSError = saved.TNSError
	}
	if s.Instrument != nil {
		in.Instrument = s.Instrument(ctx, c)
	}
	return Build(in), nil
}

func (s *Service) search(ctx context.Context, c app.DiscoveryCandidate, saved *app.DiscoveryReport) {
	sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	found, err := s.Search(sctx, c.RA, c.Dec, SearchRadius)
	now := s.now()
	fields := map[string]any{"tns_at": now}
	if err != nil {
		fields["tns_error"] = err.Error()
		saved.TNSError = err.Error()
	} else {
		if found == nil {
			found = []string{}
		}
		b, err := json.Marshal(found)
		if err != nil {
			return
		}
		v := string(b)
		fields["tns_found"], fields["tns_error"] = v, ""
		saved.TNSFound, saved.TNSError = &v, ""
	}
	saved.TNSAt = &now
	if err := s.DB.WithContext(ctx).Model(&app.DiscoveryReport{}).Where("id = ?", saved.ID).Updates(fields).Error; err != nil {
		slog.Warn("Could not save the TNS search", "error", err)
	}
}

// Measure queues the forced photometry on earlier nights of a candidate's
// place, unless it is done or under way, and starts the worker.
func (s *Service) Measure(ctx context.Context, id int, again bool) (Spot, error) {
	if s.Measurer == nil {
		return Spot{State: SpotFailed, Error: "the night comparisons are off in this stacker"}, nil
	}
	c, _, err := s.load(ctx, id)
	if err != nil {
		return Spot{}, err
	}
	saved, err := s.saved(ctx, c, true)
	if err != nil {
		return Spot{}, err
	}
	switch saved.SpotState {
	case SpotQueued, SpotMeasuring:
	case SpotDone, SpotFailed:
		if !again {
			break
		}
		fallthrough
	default:
		if err := s.DB.WithContext(ctx).Model(&app.DiscoveryReport{}).Where("id = ?", saved.ID).Updates(map[string]any{"spot_state": SpotQueued, "spot_error": ""}).Error; err != nil {
			return Spot{}, err
		}
		saved.SpotState = SpotQueued
	}
	s.kick(context.WithoutCancel(ctx))
	return Spot{State: saved.SpotState, Error: saved.SpotError, MeasuredAt: saved.SpotAt}, nil
}

func (s *Service) kick(ctx context.Context) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.mu.Unlock()
	go func() {
		defer func() {
			s.mu.Lock()
			s.running = false
			s.mu.Unlock()
		}()
		for s.MeasureNext(ctx) {
		}
	}()
}

// Resume restarts measurements a restart left queued or half done.
func (s *Service) Resume(ctx context.Context) {
	if s.Measurer == nil {
		return
	}
	s.DB.WithContext(ctx).Model(&app.DiscoveryReport{}).Where("spot_state = ?", SpotMeasuring).Update("spot_state", SpotQueued)
	s.kick(context.WithoutCancel(ctx))
}

// MeasureNext measures the oldest queued spot; false when none is queued.
func (s *Service) MeasureNext(ctx context.Context) bool {
	var r app.DiscoveryReport
	if err := s.DB.WithContext(ctx).Where("spot_state = ?", SpotQueued).Order("updated_at").First(&r).Error; err != nil {
		return false
	}
	res := s.DB.WithContext(ctx).Model(&app.DiscoveryReport{}).Where("id = ? AND spot_state = ?", r.ID, SpotQueued).Update("spot_state", SpotMeasuring)
	if res.Error != nil || res.RowsAffected == 0 {
		return res.Error == nil
	}
	start := s.now()
	spots, err := s.Measurer.SpotNights(ctx, r.Object, r.RA, r.Dec, discoveries.SpotOptions{Nights: SpotNights, PerNight: SpotPerNight, Before: r.Night})
	now := s.now()
	fields := map[string]any{"spot_at": now}
	if err != nil {
		fields["spot_state"], fields["spot_error"] = SpotFailed, err.Error()
		slog.Warn("Measuring a find's earlier nights failed", "object", r.Object, "night", r.Night.Format(time.DateOnly), "error", err)
	} else {
		if spots == nil {
			spots = []discoveries.SpotNight{}
		}
		b, err := json.Marshal(spots)
		if err != nil {
			fields["spot_state"], fields["spot_error"] = SpotFailed, err.Error()
		} else {
			fields["spot_state"], fields["spot"], fields["spot_error"] = SpotDone, string(b), ""
		}
		slog.Info("Measured a find's earlier nights", "object", r.Object, "night", r.Night.Format(time.DateOnly), "nights", len(spots), "took", now.Sub(start).Round(time.Second))
	}
	if err := s.DB.WithContext(context.WithoutCancel(ctx)).Model(&app.DiscoveryReport{}).Where("id = ?", r.ID).Updates(fields).Error; err != nil {
		slog.Error("Could not save a find's earlier nights", "error", err)
	}
	return true
}

// SetName stores the name TNS gave a find.
func (s *Service) SetName(ctx context.Context, id int, name string) error {
	name = strings.Join(strings.Fields(name), " ")
	if len(name) > 40 {
		return fmt.Errorf("%w: a TNS name is at most 40 characters", ErrInvalid)
	}
	c, _, err := s.load(ctx, id)
	if err != nil {
		return err
	}
	saved, err := s.saved(ctx, c, true)
	if err != nil {
		return err
	}
	return s.DB.WithContext(ctx).Model(&app.DiscoveryReport{}).Where("id = ?", saved.ID).Update("tns_name", name).Error
}

// ErrInvalid is a request that can't be done as asked.
var ErrInvalid = errors.New("invalid")

// SendTNS sends the find's AT report to TNS. It is only ever called on the
// report page's explicit Send.
func (s *Service) SendTNS(ctx context.Context, id int) (Reply, error) {
	rep, err := s.Report(ctx, id)
	if err != nil {
		return Reply{}, err
	}
	if !rep.TNS.CanSend {
		return Reply{}, fmt.Errorf("%w: %s", ErrInvalid, rep.TNS.SendReason)
	}
	c, run, err := s.load(ctx, id)
	if err != nil {
		return Reply{}, err
	}
	vals, err := s.TNS.Values(ctx)
	if err != nil {
		return Reply{}, err
	}
	at, err := ATReport(c, run, s.spots(ctx, c), s.Settings, vals, s.nightExposure(ctx, c, run))
	if err != nil {
		return Reply{}, err
	}
	saved, err := s.saved(ctx, c, true)
	if err != nil {
		return Reply{}, err
	}
	body, err := json.Marshal(at)
	if err != nil {
		return Reply{}, err
	}
	now := s.now()
	claim := s.DB.WithContext(ctx).Model(&app.DiscoveryReport{}).Where("id = ? AND sent_at IS NULL", saved.ID).
		Updates(map[string]any{"sent_at": now, "sent_report": string(body)})
	if claim.Error != nil {
		return Reply{}, claim.Error
	}
	if claim.RowsAffected == 0 {
		return Reply{}, fmt.Errorf("%w: already sent", ErrInvalid)
	}
	reply, err := s.TNS.Send(ctx, at)
	fields := map[string]any{"sent_reply_id": reply.ReportID, "sent_reply": string(reply.Raw)}
	if reply.Name != "" {
		fields["tns_name"] = reply.Name
	}
	if err != nil {
		fields["sent_reply"] = err.Error()
		if reply.ReportID == "" && !errors.Is(err, context.DeadlineExceeded) {
			fields["sent_at"] = nil
		}
	}
	if uerr := s.DB.WithContext(context.WithoutCancel(ctx)).Model(&app.DiscoveryReport{}).Where("id = ?", saved.ID).Updates(fields).Error; uerr != nil {
		slog.Error("Could not record a TNS report", "error", uerr)
	}
	return reply, err
}

func (s *Service) spots(ctx context.Context, c app.DiscoveryCandidate) []discoveries.SpotNight {
	saved, err := s.saved(ctx, c, false)
	if err != nil || saved.Spot == "" {
		return nil
	}
	var out []discoveries.SpotNight
	_ = json.Unmarshal([]byte(saved.Spot), &out)
	return out
}

// ATReport is the TNS bulk AT report of a find, with ids from vals.
func ATReport(c app.DiscoveryCandidate, run app.DiscoveryRun, spot []discoveries.SpotNight, st Settings, vals Values, exposure float64) (map[string]any, error) {
	unc, ok := uncertainty(c, run)
	if !ok {
		return nil, fmt.Errorf("%w: the position's uncertainty is not measured", ErrInvalid)
	}
	p, ok := discovery(c)
	mag := discoveries.NewLight(c)
	if !ok || mag == nil {
		return nil, fmt.Errorf("%w: the discovery time or magnitude is not measured", ErrInvalid)
	}
	nd, ok := lastNonDetection(spot, mag)
	if !ok {
		return nil, fmt.Errorf("%w: the last non-detection is not measured", ErrInvalid)
	}
	_, atName := typeGuess(c)
	atType, ok := vals.ID(atName, "at_types", "at_type", "objtypes")
	if !ok {
		return nil, fmt.Errorf("%w: TNS has no at type %s", ErrInvalid, atName)
	}
	clearID, ok := vals.ID("Clear-", "filters", "filter")
	if !ok {
		return nil, fmt.Errorf("%w: TNS has no Clear- filter", ErrInvalid)
	}
	unit, ok := vals.ID("ABMag", "units", "flux_units", "unit")
	if !ok {
		return nil, fmt.Errorf("%w: TNS has no ABMag unit", ErrInvalid)
	}
	ndFilter := clearID
	if discoveries.GroupOf(nd.Filter) != discoveries.GroupBroadband {
		return nil, fmt.Errorf("%w: the last non-detection is narrowband", ErrInvalid)
	}
	e := magErr(c)
	flux := map[string]any{"obsdate": p.Time.UTC().Format("2006-01-02 15:04:05"), colFlux: fmt.Sprintf("%.2f", *mag), "flux_error": fmt.Sprintf("%.2f", *e),
		"limiting_flux": "", "flux_unitid": unit, "filterid": clearID, "instrumentid": st.TNSInstrumentID, "exptime": fmt.Sprintf("%.0f", exposure),
		"observer": st.Reporter, "comments": "Night stack of " + itoa(run.NightSubs) + " subs"}
	entry := map[string]any{
		"ra":                  map[string]any{"value": RAText(c.RA), "error": fmt.Sprintf("%.2f", unc), "units": unitArcsec},
		"dec":                 map[string]any{"value": DecText(c.Dec), "error": fmt.Sprintf("%.2f", unc), "units": unitArcsec},
		"reporting_groupid":   st.TNSGroupID,
		"data_source_groupid": st.TNSGroupID,
		"reporter":            st.Reporter,
		"discovery_datetime":  p.Time.UTC().Format("2006-01-02 15:04:05"),
		"at_type":             atType,
		"remarks":             "",
		"non_detection": map[string]any{"obsdate": nd.Mid.UTC().Format("2006-01-02 15:04:05"), "limiting_flux": fmt.Sprintf("%.2f", *nd.Limit),
			"flux_unitid": unit, "filterid": ndFilter, "instrumentid": st.TNSInstrumentID, "exptime": fmt.Sprintf("%.0f", nd.Exposure),
			"observer": st.Reporter, "comments": "Forced photometry, 3 sigma"},
		"photometry": map[string]any{"0": flux},
	}
	if h, ok := host(c); ok {
		entry["host_name"] = h.Name
	}
	return map[string]any{"at_report": map[string]any{"0": entry}}, nil
}

// nightExposure is the total exposure, in seconds, of the find's night's
// lights in its filters.
func (s *Service) nightExposure(ctx context.Context, c app.DiscoveryCandidate, run app.DiscoveryRun) float64 {
	var filters []string
	for _, f := range strings.Split(run.Filters, ",") {
		if f = strings.TrimSpace(f); f != "" {
			filters = append(filters, f)
		}
	}
	var total float64
	if err := s.DB.WithContext(ctx).Model(&app.Frame{}).Select("COALESCE(SUM(exposure), 0)").
		Where("type = ? AND object = ? AND night = ? AND filter IN ?", "LIGHT", c.Object, c.Night, filters).Scan(&total).Error; err != nil {
		return 0
	}
	return total
}

// Bundle is a zip of the find's cutouts, its plate-solved stamp, its
// per-sub light curve and the earlier nights at its place.
func (s *Service) Bundle(ctx context.Context, id int) (string, []byte, error) {
	c, _, err := s.load(ctx, id)
	if err != nil {
		return "", nil, err
	}
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	if c.CutoutKey != "" && s.Cutouts != nil {
		for _, n := range []string{"night.png", "ref.png", "diff.png", "stamp.fits"} {
			b, err := s.Cutouts.Cutout(ctx, c.CutoutKey+n)
			if err != nil {
				continue
			}
			w, err := z.Create(n)
			if err != nil {
				return "", nil, err
			}
			if _, err := w.Write(b); err != nil {
				return "", nil, err
			}
		}
	}
	if err := writeCSV(z, "light-curve.csv", []string{"time_utc", "filter", colFlux, "flux_err", "mag", "mag_err"}, lightRows(c)); err != nil {
		return "", nil, err
	}
	if err := writeCSV(z, "earlier-nights.csv", []string{"night", "filter", "subs", "mid_utc", colFlux, "flux_err", "snr", "detected", "mag", "mag_err", "limit_3sigma"},
		spotRows(s.spots(ctx, c))); err != nil {
		return "", nil, err
	}
	if err := z.Close(); err != nil {
		return "", nil, err
	}
	name := fmt.Sprintf("%s-%s-%d.zip", strings.ReplaceAll(strings.ToLower(c.Object), " ", "-"), c.Night.Format(time.DateOnly), c.ID)
	return name, buf.Bytes(), nil
}

func opt(v *float64) string {
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%.3f", *v)
}

func lightRows(c app.DiscoveryCandidate) [][]string {
	ls := light(c)
	rows := make([][]string, 0, len(ls))
	for _, p := range ls {
		rows = append(rows, []string{p.Time.UTC().Format(time.RFC3339), p.Filter, fmt.Sprintf("%g", p.Flux), fmt.Sprintf("%g", p.FluxErr), opt(p.Mag), opt(p.MagErr)})
	}
	return rows
}

func spotRows(spot []discoveries.SpotNight) [][]string {
	rows := make([][]string, 0, len(spot))
	for _, s := range spot {
		rows = append(rows, []string{s.Night.Format(time.DateOnly), s.Filter, itoa(s.Subs), s.Mid.UTC().Format(time.RFC3339), fmt.Sprintf("%g", s.Flux),
			fmt.Sprintf("%g", s.FluxErr), fmt.Sprintf("%.2f", s.SNR), fmt.Sprint(s.Detected), opt(s.Mag), opt(s.MagErr), opt(s.Limit)})
	}
	return rows
}

func writeCSV(z *zip.Writer, name string, head []string, rows [][]string) error {
	w, err := z.Create(name)
	if err != nil {
		return err
	}
	cw := csv.NewWriter(w)
	if err := cw.Write(head); err != nil {
		return err
	}
	if err := cw.WriteAll(rows); err != nil {
		return err
	}
	cw.Flush()
	return cw.Error()
}

// InstrumentFromFrames names the telescope and camera most of a find's
// night's lights were taken with, from their headers, and the focal length.
func InstrumentFromFrames(db *gorm.DB) func(ctx context.Context, c app.DiscoveryCandidate) string {
	return func(ctx context.Context, c app.DiscoveryCandidate) string {
		var rows []struct {
			Telescope   *string
			Camera      string
			FocalLength *float64
			N           int
		}
		if err := db.WithContext(ctx).Model(&app.Frame{}).Select("telescope, camera, focal_length, COUNT(*) AS n").
			Where("type = ? AND object = ? AND night = ?", "LIGHT", c.Object, c.Night).Group("telescope, camera, focal_length").Order("n DESC").Limit(1).
			Find(&rows).Error; err != nil || len(rows) == 0 {
			return ""
		}
		r := rows[0]
		var parts []string
		if r.Telescope != nil && strings.TrimSpace(*r.Telescope) != "" {
			t := strings.TrimSpace(*r.Telescope)
			if r.FocalLength != nil && *r.FocalLength > 0 {
				t += fmt.Sprintf(" (%.0f mm)", *r.FocalLength)
			}
			parts = append(parts, t)
		} else if r.FocalLength != nil && *r.FocalLength > 0 {
			parts = append(parts, fmt.Sprintf("%.0f mm telescope", *r.FocalLength))
		}
		if strings.TrimSpace(r.Camera) != "" {
			parts = append(parts, strings.TrimSpace(r.Camera))
		}
		return strings.Join(parts, ", ")
	}
}
