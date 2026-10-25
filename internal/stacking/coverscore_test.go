package stacking

import "testing"

// Masters as stored on 2026-10-24: Sculptor Galaxy's red, green and blue
// came from one night under a 90% Moon at airmass 2.2 to 3.2, and their subs
// scored 0.03 to 0.05; its cover was moonlit cloud bands. M 15's channels
// score 0.18 to 0.51. Carina Nebula's narrowband scored 0.01 to 0.03 and
// made a clean cover.
func TestChoosePaletteSkipsPoorScores(t *testing.T) {
	t.Parallel()
	ch := func(effective, exposure float64) layer {
		return layer{Key: "k", Effective: effective, Exposure: exposure}
	}
	sculptor := map[string]layer{
		filterRed:       ch(49.183679186718514, 1200),
		filterGreen:     ch(36.91992301516527, 900),
		filterBlue:      ch(38.61770420550303, 1200),
		filterHa:        ch(367.57951664299975, 600),
		filterLuminance: ch(1169.5408129126656, 1800),
	}
	if p, ok := choosePalette(sculptor, false); ok {
		t.Errorf("Sculptor Galaxy: got a %s cover from subs scoring 0.03 to 0.05", p.Name)
	}
	m15 := map[string]layer{
		filterRed:       ch(245.82826493063297, 900),
		filterGreen:     ch(195.74806755842133, 900),
		filterBlue:      ch(651.9957503229649, 2400),
		filterHa:        ch(3448.321434243039, 19200),
		filterLuminance: ch(1682.581271401879, 3300),
	}
	p, ok := choosePalette(m15, false)
	if !ok || p.Name != paletteRGBHa || !p.Lum {
		t.Errorf("M 15: got %q (ok %v, lum %v), want RGB+Ha with luminance", p.Name, ok, p.Lum)
	}
	poorLum := map[string]layer{
		filterRed: ch(3600, 3600), filterGreen: ch(3600, 3600), filterBlue: ch(3600, 3600),
		filterLuminance: ch(40, 1800),
	}
	if p, ok := choosePalette(poorLum, false); !ok || p.Lum {
		t.Errorf("poor luminance: got %q (ok %v, lum %v), want RGB without it", p.Name, ok, p.Lum)
	}
	carina := map[string]layer{
		filterHa:   ch(32.142324793650126, 3000),
		filterOIII: ch(25.43791859480399, 2400),
		filterSII:  ch(115.65794629604171, 3600),
	}
	if p, ok := choosePalette(carina, false); !ok || p.Name != paletteHOO {
		t.Errorf("Carina Nebula: got %q (ok %v), want HOO from narrowband scoring 0.01", p.Name, ok)
	}
	if p, ok := choosePalette(map[string]layer{filterRed: ch(3600, 0), filterGreen: ch(3600, 0), filterBlue: ch(3600, 0)}, false); !ok || p.Name != paletteRGB {
		t.Errorf("unknown exposure: got %q (ok %v), want RGB", p.Name, ok)
	}
}

func TestPoorScore(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		filter              string
		effective, exposure float64
		want                bool
	}{
		{filterRed, 49.183679186718514, 1200, true},
		{filterBlue, 651.9957503229649, 2400, false},
		{filterLuminance, 0.05 * 600, 600, false},
		{filterGreen, 0.0499 * 600, 600, true},
		{filterRed, 10, 0, false},
		{filterHa, 32.142324793650126, 3000, false},
	} {
		if got := PoorScore(c.filter, c.effective, c.exposure); got != c.want {
			t.Errorf("PoorScore(%s, %v, %v) = %v, want %v", c.filter, c.effective, c.exposure, got, c.want)
		}
	}
}
