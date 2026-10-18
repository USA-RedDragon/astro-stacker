package surveys

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

// TESSRequestPrefix is where TESS requests wait for the TESS job.
const TESSRequestPrefix = "vetting/tess/requests/"

// TESSClaimPrefix is where a TESS worker claims the request of the same
// name while it fits that star.
const TESSClaimPrefix = "vetting/tess/claims/"

// TESSClaim is one worker's lease on a request. It holds until Expires, in
// Unix seconds; the worker renews it while it works.
type TESSClaim struct {
	GaiaID    GaiaID    `json:"gaia_id"`
	Worker    string    `json:"worker"`
	ClaimedAt time.Time `json:"claimed_at"`
	Expires   float64   `json:"expires"`
}

// Live is whether the claim still holds at now.
func (c TESSClaim) Live(now time.Time) bool {
	return float64(now.UnixNano())/1e9 < c.Expires
}

// TESSRequest asks the TESS job to fit the star and its neighbours in
// every TESS sector.
type TESSRequest struct {
	GaiaID      int64       `json:"gaia_id"`
	RA          float64     `json:"ra"`
	Dec         float64     `json:"dec"`
	PMRA        float64     `json:"pmra"`
	PMDec       float64     `json:"pmdec"`
	G           float64     `json:"g"`
	Neighbours  []Neighbour `json:"neighbours"`
	Field       Field       `json:"field"`
	RequestedAt time.Time   `json:"requested_at"`
}

// GaiaID is a Gaia source id that reads from a JSON number or string.
type GaiaID int64

// UnmarshalJSON takes 123 or "123".
func (g *GaiaID) UnmarshalJSON(b []byte) error {
	v, err := strconv.ParseInt(strings.Trim(string(b), `"`), 10, 64)
	if err != nil {
		return fmt.Errorf("gaia_id %s: %w", b, err)
	}
	*g = GaiaID(v)
	return nil
}

// TESSStatus is the TESS job's progress on one star.
type TESSStatus struct {
	GaiaID           GaiaID            `json:"gaia_id"`
	SectorsAvailable []int             `json:"sectors_available"`
	SectorsDone      []int             `json:"sectors_done"`
	SectorsFailed    map[string]string `json:"sectors_failed"`
	UpdatedAt        time.Time         `json:"updated_at"`
}

// tessDir is where the TESS job writes a star's results. The job's
// layout is fixed under DefaultPrefix, whatever Prefix says.
func tessDir(field Field, t Target) string {
	return fmt.Sprintf("%s%s/tess/%d/", DefaultPrefix, FieldSlug(field), t.GaiaID)
}

func (f *Fetcher) tess(ctx context.Context, field Field, t Target) Fetch {
	if t.GaiaID == 0 {
		return failed(lc.TESS, "the TESS job needs a Gaia source")
	}
	b, err := f.Store.Get(ctx, tessDir(field, t)+"status.json")
	if isNotFound(err) {
		if err := f.tessRequest(ctx, field, t); err != nil {
			return failed(lc.TESS, "could not queue the star for the TESS job: %v", err)
		}
		return Fetch{Survey: lc.TESS, State: Pending, Note: "queued for the TESS job"}
	}
	if err != nil {
		return failed(lc.TESS, "could not read the TESS status: %v", err)
	}
	var st TESSStatus
	if err := json.Unmarshal(b, &st); err != nil {
		return failed(lc.TESS, "damaged TESS status: %v", err)
	}
	if len(st.SectorsAvailable) == 0 {
		return Fetch{Survey: lc.TESS, State: Uncovered, Note: "TESS has not observed the star"}
	}
	fe := Fetch{Survey: lc.TESS, State: Done}
	done := append([]int(nil), st.SectorsDone...)
	sort.Ints(done)
	var notes []string
	var missingFiles []int
	for _, s := range done {
		ser, err := f.tessSector(ctx, field, t, s)
		if isNotFound(err) {
			missingFiles = append(missingFiles, s)
			continue
		}
		if err != nil {
			return failed(lc.TESS, "could not read TESS sector %d: %v", s, err)
		}
		fe.Sectors = append(fe.Sectors, s)
		if len(ser.Points) > 0 {
			fe.Series = append(fe.Series, ser)
		}
	}
	for _, s := range missingFiles {
		notes = append(notes, fmt.Sprintf("sector %d is marked done but its light curve is missing", s))
	}
	missing := 0
	for _, s := range st.SectorsAvailable {
		if !containsInt(st.SectorsDone, s) {
			if _, ok := st.SectorsFailed[strconv.Itoa(s)]; !ok {
				missing++
			}
		}
	}
	failedKeys := make([]string, 0, len(st.SectorsFailed))
	for k := range st.SectorsFailed {
		failedKeys = append(failedKeys, k)
	}
	sort.Strings(failedKeys)
	for _, k := range failedKeys {
		notes = append(notes, fmt.Sprintf("sector %s failed: %s", k, st.SectorsFailed[k]))
	}
	missing += len(missingFiles)
	if missing > 0 {
		fe.State = Pending
		notes = append([]string{fmt.Sprintf("%d of %d sectors still with the TESS job", missing, len(st.SectorsAvailable))}, notes...)
		if err := f.tessRequest(ctx, field, t); err != nil {
			notes = append(notes, fmt.Sprintf("could not queue the star again: %v", err))
		}
	}
	if fe.State == Done && len(fe.Sectors) == 0 {
		fe.State = Failed
	}
	fe.Note = strings.Join(notes, "; ")
	return fe
}

