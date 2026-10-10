package main

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
	"github.com/klauspost/compress/zstd"
)

//go:embed herschel400.txt
var herschel400 string

//go:embed aliases.tsv
var curatedAliases string

const openNGCRef = "v20260501"

const listGreen = "green"

const srcOpenNGC = "openngc"

const (
	pArp   = "Arp "
	pLBN   = "LBN "
	pMel   = "Mel "
	pSh2   = "Sh2-"
	pCr    = "Cr "
	pLDN   = "LDN "
	pPK    = "PK "
	pHCG   = "HCG "
	pUGC   = "UGC "
	pRCW   = "RCW "
	pCed   = "Ced "
	pGum   = "Gum "
	pVdB   = "vdB "
	pACO   = "ACO "
	catNGC = "NGC"
)

const vizierURL = "https://vizier.cds.unistra.fr/viz-bin/asu-tsv?-source=%s&-out.max=unlimited&-out.all&-out.add=_RAJ2000,_DEJ2000&-oc.form=d"

func main() {
	out := flag.String("out", "internal/catalog/data/catalog.json.zst", "output file")
	nina := flag.String("nina", "", "NINA.sqlite from a N.I.N.A. install, for the atlas overlay")
	ninaOut := flag.String("nina-out", "internal/catalog/data/nina.json.zst", "atlas overlay output file")
	cache := flag.String("cache", "", "directory to cache downloads in")
	audit := flag.Bool("audit", false, "check every nickname against SIMBAD and fail on unreviewed mismatches")
	flag.Parse()
	b := newBuilder(*cache)
	if err := b.build(context.Background()); err != nil {
		log.Fatal(err)
	}
	if err := b.write(*out); err != nil {
		log.Fatal(err)
	}
	if *nina != "" {
		if err := b.ninaOverlay(*nina, *ninaOut); err != nil {
			log.Fatal(err)
		}
	}
	if *audit {
		overlay := *ninaOut
		if *nina == "" && overlay != "" {
			if _, err := os.Stat(overlay); err != nil {
				overlay = ""
			}
		}
		objs, err := loadBuilt(*out, overlay)
		if err != nil {
			log.Fatal(err)
		}
		if err := b.auditNames(context.Background(), objs); err != nil {
			log.Fatal(err)
		}
	}
}

type builder struct {
	cache   string
	client  *http.Client
	objects []*catalog.Object
	byKey   map[string]*catalog.Object
	byName  map[string]*catalog.Object
	cells   map[int][]*catalog.Object
	lists   map[string][]string
	sources []catalog.Source
}

func newBuilder(cache string) *builder {
	return &builder{cache: cache, client: &http.Client{Timeout: 5 * time.Minute}, byKey: map[string]*catalog.Object{}, byName: map[string]*catalog.Object{}, lists: map[string][]string{}}
}

