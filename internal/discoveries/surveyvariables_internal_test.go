package discoveries

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/starcat"
)

// surveyVizier answers like VizieR's copies of the survey catalogues, one
// star each at (10, 41) and an ATLAS dubious candidate; a table down says
// fails.
func surveyVizier(t *testing.T, down func(table string) bool) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var asked atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		src := r.URL.Query().Get("-source")
		if down(src) {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		switch src {
		case "I/358/vclassre":
			fmt.Fprint(w, "Source\tRA_ICRS\tDE_ICRS\tClass\n \tdeg\t\t\n----\t----\t----\t----\n42\t10.0\t+41.0\tECL\n")
		case "J/AJ/156/241/table4":
			fmt.Fprint(w, "ATOID\tRAJ2000\tDEJ2000\tClass\tvf-o-med\n----\t----\t----\t----\t----\n"+
				"J010.0000+41.0000\t10.0\t41.0\tMIRA\t12.5\nJ010.0001+41.0000\t10.0001\t41.0\tdubious\t14.0\n")
		case "J/ApJS/249/18/table2":
			fmt.Fprint(w, "ID\tRAJ2000\tDEJ2000\tType\tPer\trmag\trAmp\n----\t----\t----\t----\t----\t----\t----\nZTFJ004000.00+410000.0\t10.0\t41.0\tEW\t0.31\t15.2\t0.4\n")
		case "II/366/catv2021":
			fmt.Fprint(w, "ASASSN-V\tRAJ2000\tDEJ2000\tType\tPer\tVmag\tAmp\n----\t----\t----\t----\t----\t----\t----\nJ004000.00+410000.0\t10.0\t41.0\tSR\t\t13.1\t0.6\n")
		default:
			http.Error(w, "unknown", http.StatusBadRequest)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &asked
}

const asassnName = "ASASSN-V J004000.00+410000.0"

func byCatalog(vs []Variable) map[string][]Variable {
	out := map[string][]Variable{}
	for _, v := range vs {
		out[v.Catalog] = append(out[v.Catalog], v)
	}
	return out
}

func TestSurveyVariablesFromVizieRWhileTheMirrorsCannotAnswer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	srv, _ := surveyVizier(t, func(s string) bool { return s == "J/ApJS/249/18/table2" })
	r := New(nil, nil, &memStore{m: map[string][]byte{}}, &Catalogs{VizierURL: srv.URL}, Options{SurveyVariables: NewSurveyVariables(&starcat.MemBucket{})})
	var k Known
	r.surveyVariables(ctx, "M 31", 10, 41, 0.5, &k)
	if !slices.Equal(k.Checked, []string{catalogGaiaVari, catalogATLAS, catalogASASSN}) || !slices.Equal(k.Failed, []string{catalogZTFVar}) {
		t.Fatalf("checked %v failed %v", k.Checked, k.Failed)
	}
	by := byCatalog(k.Variables)
	want := map[string]Variable{
		catalogGaiaVari: {Catalog: catalogGaiaVari, Name: "Gaia DR3 42", Type: "ECL", RA: 10, Dec: 41},
		catalogATLAS:    {Catalog: catalogATLAS, Name: "ATO J010.0000+41.0000", Type: "MIRA", RA: 10, Dec: 41, Max: 12.5, Band: "o"},
		catalogASASSN:   {Catalog: catalogASASSN, Name: asassnName, Type: "SR", RA: 10, Dec: 41, Max: 13.1, Band: "V", Min: 0.6, Amplitude: true},
	}
	for c, w := range want {
		if len(by[c]) != 1 || by[c][0] != w {
			t.Errorf("%s: %+v, want %+v", c, by[c], w)
		}
	}
}

func TestSurveyVariablesFromTheMirrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	srv, asked := surveyVizier(t, func(string) bool { return false })
	b := &starcat.MemBucket{}
	nan := math.NaN()
	n32 := float32(nan)
	rows := map[*starcat.VarCatalog][]starcat.VarStar{
		starcat.GaiaVariability(): {{ID: 42, RA: 10, Dec: 41, Type: "ECL", Period: nan, Mag: n32, Amp: n32, Score: 0.9}},
		starcat.ATLASVariables(): {{ID: 7, Name: "J010.0000+41.0000", RA: 10, Dec: 41, Type: "MIRA", Period: nan, Mag: 12.5, Amp: n32, Score: n32},
			{ID: 8, Name: "J010.0001+41.0000", RA: 10.0001, Dec: 41, Type: "dubious", Period: nan, Mag: 14, Amp: n32, Score: n32}},
		starcat.ZTFVariables(): {{ID: 1, Name: "ZTFJ004000.00+410000.0", RA: 10, Dec: 41, Type: "EW", Period: 0.31, Mag: 15.2, Amp: 0.4, Score: n32},
			{ID: 2, Name: "ZTFJ050000.00+410000.0", RA: 75, Dec: 41, Type: "EW", Period: 0.5, Mag: 15, Amp: 0.4, Score: n32}},
		starcat.ASASSNVariables(): {{ID: 3, Name: asassnName, RA: 10, Dec: 41, Type: "SR", Period: nan, Mag: 13.1, Amp: 0.6, Score: 1}},
	}
	for c, rs := range rows {
		if err := starcat.WriteVariables(ctx, b, c.Prefix, c, rs); err != nil {
			t.Fatal(err)
		}
	}
	r := New(nil, nil, &memStore{m: map[string][]byte{}}, &Catalogs{VizierURL: srv.URL}, Options{SurveyVariables: NewSurveyVariables(b)})
	var k Known
	r.surveyVariables(ctx, "M 31", 10, 41, 0.5, &k)
	if !slices.Equal(k.Checked, surveyCatalogNames()) || len(k.Failed) != 0 || asked.Load() != 0 {
		t.Fatalf("checked %v failed %v, VizieR asked %d times", k.Checked, k.Failed, asked.Load())
	}
	by := byCatalog(k.Variables)
	if len(k.Variables) != 4 || by[catalogGaiaVari][0].Name != "Gaia DR3 42" || by[catalogATLAS][0].Name != "ATO J010.0000+41.0000" ||
		by[catalogZTFVar][0] != (Variable{Catalog: catalogZTFVar, Name: "ZTFJ004000.00+410000.0", Type: "EW", RA: 10, Dec: 41, Max: 15.2, Band: "r", Min: 0.4, Amplitude: true, Period: 0.31}) ||
		by[catalogASASSN][0].Name != asassnName || by[catalogASASSN][0].Period != 0 {
		t.Fatalf("%+v", k.Variables)
	}
}

func TestSurveyVariableMakesAChangeKnownWithin2Arcsec(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, c := range surveyCatalogNames() {
		v := Variable{Catalog: c, Name: "V", Type: "EW", RA: 10, Dec: 41}
		res := &Result{Candidates: []Candidate{{RA: 10, Dec: 41 + 1.5/3600, Sign: 1, SNR: 20}}}
		ls := classifyChanges(ctx, t, res, Known{Variables: []Variable{v}})
		if ls[0].Class != ClassKnownVariable || len(ls[0].Matches) != 1 || ls[0].Matches[0].Catalog != c {
			t.Errorf("%s at 1.5 arcsec: %s %+v", c, ls[0].Class, ls[0].Matches)
		}
		res.Candidates[0].Dec = 41 + 3.0/3600
		ls = classifyChanges(ctx, t, res, Known{Variables: []Variable{v}})
		if ls[0].Class == ClassKnownVariable || len(ls[0].Matches) != 0 {
			t.Errorf("%s at 3 arcsec: %s %+v", c, ls[0].Class, ls[0].Matches)
		}
	}
}

func TestSurveyVariablesFromVizieRAreCached(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var down atomic.Bool
	srv, asked := surveyVizier(t, func(string) bool { return down.Load() })
	r := New(nil, nil, &memStore{m: map[string][]byte{}}, &Catalogs{VizierURL: srv.URL}, Options{})
	now := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	var k Known
	r.surveyVariables(ctx, "M 31", 10, 41, 0.5, &k)
	if len(k.Checked) != 4 || len(k.Variables) != 4 || asked.Load() != 4 {
		t.Fatalf("checked %v, %d variables, VizieR asked %d times", k.Checked, len(k.Variables), asked.Load())
	}
	k = Known{}
	r.surveyVariables(ctx, "M 31", 10, 41, 0.5, &k)
	if len(k.Variables) != 4 || asked.Load() != 4 {
		t.Fatalf("a cached field asked VizieR again: %d variables, asked %d times", len(k.Variables), asked.Load())
	}
	down.Store(true)
	now = now.Add(60 * 24 * time.Hour)
	k = Known{}
	r.surveyVariables(ctx, "M 31", 10, 41, 0.5, &k)
	if len(k.Checked) != 4 || len(k.Failed) != 0 || len(k.Variables) != 4 {
		t.Errorf("with VizieR down an older answer stands in: checked %v failed %v", k.Checked, k.Failed)
	}
	k = Known{}
	r.surveyVariables(ctx, "M 33", 23.46, 30.66, 0.5, &k)
	if len(k.Checked) != 0 || !slices.Equal(k.Failed, surveyCatalogNames()) {
		t.Errorf("with VizieR down and no older answer: checked %v failed %v", k.Checked, k.Failed)
	}
}
