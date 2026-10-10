package catalog

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/klauspost/compress/zstd"
)

//go:embed data/catalog.json.zst
var embeddedData []byte

//go:embed data/nina.json.zst
var embeddedNINA []byte

type Patch struct {
	ID                string   `json:"id"`
	Name              string   `json:"name,omitempty"`
	Aliases           []string `json:"aliases,omitempty"`
	SurfaceBrightness *float64 `json:"surfaceBrightness,omitempty"`
	MajorArcmin       float64  `json:"majorArcmin,omitempty"`
	MinorArcmin       float64  `json:"minorArcmin,omitempty"`
}

type Overlay struct {
	Source  Source   `json:"source"`
	Objects []Object `json:"objects"`
	Patches []Patch  `json:"patches"`
}

func (ds *Dataset) Apply(ov Overlay) {
	pos := make(map[string]int, len(ds.Objects))
	for i, o := range ds.Objects {
		pos[o.ID] = i
	}
	for _, p := range ov.Patches {
		i, ok := pos[p.ID]
		if !ok {
			continue
		}
		o := &ds.Objects[i]
		if o.Name == "" {
			o.Name = p.Name
		} else if p.Name != "" && p.Name != o.Name && !slices.Contains(o.Aliases, p.Name) {
			o.Aliases = append(o.Aliases, p.Name)
		}
		for _, a := range p.Aliases {
			if !slices.Contains(o.Aliases, a) && a != o.Designation && a != o.Name {
				o.Aliases = append(o.Aliases, a)
			}
		}
		if o.SurfaceBrightness == nil {
			o.SurfaceBrightness = p.SurfaceBrightness
		}
		if o.MajorArcmin == 0 && p.MajorArcmin > 0 {
			o.MajorArcmin, o.MinorArcmin = p.MajorArcmin, p.MinorArcmin
		}
	}
	for _, o := range ov.Objects {
		if _, dup := pos[o.ID]; dup {
			continue
		}
		pos[o.ID] = len(ds.Objects)
		ds.Objects = append(ds.Objects, o)
	}
	ds.Sources = append(ds.Sources, ov.Source)
}

func decompress(compressed []byte) ([]byte, error) {
	dec, err := zstd.NewReader(nil)
	if err != nil {
		return nil, err
	}
	defer dec.Close()
	return dec.DecodeAll(compressed, nil)
}

type Source struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Citation string `json:"citation"`
	URL      string `json:"url"`
	Licence  string `json:"licence"`
}

type Dataset struct {
	Sources []Source            `json:"sources"`
	Lists   map[string][]string `json:"lists"`
	Objects []Object            `json:"objects"`
}

type ListInfo struct {
	Key   string `json:"key"`
	Name  string `json:"name"`
	Total int    `json:"total"`
}

func listCatalogue() []ListInfo {
	return []ListInfo{
		{Key: "messier", Name: "Messier"}, {Key: "caldwell", Name: "Caldwell"}, {Key: "herschel400", Name: "Herschel 400"},
		{Key: "sharpless", Name: "Sharpless"}, {Key: "vdb", Name: "van den Bergh"}, {Key: "barnard", Name: "Barnard"},
		{Key: "lbn", Name: "Lynds Bright"}, {Key: "arp", Name: "Arp"}, {Key: "hickson", Name: "Hickson"},
		{Key: "rcw", Name: "RCW"}, {Key: "green", Name: "Green SNR"},
	}
}

type entry struct {
	keys  []string
	names []string
	cores []string
	tris  [][]uint64
	rank  float64
}

type Index struct {
	objects []Object
	entries []entry
	byKey   map[string]int
	byID    map[string]int
	byName  map[string][]int
	lists   map[string][]string
	sources []Source
}