func (b *builder) fetch(ctx context.Context, url, name string) ([]byte, error) {
	if b.cache != "" {
		if data, err := os.ReadFile(b.cache + "/" + name); err == nil {
			return data, nil
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "astro-stacker catalogue builder (github.com/USA-RedDragon/astro-stacker)")
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if b.cache != "" {
		if err := os.MkdirAll(b.cache, 0o750); err == nil {
			_ = os.WriteFile(b.cache+"/"+name, data, 0o600)
		}
	}
	return data, nil
}

func (b *builder) add(o *catalog.Object) {
	o.ID = catalog.Key(o.Designation)
	b.objects = append(b.objects, o)
	b.index(o, o.Designation)
	b.indexName(o, o.Name)
	for _, a := range o.Aliases {
		b.index(o, a)
		b.indexName(o, a)
	}
}

func (b *builder) indexName(o *catalog.Object, name string) {
	if name == "" {
		return
	}
	if _, ok := catalog.Canonical(name); ok {
		return
	}
	n := catalog.NormalizeName(name)
	if _, taken := b.byName[n]; !taken {
		b.byName[n] = o
	}
}

func (b *builder) byCommonName(o *catalog.Object) *catalog.Object {
	for _, n := range append([]string{o.Name}, o.Aliases...) {
		if n == "" {
			continue
		}
		if _, ok := catalog.Canonical(n); ok {
			continue
		}
		if existing := b.byName[catalog.NormalizeName(n)]; existing != nil && compatible(existing, o) {
			return existing
		}
	}
	return nil
}

func (b *builder) index(o *catalog.Object, name string) {
	if !catalog.LooksLikeDesignation(name) {
		return
	}
	k := catalog.Key(name)
	if _, taken := b.byKey[k]; !taken {
		b.byKey[k] = o
	}
}

func (b *builder) lookup(name string) *catalog.Object {
	if !catalog.LooksLikeDesignation(name) {
		return nil
	}
	return b.byKey[catalog.Key(name)]
}

func addAlias(o *catalog.Object, a string) {
	a = strings.TrimSpace(a)
	if a == "" || a == o.Designation || a == o.Name {
		return
	}
	if slices.Contains(o.Aliases, a) {
		return
	}
	o.Aliases = append(o.Aliases, a)
}

func (b *builder) alias(o *catalog.Object, a string) {
	if c, ok := catalog.Canonical(a); ok {
		a = c
	}
	addAlias(o, a)
	b.index(o, a)
	b.indexName(o, a)
}

func (b *builder) merge(into, from *catalog.Object) {
	b.alias(into, from.Designation)
	for _, a := range from.Aliases {
		b.alias(into, a)
	}
	if into.Name == "" {
		into.Name = from.Name
	} else if from.Name != "" {
		addAlias(into, from.Name)
	}
	b.indexName(into, from.Name)
	if into.MajorArcmin == 0 && from.MajorArcmin > 0 {
		into.MajorArcmin, into.MinorArcmin, into.PA = from.MajorArcmin, from.MinorArcmin, from.PA
	}
	if into.Brightness == "" {
		into.Brightness = from.Brightness
		into.BrightScore = from.BrightScore
	}
	if into.Type == catalog.TypeOther || into.Type == catalog.TypeNebula {
		into.Type = from.Type
	}
}

func compatible(a, b *catalog.Object) bool {
	d := sep(a.RA, a.Dec, b.RA, b.Dec)
	reach := math.Max(math.Max(a.MajorArcmin, b.MajorArcmin)/120, 0.1)
	if d > reach {
		return false
	}
	if a.MajorArcmin > 0 && b.MajorArcmin > 0 {
		r := a.MajorArcmin / b.MajorArcmin
		if r > 4 || r < 0.25 {
			return false
		}
	}
	return true
}

func (b *builder) addOrMerge(o *catalog.Object, crossIDs []string) {
	if existing := b.lookup(o.Designation); existing != nil {
		if existing.Designation == o.Designation || compatible(existing, o) {
			b.merge(existing, o)
			return
		}
		k := catalog.Key(o.Designation)
		existing.Aliases = slices.DeleteFunc(existing.Aliases, func(a string) bool { return catalog.Key(a) == k })
		delete(b.byKey, k)
	}
	if len(crossIDs) > 1 {
		crossIDs = nil
	}
	for _, id := range crossIDs {
		if existing := b.lookup(id); existing != nil && compatible(existing, o) {
			b.merge(existing, o)
			for _, c := range crossIDs {
				b.alias(existing, c)
			}
			return
		}
	}
	if existing := b.byCommonName(o); existing != nil {
		b.merge(existing, o)
		return
	}
	for _, c := range crossIDs {
		if cc, ok := catalog.Canonical(c); ok && b.lookup(cc) == nil {
			addAlias(o, cc)
		}
	}
	b.add(o)
}

func (b *builder) build(ctx context.Context) error {
	steps := []func(context.Context) error{
		b.openNGC, b.sharpless, b.lbn, b.ldn, b.barnard, b.vdb, b.arp, b.hickson, b.green, b.planetaries, b.rcw, b.cederblad,
	}
	for _, s := range steps {
		if err := s(ctx); err != nil {
			return err
		}
	}
	if err := b.curated(); err != nil {
		return err
	}
	if err := b.fixNames(); err != nil {
		return err
	}
	b.finalize()
	if err := b.groups(); err != nil {
		return err
	}
	return b.buildLists()
}

func hms(s string, hours bool) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	sign := 1.0
	if s[0] == '-' || s[0] == '+' {
		if s[0] == '-' {
			sign = -1
		}
		s = s[1:]
	}
	parts := strings.Split(s, ":")
	v, mult := 0.0, 1.0
	for _, p := range parts {
		f, err := strconv.ParseFloat(p, 64)
		if err != nil {
			return 0, false
		}
		v += f * mult
		mult /= 60
	}
	if hours {
		v *= 15
	}
	return sign * v, true
}

func num(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) {
		return 0, false
	}
	return f, true
}

func ptr(f float64, ok bool) *float64 {
	if !ok {
		return nil
	}
	return &f
}

func axis(s string) *float64 {
	f, ok := num(s)
	if !ok || f <= 0 {
		return nil
	}
	return &f
}

