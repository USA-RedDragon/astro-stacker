package starcat_test

import (
	"context"
	"math"
	"math/rand/v2"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/starcat"
)

func randomStars(n int, rng *rand.Rand, ra, dec, spread float64) []starcat.GaiaStar {
	out := make([]starcat.GaiaStar, n)
	for i := range out {
		s := starcat.GaiaStar{SourceID: rng.Int64(), RA: math.Mod(ra+(rng.Float64()*2-1)*spread+360, 360),
			Dec: math.Max(-90, math.Min(90, dec+(rng.Float64()*2-1)*spread)), RAError: rng.Float32(), DecError: rng.Float32(),
			Parallax: rng.NormFloat64(), ParallaxError: rng.Float32(), PMRA: rng.NormFloat64() * 10, PMRAError: rng.Float32(),
			PMDec: rng.NormFloat64() * 10, PMDecError: rng.Float32(), G: 10 + 10*rng.Float32(), BP: rng.Float32(), RP: rng.Float32(),
			RUWE: rng.Float32(), Variable: []uint8{starcat.VariableNotAvailable, starcat.VariableVariable}[rng.IntN(2)]}
		if rng.IntN(3) == 0 {
			s.Parallax, s.PMRA, s.PMDec = math.NaN(), math.NaN(), math.NaN()
			s.BP = float32(math.NaN())
		}
		out[i] = s
	}
	return out
}

const (
	i64 = "int64"
	f32 = "float32"
	f64 = "float64"
	str = "string"
)

func ecsvColumns() [][2]string {
	return [][2]string{{"source_id", i64}, {"ref_epoch", f64}, {"ra", f64}, {"ra_error", f32}, {"dec", f64},
		{"dec_error", f32}, {"parallax", f64}, {"parallax_error", f32}, {"pmra", f64}, {"pmra_error", f32},
		{"pmdec", f64}, {"pmdec_error", f32}, {"ruwe", f32}, {"phot_g_mean_mag", f32}, {"phot_bp_mean_mag", f32},
		{"phot_rp_mean_mag", f32}, {"phot_variable_flag", str}}
}

func sameStar(a, b starcat.GaiaStar) bool {
	f64 := func(x, y float64) bool { return math.Float64bits(x) == math.Float64bits(y) }
	f32 := func(x, y float32) bool { return math.Float32bits(x) == math.Float32bits(y) }
	return a.SourceID == b.SourceID && f64(a.RA, b.RA) && f64(a.Dec, b.Dec) && f32(a.RAError, b.RAError) && f32(a.DecError, b.DecError) &&
		f64(a.Parallax, b.Parallax) && f32(a.ParallaxError, b.ParallaxError) && f64(a.PMRA, b.PMRA) && f32(a.PMRAError, b.PMRAError) &&
		f64(a.PMDec, b.PMDec) && f32(a.PMDecError, b.PMDecError) && f32(a.G, b.G) && f32(a.BP, b.BP) && f32(a.RP, b.RP) &&
		f32(a.RUWE, b.RUWE) && a.Variable == b.Variable
}

func bySource(s []starcat.GaiaStar) {
	sort.Slice(s, func(i, j int) bool { return s[i].SourceID < s[j].SourceID })
}

func TestGaiaPieceKeepsEveryBit(t *testing.T) {
	t.Parallel()
	in := randomStars(10000, rand.New(rand.NewPCG(3, 4)), 100, 20, 1)
	out, err := starcat.RoundTripGaia(in)
	if err != nil {
		t.Fatal(err)
	}
	want := append([]starcat.GaiaStar(nil), in...)
	bySource(want)
	bySource(out)
	if len(out) != len(want) {
		t.Fatalf("%d rows back, want %d", len(out), len(want))
	}
	for i := range want {
		if !sameStar(out[i], want[i]) {
			t.Fatalf("row %d: %+v, want %+v", i, out[i], want[i])
		}
	}
}