func containsInt(s []int, v int) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func (f *Fetcher) tessRequest(ctx context.Context, field Field, t Target) error {
	key := fmt.Sprintf("%s%d.json", TESSRequestPrefix, t.GaiaID)
	_, err := f.Store.Get(ctx, key)
	if err == nil {
		return nil
	}
	if !isNotFound(err) {
		return err
	}
	nb := t.Neighbours
	if nb == nil {
		nb = []Neighbour{}
	}
	field.Slug = FieldSlug(field)
	raw, err := json.Marshal(TESSRequest{
		GaiaID: t.GaiaID, RA: t.RA, Dec: t.Dec, PMRA: t.PMRA, PMDec: t.PMDec, G: t.G,
		Neighbours: nb, Field: field, RequestedAt: f.now().UTC(),
	})
	if err != nil {
		return err
	}
	return f.Store.Put(ctx, key, raw, "application/json")
}

func (f *Fetcher) tessSector(ctx context.Context, field Field, t Target, sector int) (lc.Series, error) {
	ser := lc.Series{Survey: lc.TESS, Band: fmt.Sprintf("S%d", sector), Relative: true, Dilution: 1, Deblended: true}
	raw, err := f.getZ(ctx, fmt.Sprintf("%ss%04d.csv.zst", tessDir(field, t), sector))
	if err != nil {
		return ser, err
	}
	pts, err := parseTESS(raw)
	ser.Points = pts
	return ser, err
}

// parseTESS reads one sector's light curve, written by the TESS job, as
// magnitudes relative to the median flux.
func parseTESS(b []byte) ([]lc.Point, error) {
	cr := csv.NewReader(bytes.NewReader(b))
	head, err := cr.Read()
	if err != nil {
		return nil, err
	}
	col := map[string]int{}
	for i, h := range head {
		col[strings.TrimSpace(h)] = i
	}
	for _, c := range []string{"bjd", "flux", "flux_err", "quality"} {
		if _, ok := col[c]; !ok {
			return nil, fmt.Errorf("no %s column", c)
		}
	}
	var out []lc.Point
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		num := func(c string) float64 {
			v, err := strconv.ParseFloat(strings.TrimSpace(rec[col[c]]), 64)
			if err != nil {
				return math.NaN()
			}
			return v
		}
		q, bjd, fl, fe := num("quality"), num("bjd"), num("flux"), num("flux_err")
		if q != 0 || math.IsNaN(bjd) || !(fl > 0) || math.IsInf(fl, 0) || math.IsNaN(fe) || fe < 0 {
			continue
		}
		out = append(out, lc.Point{BJD: bjd, Mag: -2.5 * math.Log10(fl), Err: fluxToMagErr * fe / fl})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].BJD < out[j].BJD })
	return out, nil
}