func openNGCType(t string) string {
	switch t {
	case "G":
		return catalog.TypeGalaxy
	case "GPair", "GTrpl", "GGroup":
		return catalog.TypeGalaxyGroup
	case "HII", "EmN":
		return catalog.TypeEmission
	case "Neb":
		return catalog.TypeNebula
	case "RfN":
		return catalog.TypeReflection
	case "DrkN":
		return catalog.TypeDark
	case "PN":
		return catalog.TypePN
	case "SNR":
		return catalog.TypeSNR
	case "OCl", "*Ass":
		return catalog.TypeOpenCluster
	case "GCl":
		return catalog.TypeGlobular
	case "Cl+N":
		return catalog.TypeClusterNebula
	case "*", "**", "Nova":
		return catalog.TypeStar
	}
	return catalog.TypeOther
}

func keptIdentifier(id string) bool {
	for _, p := range []string{"C ", pLBN, pLDN, "SH 2-", "B ", pVdB, "VdB ", pArp, pHCG, "Cl ", pMel, pCed, pRCW, pGum, "PGC ", pUGC, pPK, "Abell ", pCr} {
		if strings.HasPrefix(id, p) {
			return true
		}
	}
	return false
}

type ngcDup struct{ name, target string }

func (b *builder) openNGC(ctx context.Context) error {
	b.sources = append(b.sources, catalog.Source{
		ID: srcOpenNGC, Name: "OpenNGC " + openNGCRef, Citation: "Mattia Verga, OpenNGC, https://github.com/mattiaverga/OpenNGC",
		URL: "https://github.com/mattiaverga/OpenNGC/tree/" + openNGCRef, Licence: "CC-BY-SA-4.0",
	})
	var dups []ngcDup
	for _, file := range []string{"NGC.csv", "addendum.csv"} {
		data, err := b.fetch(ctx, "https://raw.githubusercontent.com/mattiaverga/OpenNGC/"+openNGCRef+"/database_files/"+file, "openngc-"+file)
		if err != nil {
			return err
		}
		r := csv.NewReader(bytes.NewReader(data))
		r.Comma = ';'
		r.FieldsPerRecord = -1
		rows, err := r.ReadAll()
		if err != nil {
			return fmt.Errorf("%s: %w", file, err)
		}
		t := table{cols: map[string]int{}, rows: rows[1:]}
		for i, h := range rows[0] {
			t.cols[h] = i
		}
		for _, row := range t.rows {
			o, d := openNGCRow(t, row)
			dups = append(dups, d...)
			if o != nil {
				b.add(o)
			}
		}
	}
	for _, d := range dups {
		if target := b.lookup(d.target); target != nil {
			b.alias(target, d.name)
		}
	}
	return nil
}

func openNGCRow(t table, row []string) (*catalog.Object, []ngcDup) {
	name, typ := t.get(row, "Name"), t.get(row, "Type")
	switch typ {
	case "NonEx":
		return nil, nil
	case "Dup":
		var dups []ngcDup
		for _, c := range []string{catNGC, "IC"} {
			if v := t.get(row, c); v != "" {
				dups = append(dups, ngcDup{name, c + " " + v})
			}
		}
		if m := t.get(row, "M"); m != "" {
			dups = append(dups, ngcDup{"M " + m, name})
		}
		return nil, dups
	}
	ra, ok1 := hms(t.get(row, "RA"), true)
	dec, ok2 := hms(t.get(row, "Dec"), false)
	if !ok1 || !ok2 {
		return nil, nil
	}
	desig := name
	if c, ok := catalog.Canonical(name); ok {
		desig = c
	}
	o := &catalog.Object{Designation: desig, Type: openNGCType(typ), RA: ra, Dec: dec, Source: srcOpenNGC}
	o.MajorArcmin, _ = num(t.get(row, "MajAx"))
	o.MinorArcmin = axis(t.get(row, "MinAx"))
	o.PA = ptr(num(t.get(row, "PosAng")))
	if v, ok := num(t.get(row, "V-Mag")); ok {
		o.Magnitude = &v
	} else {
		o.Magnitude = ptr(num(t.get(row, "B-Mag")))
	}
	o.SurfaceBrightness = ptr(num(t.get(row, "SurfBr")))
	if m := t.get(row, "M"); m != "" {
		addAlias(o, "M "+strings.TrimLeft(m, "0"))
	}
	for _, c := range []string{catNGC, "IC"} {
		for v := range strings.SplitSeq(t.get(row, c), ",") {
			if cc, ok := catalog.Canonical(c + " " + strings.TrimSpace(v)); ok {
				addAlias(o, cc)
			}
		}
	}
	for id := range strings.SplitSeq(t.get(row, "Identifiers"), ",") {
		id = strings.TrimSpace(id)
		if keptIdentifier(id) {
			if cc, ok := catalog.Canonical(id); ok {
				id = cc
			}
			addAlias(o, id)
		}
	}
	for i, n := range strings.Split(t.get(row, "Common names"), ",") {
		n = strings.TrimSpace(n)
		switch {
		case n == "":
		case i == 0:
			o.Name = strings.TrimPrefix(n, "the ")
		default:
			addAlias(o, n)
		}
	}
	return o, nil
}