func TestVSXPieceKeepsEveryField(t *testing.T) {
	t.Parallel()
	in := []starcat.VSXStar{
		{OID: 5, Name: "V0001 Ori", V: 0, RA: 83.1, Dec: -5.2, Type: "INS", Max: 11.2, Min: 0.5, FMin: "(", NMax: "V", Epoch: math.NaN(), Period: math.NaN(), SpectralTy: "K0"},
		{OID: 2, Name: "X", V: starcat.VSXNoFlag, RA: 83.2, Dec: -5.1, Max: math.NaN(), Min: math.NaN(), Epoch: 2450000.12345, Period: 0.123456789012, LPeriod: "<", UPeriod: ":"},
	}
	out, err := starcat.RoundTripVSX(in)
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OID > out[j].OID })
	norm := func(s starcat.VSXStar) starcat.VSXStar {
		for _, f := range []*float64{&s.Max, &s.Min, &s.Epoch, &s.Period} {
			if math.IsNaN(*f) {
				*f = -999
			}
		}
		return s
	}
	for i := range in {
		if !reflect.DeepEqual(norm(out[i]), norm(in[i])) {
			t.Fatalf("row %d: %+v, want %+v", i, out[i], in[i])
		}
	}
}

func TestParseGaiaECSV(t *testing.T) {
	t.Parallel()
	head := "# %ECSV 1.0\n# datatype:\n"
	cols := append([][2]string{{"solution_id", i64}, {"designation", str}}, ecsvColumns()...)
	names := make([]string, len(cols))
	for i, c := range cols {
		head += "# - \n#   name: " + c[0] + "\n#   datatype: " + c[1] + "\n#   meta:\n#     name: other\n"
		names[i] = c[0]
	}
	body := strings.Join(names, ",") + "\n" +
		`1636148068921376768,"Gaia DR3 4295806720",4295806720,2016.0,44.99615537864534,0.10161827,0.005615226341865997,0.10133387,0.3543305595550248,0.12266381,11.93835156938502,0.13794228,-4.0806193394130865,0.13316983,1.0034,17.640068,6.428645E-4,null,"VARIABLE"` + "\n" +
		`1636148068921376768,"Gaia DR3 1",1,2016.0,1.5,0.1,2.5,0.1,null,null,null,null,null,null,null,20.1,null,null,"NOT_AVAILABLE"`
	var got []starcat.GaiaStar
	n, err := starcat.ParseGaiaECSV(strings.NewReader(head+body), func(s starcat.GaiaStar) error { got = append(got, s); return nil })
	if err != nil || n != 2 {
		t.Fatalf("%d rows, %v", n, err)
	}
	s := got[0]
	if s.SourceID != 4295806720 || s.RA != 44.99615537864534 || s.Dec != 0.005615226341865997 || s.PMDec != -4.0806193394130865 ||
		s.G != float32(17.640068) || s.BP != float32(6.428645e-4) || !math.IsNaN(float64(s.RP)) || s.Variable != starcat.VariableVariable ||
		s.RUWE != float32(1.0034) || s.Parallax != 0.3543305595550248 {
		t.Fatalf("parsed %+v", s)
	}
	if !math.IsNaN(got[1].PMRA) || !math.IsNaN(got[1].Parallax) || got[1].Variable != starcat.VariableNotAvailable {
		t.Fatalf("parsed %+v", got[1])
	}
	bad := strings.Replace(head, "#   name: ra\n#   datatype: float64", "#   name: ra\n#   datatype: float32", 1)
	if _, err := starcat.ParseGaiaECSV(strings.NewReader(bad+body), func(starcat.GaiaStar) error { return nil }); err == nil {
		t.Fatal("a float32 ra column was accepted")
	}
	epoch := strings.Replace(body, ",2016.0,1.5", ",2015.5,1.5", 1)
	if _, err := starcat.ParseGaiaECSV(strings.NewReader(head+epoch), func(starcat.GaiaStar) error { return nil }); err == nil {
		t.Fatal("a row at epoch 2015.5 was accepted")
	}
}

func vsxLine(fields map[int]string) string {
	b := []byte(strings.Repeat(" ", 209))
	for at, v := range fields {
		copy(b[at-1:], v)
	}
	return string(b)
}

