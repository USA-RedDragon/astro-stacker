package main

import (
	"encoding/json"
	"log"
	"math"
	"os"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
	"github.com/glebarez/sqlite"
	"github.com/klauspost/compress/zstd"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type ninaRow struct {
	ID                string
	RA                *float64
	Dec               *float64
	Magnitude         *float64
	SurfaceBrightness *float64
	SizeMin           *float64
	SizeMax           *float64
	PositionAngle     *float64
	DSOType           string
}

type ninaDesignation struct {
	DSODetailID string `gorm:"column:dsodetailid"`
	Catalogue   string
	Designation string
}

func ninaType(t string) string {
	switch t {
	case "GALXY", "GX+DN":
		return catalog.TypeGalaxy
	case "GALCL":
		return catalog.TypeGalaxyGroup
	case "BRTNB", "LMCDN", "SMCDN":
		return catalog.TypeNebula
	case "CL+NB", "LMCCN", "SMCCN":
		return catalog.TypeClusterNebula
	case "DRKNB":
		return catalog.TypeDark
	case "PLNNB":
		return catalog.TypePN
	case "SNREM":
		return catalog.TypeSNR
	case "OPNCL", "LMCOC", "SMCOC", "ASTER":
		return catalog.TypeOpenCluster
	case "GLOCL", "LMCGC", "SMCGC":
		return catalog.TypeGlobular
	}
	return ""
}

func compact(s string) string {
	return strings.Join(strings.Fields(s), "")
}

func splitDesignation(cat, d string) (string, bool) {
	r, _ := utf8.DecodeRuneInString(d)
	if !unicode.IsLower(r) {
		return "", false
	}
	word, rest, _ := strings.Cut(d, " ")
	rest = strings.Join(strings.Fields(rest), " ")
	if strings.HasSuffix(cat, word) {
		return strings.TrimSpace(cat + " " + rest), true
	}
	return strings.TrimSpace(cat + word + " " + rest), true
}

func ninaDesignationName(cat, d, typ string) string {
	d = strings.TrimSpace(d)
	if d == "" {
		return ""
	}
	if joined, ok := splitDesignation(cat, d); ok {
		return joined
	}
	prefix := map[string]string{
		"NGC": "NGC ", "IC": "IC ", "UGC": pUGC, "Caldwell": "C ", "Sh2": pSh2, "Barnard": "B ", "LDN": pLDN, "LBN": pLBN,
		"vdB": pVdB, "Arp": pArp, "RCW": pRCW, "Ced": pCed, "Gum": pGum, "Hickson": pHCG, "Collinder": pCr,
		"Melotte": pMel, "PK": pPK, "ESO": "ESO ", "MCG": "MCG ", "Henize": "He ", "Ruprecht": "Ruprecht ", "Berkeley": "Berkeley ",
		"Trumpler": "Trumpler ", "Mrk": "Mrk ", "K": "K ", "Ho": "Ho ", "King": "King ", "Stock": "Stock ", "Haffner": "Haffner ",
		"Czernik": "Czernik ", "Dolidze": "Dolidze ", "Pismis": "Pismis ", "ZwG": "ZwG ", "Haro": "Haro ", "Hu": "Hu ", "Pal": "Pal ",
		"Harvard": "Harvard ", "Basel": "Basel ", "Lynga": "Lynga ", "Bochum": "Bochum ", "3C": "3C ", "VV": "VV ", "Biur": "Biurakan ",
		"Roslund": "Roslund ", "Dunlop": "Dunlop ", "Hogg": "Hogg ", "StDr": "StDr ", "LDu": "LDu ", "SL": "SL ", "Ra": "Ra ",
		"Fe": "Fe ", "Mul": "Mul ", "NPM1G": "NPM1G ", "H": "H ", "Dolidze-Dzimselejsvili": "DoDz ",
	}
	switch cat {
	case "NAME":
		return ""
	case "SNR":
		if c, ok := catalog.Canonical(d); ok {
			return c
		}
		return d
	case "M":
		if strings.Contains(d, "-") {
			return "M " + compact(d)
		}
		return "M " + d
	case "Abell":
		if typ == "GALCL" {
			return pACO + d
		}
		return "Abell " + d
	}
	p, ok := prefix[cat]
	if !ok {
		p = cat + " "
	}
	out := p + compact(d)
	if c, ok := catalog.Canonical(out); ok {
		return c
	}
	return out
}

func designationRank(d string) int {
	for i, p := range []string{"M ", "Mkn ", "NGC ", "IC ", pSh2, "C ", "B ", pLBN, pLDN, pVdB, pArp, pHCG, "Abell ", pACO, pCr, pMel, pPK, pUGC} {
		if strings.HasPrefix(d, p) && (p != "M " || !strings.Contains(d, "-")) {
			return i
		}
	}
	return 50
}

type overlay struct {
	Source  catalog.Source   `json:"source"`
	Objects []catalog.Object `json:"objects"`
	Patches []catalog.Patch  `json:"patches"`
}

func (b *builder) ninaOverlay(path, out string) error {
	db, err := gorm.Open(sqlite.Open("file:"+path+"?mode=ro"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		return err
	}
	var rows []ninaRow
	if err := db.Raw(`SELECT id, ra, dec, magnitude, surfacebrightness AS surface_brightness, sizemin AS size_min, sizemax AS size_max, positionangle AS position_angle, dsotype AS dso_type FROM dsodetail ORDER BY id`).Scan(&rows).Error; err != nil {
		return err
	}
	var desigs []ninaDesignation
	if err := db.Raw(`SELECT dsodetailid, catalogue, designation FROM cataloguenr`).Scan(&desigs).Error; err != nil {
		return err
	}
	byID := map[string][]ninaDesignation{}
	for _, d := range desigs {
		byID[d.DSODetailID] = append(byID[d.DSODetailID], d)
	}
	ov := overlay{Source: catalog.Source{
		ID: "nina-atlas", Name: "N.I.N.A. sky atlas (NINA.sqlite, N.I.N.A. 3.1)",
		Citation: "N.I.N.A. - Nighttime Imaging 'N' Astronomy, sky atlas database, compiled from SIMBAD, NED, VizieR and HASH",
		URL:      "https://github.com/isbeorn/nina", Licence: "MPL-2.0",
	}}
	usedIDs := map[string]bool{}
	for _, o := range b.objects {
		usedIDs[o.ID] = true
	}
	added, patched := 0, 0
	for _, r := range rows {
		o, ids, names := ninaObject(r, byID[r.ID], ov.Source.ID)
		if o == nil {
			continue
		}
		if existing := b.ninaMatch(o, ids, names); existing != nil {
			if p, ok := b.patch(existing, o, ids, names); ok {
				ov.Patches = append(ov.Patches, p)
				patched++
			}
			continue
		}
		if b.ninaNew(o, ids, names, usedIDs) {
			ov.Objects = append(ov.Objects, *o)
			added++
		}
	}
	if err := fixOverlayNames(&ov); err != nil {
		return err
	}
	data, err := json.Marshal(ov)
	if err != nil {
		return err
	}
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	if err != nil {
		return err
	}
	compressed := enc.EncodeAll(data, nil)
	log.Printf("NINA atlas: %d new objects, %d patched, %d bytes compressed", added, patched, len(compressed))
	return os.WriteFile(out, compressed, 0o600)
}

func ninaObject(r ninaRow, desigs []ninaDesignation, source string) (*catalog.Object, []string, []string) {
	typ := ninaType(r.DSOType)
	if typ == "" || r.RA == nil || r.Dec == nil {
		return nil, nil, nil
	}
	var names, ids []string
	for _, d := range desigs {
		if d.Catalogue == "NAME" {
			if n := capitalise(strings.TrimSpace(d.Designation)); n != "" && !slices.Contains(names, n) {
				names = append(names, n)
			}
			continue
		}
		if n := ninaDesignationName(d.Catalogue, d.Designation, r.DSOType); n != "" && !slices.Contains(ids, n) {
			ids = append(ids, n)
		}
	}
	if len(ids) == 0 && len(names) == 0 {
		return nil, nil, nil
	}
	o := &catalog.Object{Type: typ, RA: *r.RA, Dec: *r.Dec, Source: source}
	if r.SizeMax != nil {
		o.MajorArcmin = *r.SizeMax / 60
	}
	if r.SizeMin != nil && *r.SizeMin > 0 {
		v := *r.SizeMin / 60
		o.MinorArcmin = &v
	}
	o.PA = r.PositionAngle
	if r.Magnitude != nil && *r.Magnitude < 90 {
		o.Magnitude = r.Magnitude
	}
	if r.SurfaceBrightness != nil && *r.SurfaceBrightness < 90 && typ != catalog.TypeDark {
		o.SurfaceBrightness, o.SurfaceBrightnessSource = r.SurfaceBrightness, source
	}
	return o, ids, names
}

func (b *builder) ninaNew(o *catalog.Object, ids, names []string, usedIDs map[string]bool) bool {
	slices.SortStableFunc(ids, func(a, c string) int { return designationRank(a) - designationRank(c) })
	if len(ids) == 0 {
		return false
	}
	o.Designation = ids[0]
	o.ID = catalog.Key(o.Designation)
	if usedIDs[o.ID] {
		return false
	}
	for _, n := range names {
		if b.byName[catalog.NormalizeName(n)] != nil {
			continue
		}
		if o.Name == "" {
			o.Name = n
		} else {
			addAlias(o, n)
		}
	}
	for _, d := range ids[1:] {
		if b.lookup(d) == nil {
			addAlias(o, d)
		}
	}
	usedIDs[o.ID] = true
	o.RA = math.Round(o.RA*1e5) / 1e5
	o.Dec = math.Round(o.Dec*1e5) / 1e5
	b.index(o, o.Designation)
	for _, a := range o.Aliases {
		b.index(o, a)
	}
	b.indexName(o, o.Name)
	return true
}

func (b *builder) ninaMatch(o *catalog.Object, ids, names []string) *catalog.Object {
	for _, d := range ids {
		if existing := b.lookup(d); existing != nil {
			if sep(existing.RA, existing.Dec, o.RA, o.Dec) <= math.Max(0.25, math.Max(existing.MajorArcmin, o.MajorArcmin)/120) {
				return existing
			}
		}
	}
	for _, n := range names {
		if existing := b.byName[catalog.NormalizeName(n)]; existing != nil && compatible(existing, o) {
			return existing
		}
	}
	return b.nearestSame(o)
}

func band(dec float64) int { return int(math.Floor(dec * 4)) }

func (b *builder) grid() map[int][]*catalog.Object {
	if b.cells != nil {
		return b.cells
	}
	b.cells = map[int][]*catalog.Object{}
	for _, o := range b.objects {
		b.cells[band(o.Dec)] = append(b.cells[band(o.Dec)], o)
	}
	return b.cells
}

func (b *builder) nearestSame(o *catalog.Object) *catalog.Object {
	reach := math.Max(0.5/60, o.MajorArcmin/600)
	var best *catalog.Object
	bestD := reach
	cells := b.grid()
	for k := band(o.Dec - reach); k <= band(o.Dec+reach); k++ {
		for _, c := range cells[k] {
			if c.Type != o.Type && c.Type != catalog.TypeNebula && o.Type != catalog.TypeNebula {
				continue
			}
			if c.MajorArcmin > 0 && o.MajorArcmin > 0 && (c.MajorArcmin/o.MajorArcmin > 4 || o.MajorArcmin/c.MajorArcmin > 4) {
				continue
			}
			if d := sep(o.RA, o.Dec, c.RA, c.Dec); d <= bestD {
				best, bestD = c, d
			}
		}
	}
	return best
}

func (b *builder) patch(existing, o *catalog.Object, ids, names []string) (catalog.Patch, bool) {
	p := catalog.Patch{ID: existing.ID}
	have := map[string]bool{catalog.NormalizeName(existing.Designation): true, catalog.NormalizeName(existing.Name): true}
	for _, a := range existing.Aliases {
		have[catalog.NormalizeName(a)] = true
	}
	for _, d := range ids {
		if have[catalog.NormalizeName(d)] || b.lookup(d) != nil {
			continue
		}
		p.Aliases = append(p.Aliases, d)
		have[catalog.NormalizeName(d)] = true
		b.index(existing, d)
	}
	for _, n := range names {
		k := catalog.NormalizeName(n)
		if have[k] {
			continue
		}
		if owner := b.byName[k]; owner != nil && owner != existing {
			continue
		}
		have[k] = true
		if existing.Name == "" && p.Name == "" {
			p.Name = n
		} else {
			p.Aliases = append(p.Aliases, n)
		}
		b.byName[k] = existing
	}
	if existing.SurfaceBrightness == nil && o.SurfaceBrightness != nil {
		p.SurfaceBrightness = o.SurfaceBrightness
	}
	if existing.MajorArcmin == 0 && o.MajorArcmin > 0 {
		p.MajorArcmin, p.MinorArcmin, p.PA = o.MajorArcmin, o.MinorArcmin, o.PA
	}
	ok := p.Name != "" || len(p.Aliases) > 0 || p.SurfaceBrightness != nil || p.MajorArcmin > 0
	return p, ok
}