type table struct {
	cols map[string]int
	rows [][]string
}

func (t table) get(row []string, name string) string {
	i, ok := t.cols[name]
	if !ok || i >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[i])
}

func (b *builder) vizier(ctx context.Context, id, name, citation string) (table, error) {
	b.sources = append(b.sources, catalog.Source{
		ID: strings.ToLower(strings.ReplaceAll(id, "/", "-")), Name: name, Citation: citation + "; via VizieR, CDS, Strasbourg (DOI 10.26093/cds/vizier)",
		URL: "https://cdsarc.cds.unistra.fr/viz-bin/cat/" + id[:strings.LastIndex(id, "/")], Licence: "CDS VizieR terms: free for scientific and non-commercial use with citation",
	})
	data, err := b.fetch(ctx, fmt.Sprintf(vizierURL, id), "vizier-"+strings.ReplaceAll(id, "/", "_")+".tsv")
	if err != nil {
		return table{}, err
	}
	var t table
	stage := 0
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		switch stage {
		case 0:
			t.cols = map[string]int{}
			for i, f := range fields {
				t.cols[strings.TrimSpace(f)] = i
			}
			stage = 1
		case 1, 2:
			stage++
		default:
			t.rows = append(t.rows, fields)
		}
	}
	if len(t.rows) == 0 {
		return t, fmt.Errorf("%s: no rows", id)
	}
	return t, nil
}

func (t table) pos(row []string) (float64, float64, bool) {
	ra, ok1 := num(t.get(row, "_RAJ2000"))
	dec, ok2 := num(t.get(row, "_DEJ2000"))
	return ra, dec, ok1 && ok2
}

func classScore(v, best, worst float64) *float64 {
	s := (v - worst) / (best - worst)
	s = math.Max(0, math.Min(1, s))
	return &s
}

func (b *builder) sharpless(ctx context.Context) error {
	t, err := b.vizier(ctx, "VII/20/catalog", "Sharpless 1959, Catalogue of H II Regions", "Sharpless S. 1959, ApJS 4, 257")
	if err != nil {
		return err
	}
	for _, r := range t.rows {
		ra, dec, ok := t.pos(r)
		n := t.get(r, "Sh2")
		if !ok || n == "" {
			continue
		}
		o := &catalog.Object{Designation: pSh2 + n, Type: catalog.TypeEmission, RA: ra, Dec: dec, Source: "vii-20-catalog"}
		o.MajorArcmin, _ = num(t.get(r, "Diam"))
		if br, ok := num(t.get(r, "Bright")); ok {
			o.Brightness = fmt.Sprintf("Sharpless brightness %d of 3 (3 brightest)", int(br))
			o.BrightScore = classScore(br, 3, 1)
		}
		b.addOrMerge(o, nil)
	}
	return nil
}

func (b *builder) lbn(ctx context.Context) error {
	t, err := b.vizier(ctx, "VII/9/catalog", "Lynds 1965, Catalogue of Bright Nebulae", "Lynds B.T. 1965, ApJS 12, 163")
	if err != nil {
		return err
	}
	for _, r := range t.rows {
		ra, dec, ok := t.pos(r)
		n := t.get(r, "Seq")
		if !ok || n == "" {
			continue
		}
		o := &catalog.Object{Designation: pLBN + n, Type: catalog.TypeNebula, RA: ra, Dec: dec, Source: "vii-9-catalog"}
		o.MajorArcmin, _ = num(t.get(r, "Diam1"))
		o.MinorArcmin = axis(t.get(r, "Diam2"))
		if br, ok := num(t.get(r, "Bright")); ok {
			o.Brightness = fmt.Sprintf("LBN brightness %d of 6 (1 brightest)", int(br))
			o.BrightScore = classScore(br, 1, 6)
		}
		var cross []string
		if nm := t.get(r, "Name"); nm != "" {
			if strings.HasPrefix(nm, "S ") {
				nm = pSh2 + strings.TrimSpace(nm[2:])
			}
			cross = append(cross, nm)
		}
		b.addOrMerge(o, cross)
	}
	return nil
}