func LoadEmbedded() (*Index, error) {
	ds, err := readDataset(embeddedData)
	if err != nil {
		return nil, err
	}
	raw, err := decompress(embeddedNINA)
	if err != nil {
		return nil, fmt.Errorf("decompress atlas overlay: %w", err)
	}
	var ov Overlay
	if err := json.Unmarshal(raw, &ov); err != nil {
		return nil, fmt.Errorf("parse atlas overlay: %w", err)
	}
	ds.Apply(ov)
	ds.Apply(curatedOverlay())
	return NewIndex(ds), nil
}

func readDataset(compressed []byte) (Dataset, error) {
	raw, err := decompress(compressed)
	if err != nil {
		return Dataset{}, fmt.Errorf("decompress catalogue: %w", err)
	}
	var ds Dataset
	if err := json.Unmarshal(raw, &ds); err != nil {
		return Dataset{}, fmt.Errorf("parse catalogue: %w", err)
	}
	return ds, nil
}

func Load(compressed []byte) (*Index, error) {
	ds, err := readDataset(compressed)
	if err != nil {
		return nil, err
	}
	return NewIndex(ds), nil
}

func NewIndex(ds Dataset) *Index {
	ix := &Index{
		objects: ds.Objects, entries: make([]entry, len(ds.Objects)), byKey: map[string]int{}, byID: map[string]int{},
		byName: map[string][]int{}, lists: ds.Lists, sources: ds.Sources,
	}
	for i := range ix.objects {
		o := &ix.objects[i]
		ix.byID[o.ID] = i
		e := &ix.entries[i]
		for _, d := range append([]string{o.Designation}, o.Aliases...) {
			if LooksLikeDesignation(d) {
				k := Key(d)
				e.keys = append(e.keys, k)
				if _, taken := ix.byKey[k]; !taken {
					ix.byKey[k] = i
				}
			}
		}
		for _, n := range append([]string{o.Name, o.Designation}, o.Aliases...) {
			if n == "" {
				continue
			}
			nn := NormalizeName(n)
			if nn != "" && !slices.Contains(e.names, nn) {
				e.names = append(e.names, nn)
				c := CoreName(n)
				e.cores = append(e.cores, c)
				e.tris = append(e.tris, trigrams(c))
				ix.byName[nn] = append(ix.byName[nn], i)
			}
		}
		e.rank = float64(len(o.Lists))
		if o.Name != "" {
			e.rank += 0.5
		}
		e.rank += math.Min(o.MajorArcmin, 600) / 1200
	}
	return ix
}

func (ix *Index) Len() int { return len(ix.objects) }

func (ix *Index) Sources() []Source { return ix.sources }

func (ix *Index) Get(id string) (Object, bool) {
	if i, ok := ix.byID[id]; ok {
		return ix.objects[i], true
	}
	if i, ok := ix.byKey[Key(id)]; ok {
		return ix.objects[i], true
	}
	return Object{}, false
}

func (ix *Index) Lookup(designation string) (Object, bool) {
	if !LooksLikeDesignation(designation) {
		return Object{}, false
	}
	i, ok := ix.byKey[Key(designation)]
	if !ok {
		return Object{}, false
	}
	return ix.objects[i], true
}

func (ix *Index) Lists() []ListInfo {
	var out []ListInfo
	for _, l := range listCatalogue() {
		if ids, ok := ix.lists[l.Key]; ok {
			l.Total = len(ids)
			out = append(out, l)
		}
	}
	return out
}

func (ix *Index) List(key string) []Object {
	ids := ix.lists[key]
	out := make([]Object, 0, len(ids))
	for _, id := range ids {
		if i, ok := ix.byID[id]; ok {
			out = append(out, ix.objects[i])
		}
	}
	return out
}

func (ix *Index) All() []Object { return ix.objects }

func (ix *Index) Cone(_ context.Context, ra, dec, radius float64) ([]Object, error) {
	type hit struct {
		i int
		d float64
	}
	var hits []hit
	for i := range ix.objects {
		o := &ix.objects[i]
		if math.Abs(o.Dec-dec) > radius {
			continue
		}
		if d := Separation(ra, dec, o.RA, o.Dec); d <= radius {
			hits = append(hits, hit{i, d})
		}
	}
	slices.SortFunc(hits, func(a, b hit) int {
		if a.d != b.d {
			if a.d < b.d {
				return -1
			}
			return 1
		}
		return strings.Compare(ix.objects[a.i].ID, ix.objects[b.i].ID)
	})
	out := make([]Object, len(hits))
	for j, h := range hits {
		out[j] = ix.objects[h.i]
	}
	return out, nil
}

