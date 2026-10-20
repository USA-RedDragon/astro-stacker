package surveys

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func pairStar() Target {
	t := ic1396Star()
	t.Neighbours = []Neighbour{{GaiaID: 2179309875368546304, RA: 323.2765, Dec: 58.6345, G: 15.2}}
	return t
}

func countAPI(h http.Handler, n *atomic.Int32) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/cgi-bin/ZTF/nph_light_curves") {
			n.Add(1)
		}
		h.ServeHTTP(w, r)
	})
}

func TestPair(t *testing.T) {
	t.Parallel()
	if _, ok := Pair(ic1396Star()); ok {
		t.Error("a neighbour 3.48\" away is no pair")
	}
	n, ok := Pair(pairStar())
	if !ok || n.Sep < 1 || n.Sep > 2 {
		t.Errorf("pair %+v %v, want the neighbour about 1.4\" away", n, ok)
	}
	two := pairStar()
	two.Neighbours = append(two.Neighbours, Neighbour{GaiaID: 7, RA: 323.2775, Dec: 58.6343, G: 15.9})
	if _, ok := Pair(two); ok {
		t.Error("two close neighbours make no pair")
	}
	faint := pairStar()
	faint.Neighbours[0].G = faint.G + PairMag + 0.1
	if _, ok := Pair(faint); ok {
		t.Error("a neighbour more than PairMag fainter is no pair")
	}
}

func TestZTFParsesEpochPositions(t *testing.T) {
	t.Parallel()
	f := &Fetcher{}
	eps, positions, err := f.parseZTFAPI(readFixture(t, "ztf_api_ic1396.csv"), 323.2769976, 58.6342899)
	if err != nil {
		t.Fatal(err)
	}
	if !positions || len(eps) == 0 {
		t.Fatalf("positions %v, %d epochs", positions, len(eps))
	}
	if eps[0].RA != 323.2769904 || eps[0].Dec != 58.6343187 {
		t.Errorf("first epoch at %v %v, want 323.2769904 58.6343187", eps[0].RA, eps[0].Dec)
	}
	_, positions, err = f.parseZTFAPI([]byte("oid,hjd,mag,magerr,catflags,filtercode\n802115100046506,2458208.971546364,15.73,0.014,0,zg\n"), 323.2769976, 58.6342899)
	if err != nil || positions {
		t.Errorf("positions %v, err %v without ra and dec columns", positions, err)
	}
}

func TestZTFPositionsOnlyForPairs(t *testing.T) {
	t.Parallel()
	var hits, api atomic.Int32
	f, _ := testFetcher(t, countAPI(ztfServer(t, &hits), &api))
	ctx := t.Context()
	field := ic1396Field()
	if err := f.Prefetch(ctx, field); err != nil {
		t.Fatal(err)
	}
	lone := f.ZTF(ctx, field, ic1396Star())
	if api.Load() != 0 {
		t.Fatalf("asked the light curve service %d times for a star with no close neighbour", api.Load())
	}
	for _, p := range lone.Series[0].Points {
		if p.RA != 0 || p.Dec != 0 {
			t.Fatal("a star read from the field's files alone got positions")
		}
	}
	key := f.dir(field, "ztf") + "api/" + starKey(pairStar()) + ".json.zst"
	old, err := json.Marshal(ztfAPICache{Fetched: f.now(), RA: 323.277, Dec: 58.6343, Epochs: []ztfEpoch{{Band: "g", OID: 1, MJD: 58208.5, Mag: 15, MagErr: 0.01}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.putZ(ctx, key, old); err != nil {
		t.Fatal(err)
	}
	fe := f.ZTF(ctx, field, pairStar())
	if api.Load() != 1 {
		t.Fatalf("asked the light curve service %d times, want once to replace a cache without positions", api.Load())
	}
	if len(fe.Series) != 1 || len(fe.Series[0].Points) != len(lone.Series[0].Points) {
		t.Fatalf("positions changed the light curve: %+v", fe)
	}
	with := 0
	for i, p := range fe.Series[0].Points {
		q := lone.Series[0].Points[i]
		if p.BJD != q.BJD || p.Mag != q.Mag || p.Err != q.Err || p.Src != q.Src {
			t.Fatalf("point %d is %+v, was %+v", i, p, q)
		}
		if p.RA != 0 {
			with++
			if sepArcsec(p.RA, p.Dec, 323.2769976, 58.6342899) > 1.5 {
				t.Errorf("point %d placed %.2f\" from the star", i, sepArcsec(p.RA, p.Dec, 323.2769976, 58.6342899))
			}
		}
	}
	if with < 40 {
		t.Errorf("only %d of %d field-file epochs got a position", with, len(fe.Series[0].Points))
	}
	f.ZTF(ctx, field, pairStar())
	if api.Load() != 1 {
		t.Errorf("asked the light curve service %d times, want the cache with positions reused", api.Load())
	}
}