func (b *builder) ldn(ctx context.Context) error {
	t, err := b.vizier(ctx, "VII/7A/ldn", "Lynds 1962, Catalogue of Dark Nebulae", "Lynds B.T. 1962, ApJS 7, 1")
	if err != nil {
		return err
	}
	for _, r := range t.rows {
		ra, dec, ok := t.pos(r)
		n := t.get(r, "LDN")
		if !ok || n == "" {
			continue
		}
		o := &catalog.Object{Designation: pLDN + n, Type: catalog.TypeDark, RA: ra, Dec: dec, Source: "vii-7a-ldn"}
		if area, ok := num(t.get(r, "Area")); ok && area > 0 {
			o.MajorArcmin = 2 * math.Sqrt(area/math.Pi) * 60
		}
		if op, ok := num(t.get(r, "Opacity")); ok {
			o.Brightness = fmt.Sprintf("LDN opacity %d of 6 (6 darkest)", int(op))
			o.BrightScore = classScore(op, 6, 1)
		}
		var cross []string
		for f := range strings.FieldsSeq(t.get(r, "Barn")) {
			cross = append(cross, "B "+f)
		}
		b.addOrMerge(o, cross)
	}
	return nil
}

func (b *builder) barnard(ctx context.Context) error {
	t, err := b.vizier(ctx, "VII/220A/barnard", "Barnard 1927, Catalogue of 349 Dark Objects in the Sky", "Barnard E.E. 1927, Carnegie Institution of Washington")
	if err != nil {
		return err
	}
	for _, r := range t.rows {
		ra, dec, ok := t.pos(r)
		n := strings.TrimSpace(t.get(r, "Barn"))
		if !ok || n == "" {
			continue
		}
		o := &catalog.Object{Designation: "B " + n, Type: catalog.TypeDark, RA: ra, Dec: dec, Source: "vii-220a-barnard"}
		if c, ok := catalog.Canonical(o.Designation); ok {
			o.Designation = c
		}
		o.MajorArcmin, _ = num(t.get(r, "Diam"))
		b.addOrMerge(o, nil)
	}
	return nil
}

func (b *builder) vdb(ctx context.Context) error {
	t, err := b.vizier(ctx, "VII/21/catalog", "van den Bergh 1966, Catalogue of Reflection Nebulae", "van den Bergh S. 1966, AJ 71, 990")
	if err != nil {
		return err
	}
	for _, r := range t.rows {
		ra, dec, ok := t.pos(r)
		n := t.get(r, "VdB")
		if !ok || n == "" {
			continue
		}
		o := &catalog.Object{Designation: pVdB + n, Type: catalog.TypeReflection, RA: ra, Dec: dec, Source: "vii-21-catalog"}
		rad, ok := num(t.get(r, "BRadMax"))
		if rr, ok2 := num(t.get(r, "RRadMax")); ok2 && (!ok || rr > rad) {
			rad, ok = rr, true
		}
		if ok {
			o.MajorArcmin = 2 * rad
		}
		switch t.get(r, "SurfBr") {
		case "VBr":
			o.Brightness, o.BrightScore = "van den Bergh: very bright", classScore(4, 4, 1)
		case "Br":
			o.Brightness, o.BrightScore = "van den Bergh: bright", classScore(3, 4, 1)
		case "F":
			o.Brightness, o.BrightScore = "van den Bergh: faint", classScore(2, 4, 1)
		case "VF":
			o.Brightness, o.BrightScore = "van den Bergh: very faint", classScore(1, 4, 1)
		}
		b.addOrMerge(o, nil)
	}
	return nil
}

func (b *builder) arp(ctx context.Context) error {
	t, err := b.vizier(ctx, "VII/192/arplist", "Arp 1966, Atlas of Peculiar Galaxies", "Arp H. 1966, ApJS 14, 1; Webb 1996")
	if err != nil {
		return err
	}
	type member struct {
		ra, dec, dim float64
		name         string
	}
	groups := map[string][]member{}
	var order []string
	for _, r := range t.rows {
		ra, dec, ok := t.pos(r)
		n := t.get(r, "Arp")
		if !ok || n == "" {
			continue
		}
		dim, _ := num(t.get(r, "dim1"))
		if _, seen := groups[n]; !seen {
			order = append(order, n)
		}
		groups[n] = append(groups[n], member{ra, dec, dim, t.get(r, "Name")})
	}
	for _, n := range order {
		ms := groups[n]
		var x, y, z float64
		for _, m := range ms {
			cx, cy, cz := unit(m.ra, m.dec)
			x, y, z = x+cx, y+cy, z+cz
		}
		ra, dec := fromUnit(x, y, z)
		size := 0.0
		var names []string
		for _, m := range ms {
			size = math.Max(size, 2*sep(ra, dec, m.ra, m.dec)*60+m.dim)
			names = append(names, m.name)
		}
		o := &catalog.Object{Designation: pArp + n, Type: catalog.TypeGalaxy, RA: ra, Dec: dec, MajorArcmin: size, Source: "vii-192-arplist"}
		if len(ms) > 1 {
			o.Type = catalog.TypeGalaxyGroup
		}
		var cross []string
		if len(ms) == 1 {
			cross = names
		} else {
			for _, nm := range names {
				if c, ok := catalog.Canonical(nm); ok {
					addAlias(o, c)
				}
			}
		}
		b.addOrMerge(o, cross)
	}
	return nil
}

