package main

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
	"github.com/klauspost/compress/zstd"
)

//go:embed namereviewed.tsv
var namesReviewed string

const (
	simbadTAP     = "https://simbad.cds.unistra.fr/simbad/sim-tap/sync"
	simbadBatch   = 150
	auditSlackDeg = 0.1
	auditMinSep   = 0.05
)

var errUnreviewedNames = errors.New("nicknames that SIMBAD places on another object; fix them in namefixes.tsv or list them in namereviewed.tsv")

type simbadHit struct {
	MainID string
	RA     float64
	Dec    float64
}

func (b *builder) simbad(ctx context.Context, ids []string) (map[string]simbadHit, error) {
	out := map[string]simbadHit{}
	for i := 0; i < len(ids); i += simbadBatch {
		chunk := ids[i:min(len(ids), i+simbadBatch)]
		quoted := make([]string, len(chunk))
		for k, id := range chunk {
			quoted[k] = "'" + strings.ReplaceAll(id, "'", "''") + "'"
		}
		q := "SELECT i.id, b.main_id, b.ra, b.dec FROM ident AS i JOIN basic AS b ON b.oid = i.oidref WHERE i.id IN (" + strings.Join(quoted, ",") + ")"
		v := url.Values{"REQUEST": {"doQuery"}, "LANG": {"ADQL"}, "FORMAT": {"json"}, "QUERY": {q}}
		sum := sha256.Sum256([]byte(q))
		data, err := b.post(ctx, simbadTAP, v, "simbad-"+hex.EncodeToString(sum[:8])+".json")
		if err != nil {
			return nil, err
		}
		var res struct {
			Data [][]any `json:"data"`
		}
		if err := json.Unmarshal(data, &res); err != nil {
			return nil, fmt.Errorf("simbad: %w", err)
		}
		for _, r := range res.Data {
			id, _ := r[0].(string)
			main, _ := r[1].(string)
			ra, ok1 := r[2].(float64)
			dec, ok2 := r[3].(float64)
			if ok1 && ok2 {
				out[id] = simbadHit{MainID: main, RA: ra, Dec: dec}
			}
		}
	}
	return out, nil
}

func nicknames(o catalog.Object) []string {
	var out []string
	if o.Name != "" {
		out = append(out, o.Name)
	}
	for _, a := range o.Aliases {
		if !catalog.LooksLikeDesignation(a) {
			out = append(out, a)
		}
	}
	return out
}

func reviewedNames() map[string]bool {
	out := map[string]bool{}
	for line := range strings.SplitSeq(namesReviewed, "\n") {
		p := strings.Split(line, "\t")
		if len(p) >= 2 {
			out[p[0]+"\t"+p[1]] = true
		}
	}
	return out
}

func loadBuilt(catalogPath, ninaPath string) ([]catalog.Object, error) {
	raw, err := os.ReadFile(catalogPath)
	if err != nil {
		return nil, err
	}
	data, err := decompressFile(raw)
	if err != nil {
		return nil, err
	}
	var ds catalog.Dataset
	if err := json.Unmarshal(data, &ds); err != nil {
		return nil, err
	}
	if ninaPath != "" {
		raw, err := os.ReadFile(ninaPath)
		if err != nil {
			return nil, err
		}
		data, err := decompressFile(raw)
		if err != nil {
			return nil, err
		}
		var ov catalog.Overlay
		if err := json.Unmarshal(data, &ov); err != nil {
			return nil, err
		}
		ds.Apply(ov)
	}
	return ds.Objects, nil
}

func (b *builder) auditNames(ctx context.Context, objs []catalog.Object) error {
	owners := map[string][]string{}
	var ids []string
	seen := map[string]bool{}
	for _, o := range objs {
		for _, n := range nicknames(o) {
			k := catalog.NormalizeName(n)
			if !slices.Contains(owners[k], o.Designation) {
				owners[k] = append(owners[k], o.Designation)
			}
			if !seen[n] {
				seen[n] = true
				ids = append(ids, "NAME "+n)
			}
		}
	}
	sort.Strings(ids)
	hits, err := b.simbad(ctx, ids)
	if err != nil {
		return err
	}
	reviewed := reviewedNames()
	var flags []string
	for k, o := range owners {
		if len(o) > 1 {
			flags = append(flags, fmt.Sprintf("duplicate\t%s\t%s", k, strings.Join(o, ", ")))
		}
	}
	checked := 0
	for _, o := range objs {
		for _, n := range nicknames(o) {
			h, ok := hits["NAME "+n]
			if !ok {
				continue
			}
			checked++
			if f := judge(o, n, h, objs); f != "" && !reviewed[o.Designation+"\t"+n] {
				flags = append(flags, f)
			}
		}
	}
	sort.Strings(flags)
	log.Printf("nickname audit: %d nicknames, %d placed by SIMBAD and checked, %d flagged", len(ids), checked, len(flags))
	for _, f := range flags {
		log.Print(f)
	}
	if len(flags) > 0 {
		return errUnreviewedNames
	}
	return nil
}

func simbadKey(id string) string {
	id = strings.Join(strings.Fields(id), " ")
	id = strings.TrimPrefix(id, "Cl ")
	for from, to := range map[string]string{"Melotte ": "Mel ", "Collinder ": "Cr ", "Barnard ": "B ", "SH 2-": "Sh2-", "VdB ": "vdB ", pACO: pACO} {
		if strings.HasPrefix(id, from) {
			id = to + strings.TrimPrefix(id, from)
		}
	}
	return catalog.Key(id)
}

func ownID(o catalog.Object, h simbadHit) bool {
	k := simbadKey(h.MainID)
	if catalog.Key(o.Designation) == k {
		return true
	}
	for _, a := range o.Aliases {
		if catalog.Key(a) == k {
			return true
		}
	}
	return false
}

func judge(o catalog.Object, name string, h simbadHit, objs []catalog.Object) string {
	if ownID(o, h) {
		return ""
	}
	d := sep(o.RA, o.Dec, h.RA, h.Dec)
	radius := math.Max(o.MajorArcmin, 1) / 120
	nearest, nd := o, d
	for _, c := range objs {
		if cd := sep(c.RA, c.Dec, h.RA, h.Dec); cd < nd {
			nearest, nd = c, cd
		}
	}
	far := d > radius+auditSlackDeg
	closer := nearest.ID != o.ID && nd < d/2 && d > auditMinSep
	if !far && !closer {
		return ""
	}
	return fmt.Sprintf("%s\t%s\tSIMBAD %s is %.2f° away (radius %.2f°); nearest is %s %s at %.2f°", o.Designation, name, strings.Join(strings.Fields(h.MainID), " "), d, radius, nearest.Designation, nearest.Name, nd)
}

func decompressFile(b []byte) ([]byte, error) {
	dec, err := zstd.NewReader(nil)
	if err != nil {
		return nil, err
	}
	defer dec.Close()
	return dec.DecodeAll(b, nil)
}

func (b *builder) post(ctx context.Context, endpoint string, form url.Values, name string) ([]byte, error) {
	if b.cache != "" {
		if data, err := os.ReadFile(b.cache + "/" + name); err == nil {
			return data, nil
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "astro-stacker catalogue builder (github.com/USA-RedDragon/astro-stacker)")
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s: %.300s", endpoint, resp.Status, data)
	}
	if b.cache != "" {
		if err := os.MkdirAll(b.cache, 0o750); err == nil {
			_ = os.WriteFile(b.cache+"/"+name, data, 0o600)
		}
	}
	return data, nil
}