func TestParseVSXLine(t *testing.T) {
	t.Parallel()
	s, err := starcat.ParseVSXLine(" 2548147 Gaia DR3 4923911865762113408   0   1.76778 -54.01398 VAR                               15.290   G               15.360   G")
	if err != nil {
		t.Fatal(err)
	}
	if s.OID != 2548147 || s.Name != "Gaia DR3 4923911865762113408" || s.V != 0 || s.RA != 1.76778 || s.Dec != -54.01398 || s.Type != "VAR" ||
		s.Max != 15.29 || s.NMax != "G" || s.Min != 15.36 || s.NMin != "G" || !math.IsNaN(s.Period) || !math.IsNaN(s.Epoch) || s.SpectralTy != "" {
		t.Fatalf("parsed %+v", s)
	}
	s, err = starcat.ParseVSXLine(vsxLine(map[int]string{1: "     220", 10: "V0377 And", 41: "1", 43: "008.47042", 53: "+29.81669", 63: "SR",
		94: "<", 96: " 12.700", 104: ":", 106: "V", 117: "(", 119: ">", 121: "  0.400", 129: ":", 131: "V", 140: " 2459121.50000", 155: ":",
		157: "(", 159: "    135.000000000000", 179: ")", 181: "M5"}))
	if err != nil {
		t.Fatal(err)
	}
	want := starcat.VSXStar{OID: 220, Name: "V0377 And", V: 1, RA: 8.47042, Dec: 29.81669, Type: "SR", LMax: "<", Max: 12.7, UMax: ":", NMax: "V",
		FMin: "(", LMin: ">", Min: 0.4, UMin: ":", NMin: "V", Epoch: 2459121.5, UEpoch: ":", LPeriod: "(", Period: 135, UPeriod: ")", SpectralTy: "M5"}
	if !reflect.DeepEqual(s, want) {
		t.Fatalf("parsed %+v\nwant   %+v", s, want)
	}
}

func TestParseMD5Sums(t *testing.T) {
	t.Parallel()
	n, err := starcat.ParseMD5Sums([]byte("52d800747f32a025bdcf1594bbbad29b  GaiaSource_000000-003111.csv.gz\n2b916a83bda84c24f18fc947104d4d40  _MD5SUM.txt\n"))
	if err != nil || n != 1 {
		t.Fatalf("%d files, %v", n, err)
	}
	if _, err := starcat.ParseMD5Sums([]byte("junk  GaiaSource_000000-003111.csv.gz\n")); err == nil {
		t.Fatal("a bad line was accepted")
	}
}

func TestGaiaConeAndBoxMatchABruteForceSearch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rng := rand.New(rand.NewPCG(5, 6))
	stars := append(randomStars(60000, rng, 359.5, 30, 3), randomStars(20000, rng, 120, 88.5, 1.5)...)
	b := &starcat.MemBucket{}
	if err := starcat.WriteGaia(ctx, b, "gaia", stars); err != nil {
		t.Fatal(err)
	}
	g := starcat.NewGaia(b, "gaia")
	cases := [][3]float64{{359.5, 30, 1.2}, {0.3, 29, 0.4}, {120, 89.5, 1}, {100, 89.9, 0.3}}
	for _, c := range cases {
		got, err := g.Cone(ctx, c[0], c[1], c[2], func(s *starcat.GaiaStar) bool { return s.G < 18 })
		if err != nil {
			t.Fatal(err)
		}
		var want []starcat.GaiaStar
		for _, s := range stars {
			if starcat.Separation(c[0], c[1], s.RA, s.Dec) <= c[2] && s.G < 18 {
				want = append(want, s)
			}
		}
		bySource(want)
		if len(got) != len(want) || len(want) == 0 {
			t.Fatalf("cone %v: %d stars, brute force %d", c, len(got), len(want))
		}
		for i := range want {
			if !sameStar(got[i], want[i]) {
				t.Fatalf("cone %v row %d differs", c, i)
			}
		}
	}
	got, err := g.Box(ctx, 358.5, 0.5, 29, 31, nil)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, s := range stars {
		if (s.RA >= 358.5 || s.RA <= 0.5) && s.Dec >= 29 && s.Dec <= 31 {
			n++
		}
	}
	if len(got) != n || n == 0 {
		t.Fatalf("box: %d stars, brute force %d", len(got), n)
	}
	keys, _ := b.List(ctx, "gaia/")
	for _, k := range keys {
		if strings.HasSuffix(k, ".scat") {
			data, _ := b.Get(ctx, k)
			data[len(data)/2] ^= 1
			_ = b.Put(ctx, k, data)
		}
	}
	if _, err := starcat.NewGaia(b, "gaia").Cone(ctx, 359.5, 30, 3, nil); err == nil {
		t.Fatal("a damaged piece was read")
	}
	if _, err := starcat.NewGaia(&starcat.MemBucket{}, "gaia").Cone(ctx, 1, 1, 1, nil); err == nil {
		t.Fatal("an empty bucket answered")
	}
}