func (b *builder) hickson(ctx context.Context) error {
	t, err := b.vizier(ctx, "VII/213/groups", "Hickson 1982, Compact Groups of Galaxies", "Hickson P. 1982, ApJ 255, 382; Hickson et al. 1992")
	if err != nil {
		return err
	}
	for _, r := range t.rows {
		ra, dec, ok := t.pos(r)
		n := t.get(r, "HCG")
		if !ok || n == "" {
			continue
		}
		o := &catalog.Object{Designation: pHCG + n, Type: catalog.TypeGalaxyGroup, RA: ra, Dec: dec, Source: "vii-213-groups"}
		o.MajorArcmin, _ = num(t.get(r, "AngSize"))
		o.Magnitude = ptr(num(t.get(r, "Totmag")))
		b.addOrMerge(o, nil)
	}
	return nil
}

func (b *builder) green(ctx context.Context) error {
	t, err := b.vizier(ctx, "VII/297/snrs", "Green 2025, A Catalogue of Galactic Supernova Remnants", "Green D.A. 2025, J. Astrophys. Astron.")
	if err != nil {
		return err
	}
	for _, r := range t.rows {
		ra, dec, ok := t.pos(r)
		n := t.get(r, "SNR")
		if !ok || n == "" {
			continue
		}
		c, ok := catalog.Canonical(n)
		if !ok {
			continue
		}
		o := &catalog.Object{Designation: c, Type: catalog.TypeSNR, RA: ra, Dec: dec, Source: "vii-297-snrs"}
		o.MajorArcmin, _ = num(t.get(r, "MajDiam"))
		o.MinorArcmin = axis(t.get(r, "MinDiam"))
		for nm := range strings.SplitSeq(t.get(r, "Names"), ",") {
			nm = greenName(nm)
			if nm == "" {
				continue
			}
			if o.Name == "" && !strings.ContainsAny(nm, "0123456789") {
				o.Name = nm
			} else {
				addAlias(o, nm)
			}
		}
		b.addOrMerge(o, nil)
	}
	return nil
}

func (b *builder) planetaries(ctx context.Context) error {
	t, err := b.vizier(ctx, "V/84/main", "Acker et al. 1992, Strasbourg-ESO Catalogue of Galactic Planetary Nebulae", "Acker A. et al. 1992, ESO")
	if err != nil {
		return err
	}
	d, err := b.vizier(ctx, "V/84/diam", "Acker et al. 1992, PN diameters", "Acker A. et al. 1992, ESO")
	if err != nil {
		return err
	}
	b.sources = b.sources[:len(b.sources)-1]
	diam := map[string]float64{}
	for _, r := range d.rows {
		if v, ok := num(d.get(r, "oDiam")); ok {
			diam[d.get(r, "PNG")] = v / 60
		}
	}
	for _, r := range t.rows {
		ra, dec, ok := t.pos(r)
		png := t.get(r, "PNG")
		if !ok || png == "" {
			continue
		}
		name := t.get(r, "Name")
		var cross []string
		desig := "PN G" + png
		if strings.HasPrefix(name, "A ") {
			desig = "Abell " + strings.TrimSpace(name[2:])
			if c, ok := catalog.Canonical(desig); ok {
				desig = c
			}
			addCross := "PN G" + png
			cross = append(cross, addCross)
		} else if name != "" {
			cross = append(cross, name)
		}
		for id := range strings.SplitSeq(t.get(r, "Idents"), ",") {
			id = strings.TrimSpace(id)
			if strings.HasPrefix(id, "Sh 2-") {
				cross = append(cross, pSh2+strings.TrimSpace(id[5:]))
			}
		}
		o := &catalog.Object{Designation: desig, Type: catalog.TypePN, RA: ra, Dec: dec, Source: "v-84-main", MajorArcmin: diam[png]}
		if desig != "PN G"+png {
			addAlias(o, "PN G"+png)
		}
		b.addOrMerge(o, cross)
	}
	return nil
}

