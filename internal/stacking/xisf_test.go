package stacking

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
)

// A master written as XISF keeps its pixels, its keywords and, as
// PixInsight's properties, its plate solution. Set MASTER_FIT to also
// convert a real master, for opening in PixInsight.
func TestXISFMaster(t *testing.T) {
	t.Parallel()
	const w, h = 4, 3
	data := []float32{0, .1, .2, .3, .4, .5, .6, .7, .8, .9, 1, .25}
	cards := []imagedata.Card{
		imagedata.StringCard("FILTER", "O'III", "filter"),
		imagedata.FloatCard("CRVAL1", 10, ""), imagedata.FloatCard("CRVAL2", 20, ""),
		imagedata.FloatCard("CRPIX1", 2.5, ""), imagedata.FloatCard("CRPIX2", 2, ""),
		imagedata.FloatCard("CDELT1", -1e-3, ""), imagedata.FloatCard("CDELT2", 1e-3, ""),
		imagedata.FloatCard("PC1_1", 1, ""), imagedata.FloatCard("PC1_2", 0.5, ""),
		imagedata.FloatCard("PC2_1", 0, ""), imagedata.FloatCard("PC2_2", 1, ""),
	}
	file := filepath.Join(t.TempDir(), "m.xisf")
	if err := writeXISFFile(file, w, h, data, cards); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	im, err := imagedata.Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range data {
		if im.Data[i] != v {
			t.Fatalf("sample %d = %v, want %v", i, im.Data[i], v)
		}
	}
	kw, err := frameheader.ParseCards(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(kw) != 1 || kw[0].Value != "O'III" || !kw[0].Quoted {
		t.Errorf("keywords = %+v, want FILTER only", kw)
	}
	props, _ := pixInsightSolution(cards, h)
	got := map[string][]float64{}
	for _, p := range props {
		if v, ok := p.Value.([]float64); ok {
			got[p.ID] = v
		}
	}
	if v := got["PCL:AstrometricSolution:ReferenceImageCoordinates"]; v[0] != 2 || v[1] != 1.5 {
		t.Errorf("reference pixel = %v, want [2 1.5]", v)
	}
	if v := got["PCL:AstrometricSolution:LinearTransformationMatrix"]; v[0] != -1e-3 || v[1] != 5e-4 || v[2] != 0 || v[3] != -1e-3 {
		t.Errorf("matrix = %v", v)
	}

	if src := os.Getenv("MASTER_FIT"); src != "" {
		b, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		im, err := imagedata.Decode(b)
		if err != nil {
			t.Fatal(err)
		}
		all, err := frameheader.ParseCards(b)
		if err != nil {
			t.Fatal(err)
		}
		var cards []imagedata.Card
		for _, c := range all {
			if !replacedKeywords()[c.Name] || c.Name == "IMAGETYP" || c.Name == "EXPTIME" {
				cards = append(cards, imageCard(c))
			}
		}
		if err := writeXISFFile(src+".xisf", im.W, im.H, im.Data, cards); err != nil {
			t.Fatal(err)
		}
	}
}
