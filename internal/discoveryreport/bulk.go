package discoveryreport

import (
	"fmt"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

const (
	forcedComment = "Forced photometry, 3 sigma"
	keyComments   = "comments"
	keyError      = "error"
	keyFluxUnit   = "flux_unitid"
)

// tnsIDs are the TNS value ids a report uses.
type tnsIDs struct {
	at, clear, unit string
}

func idsFor(vals Values, atName string) (tnsIDs, error) {
	var ids tnsIDs
	var ok bool
	if ids.at, ok = vals.ID(atName, "at_types", "at_type", "objtypes"); !ok {
		return ids, fmt.Errorf("%w: TNS has no at type %s", ErrInvalid, atName)
	}
	if ids.clear, ok = vals.ID("Clear-", "filters", "filter"); !ok {
		return ids, fmt.Errorf("%w: TNS has no Clear- filter", ErrInvalid)
	}
	if ids.unit, ok = vals.ID("ABMag", "units", "flux_units", "unit"); !ok {
		return ids, fmt.Errorf("%w: TNS has no ABMag unit", ErrInvalid)
	}
	return ids, nil
}

// tnsFlux is a photometry entry or a non-detection: a magnitude with its
// error, or a limit.
type tnsFlux struct {
	Obs      time.Time
	Mag, Err *float64
	Limit    *float64
	Exposure float64
	Comments string
}

func mag2(v *float64) string {
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%.2f", *v)
}

func (f tnsFlux) common(ids tnsIDs, st Settings) map[string]any {
	return map[string]any{"obsdate": f.Obs.UTC().Format(tnsTimeLayout), "limiting_flux": mag2(f.Limit), keyFluxUnit: ids.unit, "filterid": ids.clear,
		"instrumentid": st.TNSInstrumentID, "exptime": fmt.Sprintf("%.0f", f.Exposure), "observer": st.Reporter, keyComments: f.Comments}
}

func (f tnsFlux) photometry(ids tnsIDs, st Settings) map[string]any {
	e := f.common(ids, st)
	e[colFlux], e["flux_error"] = mag2(f.Mag), mag2(f.Err)
	return e
}

func position(text string, unc float64) map[string]any {
	return map[string]any{"value": text, keyError: fmt.Sprintf("%.2f", unc), "units": unitArcsec}
}

// atEntry is one entry of a bulk at_report.
func atEntry(c app.DiscoveryCandidate, unc float64, st Settings, ids tnsIDs, discovered time.Time, remarks string, nd tnsFlux, phot []tnsFlux) map[string]any {
	ph := make(map[string]any, len(phot))
	for i, p := range phot {
		ph[itoa(i)] = p.photometry(ids, st)
	}
	entry := map[string]any{
		"ra":                  position(RAText(c.RA), unc),
		"dec":                 position(DecText(c.Dec), unc),
		"reporting_groupid":   st.TNSGroupID,
		"data_source_groupid": st.TNSGroupID,
		"reporter":            st.Reporter,
		"discovery_datetime":  discovered.UTC().Format(tnsTimeLayout),
		"at_type":             ids.at,
		"remarks":             remarks,
		"non_detection":       nd.common(ids, st),
		"photometry":          ph,
	}
	if h, ok := host(c); ok {
		entry["host_name"] = h.Name
	}
	return entry
}