func (b *builder) rcw(ctx context.Context) error {
	t, err := b.vizier(ctx, "VII/216/rcw", "Rodgers, Campbell and Whiteoak 1960, H-alpha emission regions in the southern Milky Way", "Rodgers A.W., Campbell C.T., Whiteoak J.B. 1960, MNRAS 121, 103")
	if err != nil {
		return err
	}
	for _, r := range t.rows {
		ra, dec, ok := t.pos(r)
		n := t.get(r, "RCW")
		if !ok || n == "" {
			continue
		}
		o := &catalog.Object{Designation: pRCW + n, Type: catalog.TypeEmission, RA: ra, Dec: dec, Source: "vii-216-rcw"}
		o.MajorArcmin, _ = num(t.get(r, "MajAxis"))
		o.MinorArcmin = axis(t.get(r, "MinAxis"))
		switch t.get(r, "Br") {
		case "v":
			o.Brightness, o.BrightScore = "RCW: very bright", classScore(4, 4, 1)
		case "b":
			o.Brightness, o.BrightScore = "RCW: bright", classScore(3, 4, 1)
		case "m":
			o.Brightness, o.BrightScore = "RCW: moderate", classScore(2, 4, 1)
		case "f":
			o.Brightness, o.BrightScore = "RCW: faint", classScore(1, 4, 1)
		}
		var cross []string
		for part := range strings.SplitSeq(t.get(r, "IDs"), ";") {
			part = strings.TrimSpace(part)
			switch {
			case strings.HasPrefix(part, "NGC") || strings.HasPrefix(part, "IC"):
				cross = append(cross, part)
			case strings.HasPrefix(part, "G") && len(part) > 1 && part[1] >= '0' && part[1] <= '9':
				for g := range strings.SplitSeq(part[1:], ",") {
					if g = strings.TrimSpace(g); g != "" {
						addAlias(o, pGum+strings.TrimLeft(g, "0"))
					}
				}
			}
		}
		b.addOrMerge(o, cross)
	}
	return nil
}

func (b *builder) cederblad(ctx context.Context) error {
	t, err := b.vizier(ctx, "VII/231/catalog", "Cederblad 1946, Catalog of bright diffuse Galactic nebulae", "Cederblad S. 1946, Lund Medd. Ser. II 119")
	if err != nil {
		return err
	}
	for _, r := range t.rows {
		ra, dec, ok := t.pos(r)
		n := t.get(r, "Ced")
		if !ok || n == "" {
			continue
		}
		o := &catalog.Object{Designation: pCed + n + t.get(r, "m_Ced"), Type: catalog.TypeNebula, RA: ra, Dec: dec, Source: "vii-231-catalog"}
		o.MajorArcmin, _ = num(t.get(r, "Dim1"))
		o.MinorArcmin = axis(t.get(r, "Dim2"))
		var cross []string
		if nm := t.get(r, "Name"); nm != "" {
			cross = append(cross, nm)
		}
		b.addOrMerge(o, cross)
	}
	return nil
}

func (b *builder) curated() error {
	b.sources = append(b.sources, catalog.Source{
		ID: "curated", Name: "astro-stacker curated nicknames", Citation: "Common nicknames for catalogued objects, compiled for astro-stacker",
		URL: "https://github.com/USA-RedDragon/astro-stacker", Licence: "MIT",
	})
	for line := range strings.SplitSeq(curatedAliases, "\n") {
		parts := strings.Split(line, "\t")
		if len(parts) != 2 {
			continue
		}
		o := b.lookup(parts[0])
		if o == nil {
			return fmt.Errorf("curated alias %q: no object %q", parts[1], parts[0])
		}
		if c, ok := catalog.Canonical(parts[1]); ok {
			k := catalog.Key(c)
			if prev := b.byKey[k]; prev != nil && prev != o {
				prev.Aliases = slices.DeleteFunc(prev.Aliases, func(a string) bool { return catalog.Key(a) == k })
			}
			b.byKey[k] = o
			addAlias(o, c)
			continue
		}
		if o.Name == "" {
			o.Name = parts[1]
		} else {
			addAlias(o, parts[1])
		}
	}
	return nil
}

var errListSize = errors.New("list has the wrong number of objects")