const (
	scoreDesignation = 100.0
	scoreExactName   = 80.0
	scoreCoreName    = 70.0
	scorePrefix      = 50.0
	scoreSubstring   = 35.0
	fuzzyFloor       = 0.45
)

type Match struct {
	Object Object  `json:"object"`
	Score  float64 `json:"score"`
	How    string  `json:"how"`
}

func (ix *Index) Search(ctx context.Context, query string, limit int) ([]Object, error) {
	ms := ix.Find(ctx, query, limit)
	out := make([]Object, len(ms))
	for i, m := range ms {
		out[i] = m.Object
	}
	return out, nil
}

func (ix *Index) Find(_ context.Context, query string, limit int) []Match {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil
	}
	if limit <= 0 {
		limit = 20
	}
	best := map[int]Match{}
	consider := func(i int, score float64, how string) {
		score += ix.entries[i].rank
		if m, ok := best[i]; !ok || m.Score < score {
			best[i] = Match{Score: score, How: how}
		}
	}
	if LooksLikeDesignation(query) {
		if i, ok := ix.byKey[Key(query)]; ok {
			consider(i, scoreDesignation, "designation")
		}
	}
	q := NormalizeName(query)
	core := CoreName(query)
	coreTri := trigrams(core)
	qKey := strings.ToUpper(strings.ReplaceAll(q, " ", ""))
	for _, i := range ix.byName[q] {
		consider(i, scoreExactName, "name")
	}
	for i, e := range ix.entries {
		for j, n := range e.names {
			c := e.cores[j]
			switch {
			case core != "" && c == core:
				consider(i, scoreCoreName, "name")
			case strings.HasPrefix(n, q):
				consider(i, scorePrefix, "prefix")
			case len(q) >= 3 && strings.Contains(n, q):
				consider(i, scoreSubstring, "contains")
			case len(core) >= 4:
				if s := trigramSimilarity(e.tris[j], coreTri); s >= fuzzyFloor {
					consider(i, 30*s, "similar")
				}
			}
		}
		if len(qKey) >= 2 {
			for _, k := range e.keys {
				if strings.HasPrefix(k, qKey) {
					consider(i, scorePrefix-5, "prefix")
				}
			}
		}
	}
	out := make([]Match, 0, len(best))
	for i, m := range best {
		m.Object = ix.objects[i]
		out = append(out, m)
	}
	slices.SortFunc(out, func(a, b Match) int {
		if a.Score != b.Score {
			if a.Score > b.Score {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Object.ID, b.Object.ID)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (ix *Index) Near(ctx context.Context, ra, dec, radius float64, limit int) []Match {
	if limit <= 0 {
		limit = 20
	}
	objs, _ := ix.Cone(ctx, ra, dec, radius)
	if len(objs) > limit {
		objs = objs[:limit]
	}
	out := make([]Match, len(objs))
	for i, o := range objs {
		d := Separation(ra, dec, o.RA, o.Dec)
		out[i] = Match{Object: o, Score: math.Round(100*(1-d/radius)*100) / 100, How: "coordinates"}
	}
	return out
}

func Separation(ra1, dec1, ra2, dec2 float64) float64 {
	const rad = math.Pi / 180
	d1, d2 := dec1*rad, dec2*rad
	dra := (ra2 - ra1) * rad
	a := math.Sin((d2-d1)/2)*math.Sin((d2-d1)/2) + math.Cos(d1)*math.Cos(d2)*math.Sin(dra/2)*math.Sin(dra/2)
	return 2 * math.Asin(math.Min(1, math.Sqrt(a))) / rad
}