func (b *builder) buildLists() error {
	b.sources = append(b.sources, catalog.Source{
		ID: "herschel400", Name: "Herschel 400", Citation: "Astronomical League Herschel 400 Observing Program list, as tabulated by Wikipedia (Herschel 400 Catalogue, revision 1369720351)",
		URL: "https://www.astroleague.org/herschel-400-observing-program/", Licence: "List of catalogue numbers (facts); Wikipedia text CC-BY-SA-4.0",
	})
	type spec struct {
		key, prefix string
		from, to    int
	}
	for _, s := range []spec{
		{"messier", "M ", 1, 110}, {"caldwell", "C ", 1, 109}, {"sharpless", pSh2, 1, 313}, {"vdb", pVdB, 1, 158},
		{"arp", pArp, 1, 338}, {"hickson", pHCG, 1, 100}, {"rcw", pRCW, 1, 182},
	} {
		var ids []string
		for i := s.from; i <= s.to; i++ {
			if o := b.lookup(s.prefix + strconv.Itoa(i)); o != nil {
				ids = append(ids, o.ID)
				o.Lists = appendUnique(o.Lists, s.key)
			}
		}
		b.lists[s.key] = ids
		log.Printf("%s: %d of %d", s.key, len(ids), s.to-s.from+1)
	}
	var h400 []string
	for line := range strings.SplitSeq(strings.TrimSpace(herschel400), "\n") {
		o := b.lookup("NGC " + strings.TrimSpace(line))
		if o == nil {
			return fmt.Errorf("herschel 400: no NGC %s", line)
		}
		h400 = append(h400, o.ID)
		o.Lists = appendUnique(o.Lists, "herschel400")
	}
	if len(h400) != 400 {
		return fmt.Errorf("herschel 400: %d: %w", len(h400), errListSize)
	}
	b.lists["herschel400"] = h400
	for _, prefixed := range []struct{ key, src string }{{"barnard", "vii-220a-barnard"}, {"lbn", "vii-9-catalog"}, {listGreen, "vii-297-snrs"}} {
		var ids []string
		for _, o := range b.objects {
			if o.Source == prefixed.src || hasAliasFrom(o, prefixed.key) {
				ids = append(ids, o.ID)
				o.Lists = appendUnique(o.Lists, prefixed.key)
			}
		}
		b.lists[prefixed.key] = ids
		log.Printf("%s: %d", prefixed.key, len(ids))
	}
	return nil
}

func hasAliasFrom(o *catalog.Object, list string) bool {
	prefix := map[string]string{"barnard": "B ", "lbn": pLBN, listGreen: "G"}[list]
	for _, a := range append([]string{o.Designation}, o.Aliases...) {
		if strings.HasPrefix(a, prefix) && (list != listGreen || len(a) > 1 && a[1] >= '0' && a[1] <= '9') {
			if _, ok := catalog.Canonical(a); ok {
				return true
			}
		}
	}
	return false
}

func appendUnique(s []string, v string) []string {
	if slices.Contains(s, v) {
		return s
	}
	return append(s, v)
}

func (b *builder) write(path string) error {
	objs := make([]catalog.Object, 0, len(b.objects))
	for _, o := range b.objects {
		o.RA = math.Round(o.RA*1e5) / 1e5
		o.Dec = math.Round(o.Dec*1e5) / 1e5
		objs = append(objs, *o)
	}
	data, err := json.Marshal(catalog.Dataset{Sources: b.sources, Lists: b.lists, Objects: objs})
	if err != nil {
		return err
	}
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	if err != nil {
		return err
	}
	out := enc.EncodeAll(data, nil)
	log.Printf("%d objects, %d bytes, %d compressed", len(objs), len(data), len(out))
	return os.WriteFile(path, out, 0o600)
}

func unit(ra, dec float64) (float64, float64, float64) {
	r, d := ra*math.Pi/180, dec*math.Pi/180
	return math.Cos(d) * math.Cos(r), math.Cos(d) * math.Sin(r), math.Sin(d)
}

func fromUnit(x, y, z float64) (float64, float64) {
	ra := math.Atan2(y, x) * 180 / math.Pi
	if ra < 0 {
		ra += 360
	}
	return ra, math.Atan2(z, math.Hypot(x, y)) * 180 / math.Pi
}

func sep(ra1, dec1, ra2, dec2 float64) float64 {
	return catalog.Separation(ra1, dec1, ra2, dec2)
}

func greenName(nm string) string {
	nm = strings.TrimSpace(nm)
	if strings.HasPrefix(nm, "(") && strings.HasSuffix(nm, ")") {
		nm = strings.TrimSpace(nm[1 : len(nm)-1])
	}
	if i := strings.Index(nm, "("); i > 0 && strings.HasSuffix(nm, ")") {
		nm = strings.TrimSpace(nm[:i])
	}
	return strings.Trim(nm, "() ")
}
