package cmd

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/starcat"
	"github.com/minio/minio-go/v7"
	miniocreds "github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/spf13/cobra"
)

const (
	gaiaSourceURL = "https://cdn.gea.esac.esa.int/Gaia/gdr3/gaia_source/"
	vsxSourceURL  = "https://cdsarc.cds.unistra.fr/ftp/B/vsx/"
	vizierTSVURL  = "https://vizier.cds.unistra.fr/viz-bin/asu-tsv"
	catalogUA     = "astro-stacker/1 (+https://github.com/USA-RedDragon/astro-stacker)"
)

type mirrorFlags struct {
	endpoint, bucket, prefix, baseURL string
	ssl, verify                       bool
	workers, limit                    int
	rateMB                            float64
}

// newCatalogMirrorCommand mirrors Gaia DR3 or VSX into S3. Credentials
// come from CATALOG_S3_ACCESS_KEY and CATALOG_S3_SECRET_KEY.
func newCatalogMirrorCommand() *cobra.Command {
	c := &cobra.Command{Use: "catalog-mirror", Short: "Mirror Gaia DR3 or AAVSO VSX into S3 for local catalogue queries"}
	add := func(use, short, prefix, base string, run func(context.Context, starcat.MirrorOptions) error) {
		var f mirrorFlags
		sc := &cobra.Command{
			Use:          use,
			Short:        short,
			SilenceUsage: true,
			RunE: func(cmd *cobra.Command, _ []string) error {
				b, err := envBucket(f.endpoint, f.ssl, f.bucket, "CATALOG_S3_ACCESS_KEY", "CATALOG_S3_SECRET_KEY")
				if err != nil {
					return err
				}
				return run(cmd.Context(), starcat.MirrorOptions{Bucket: b, Prefix: f.prefix, BaseURL: f.baseURL, Workers: f.workers,
					Limit: f.limit, Verify: f.verify, Limiter: starcat.NewLimiter(f.rateMB * 1e6)})
			},
		}
		sc.Flags().StringVar(&f.endpoint, "s3-endpoint", "minio.minio.svc:9000", "S3 endpoint host")
		sc.Flags().BoolVar(&f.ssl, "s3-ssl", false, "use HTTPS for S3")
		sc.Flags().StringVar(&f.bucket, "bucket", "sky-survey", "bucket")
		sc.Flags().StringVar(&f.prefix, "prefix", prefix, "path of the mirror in the bucket")
		sc.Flags().StringVar(&f.baseURL, "base-url", base, "source directory URL, with a trailing slash")
		sc.Flags().IntVar(&f.workers, "workers", 4, "files downloaded at once")
		sc.Flags().IntVar(&f.limit, "limit", 0, "stop after this many new files; 0 does all")
		sc.Flags().Float64Var(&f.rateMB, "rate-mb", 50, "total download rate cap in MB/s; 0 is unlimited")
		sc.Flags().BoolVar(&f.verify, "verify", true, "read every piece back before writing the manifest")
		c.AddCommand(sc)
	}
	add("gaia", "Mirror ESA's Gaia DR3 gaia_source files", "catalogs/gaia-dr3/v1", gaiaSourceURL, starcat.MirrorGaia)
	add("vsx", "Mirror CDS's copy of VSX (B/vsx) when it has a new version", "catalogs/vsx", vsxSourceURL, starcat.MirrorVSX)
	return c
}

func envBucket(endpoint string, ssl bool, bucket, keyEnv, secretEnv string) (starcat.MinioBucket, error) {
	key, secret := os.Getenv(keyEnv), os.Getenv(secretEnv)
	if key == "" || secret == "" {
		return starcat.MinioBucket{}, fmt.Errorf("set %s and %s", keyEnv, secretEnv)
	}
	cl, err := minio.New(endpoint, &minio.Options{Creds: miniocreds.NewStaticV4(key, secret, ""), Secure: ssl, Region: "us-east-1"})
	if err != nil {
		return starcat.MinioBucket{}, err
	}
	return starcat.MinioBucket{Client: cl, Name: bucket}, nil
}

// newCatalogCheckCommand compares the local Gaia and VSX mirrors with
// VizieR over one field, row by row, and times both. Credentials come from
// S3_ACCESS_KEY and S3_SECRET_KEY.
func newCatalogCheckCommand() *cobra.Command {
	var endpoint, bucket, gaiaPrefix, vsxPrefix, name string
	var ssl bool
	var ra, dec, radius, faint float64
	c := &cobra.Command{
		Use:          "catalog-check",
		Short:        "Compare the local Gaia DR3 and VSX mirrors with VizieR over a field",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			b, err := envBucket(endpoint, ssl, bucket, "S3_ACCESS_KEY", "S3_SECRET_KEY")
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "field %s: RA %.5f Dec %+.5f radius %.4f deg\n", name, ra, dec, radius)
			if err := checkGaia(ctx, out, starcat.NewGaia(b, gaiaPrefix), ra, dec, radius, faint); err != nil {
				return err
			}
			return checkVSX(ctx, out, starcat.NewVSX(b, vsxPrefix), ra, dec, radius)
		},
	}
	c.Flags().StringVar(&endpoint, "s3-endpoint", "minio.minio.svc:9000", "S3 endpoint host")
	c.Flags().BoolVar(&ssl, "s3-ssl", false, "use HTTPS for S3")
	c.Flags().StringVar(&bucket, "bucket", "sky-survey", "bucket")
	c.Flags().StringVar(&gaiaPrefix, "gaia-prefix", "catalogs/gaia-dr3/v1", "Gaia mirror path")
	c.Flags().StringVar(&vsxPrefix, "vsx-prefix", "catalogs/vsx", "VSX mirror path")
	c.Flags().StringVar(&name, "name", "", "field name for the report")
	c.Flags().Float64Var(&ra, "ra", 0, "field centre RA, degrees")
	c.Flags().Float64Var(&dec, "dec", 0, "field centre Dec, degrees")
	c.Flags().Float64Var(&radius, "radius", 1, "cone radius, degrees")
	c.Flags().Float64Var(&faint, "faint", 21.5, "faintest Gaia G compared")
	return c
}

func vizierRows(ctx context.Context, q url.Values) ([]map[string]string, time.Duration, error) {
	t0 := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, vizierTSVURL+"?"+q.Encode(), nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", catalogUA)
	resp, err := (&http.Client{Timeout: 20 * time.Minute}).Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("vizier: %s", resp.Status)
	}
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	var cols []string
	var rows []map[string]string
	state := 0
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		switch state {
		case 0:
			for _, c := range f {
				cols = append(cols, strings.TrimSpace(c))
			}
			state = 1
		case 1:
			if strings.HasPrefix(strings.TrimSpace(f[0]), "-") {
				state = 2
			}
		default:
			r := map[string]string{}
			for i, c := range cols {
				if i < len(f) {
					r[c] = strings.TrimSpace(f[i])
				}
			}
			rows = append(rows, r)
		}
	}
	if state == 0 {
		return nil, 0, errors.New("vizier: no table in the response")
	}
	return rows, time.Since(t0), sc.Err()
}

// sameAtPrecision says whether a local value printed with VizieR's
// decimals is VizieR's value: blank matches NaN.
func sameAtPrecision(viz string, local float64) bool {
	if viz == "" {
		return math.IsNaN(local)
	}
	if math.IsNaN(local) {
		return false
	}
	d := 0
	if i := strings.IndexByte(viz, '.'); i >= 0 {
		d = len(viz) - i - 1
	}
	v, err := strconv.ParseFloat(viz, 64)
	if err != nil {
		return false
	}
	l, _ := strconv.ParseFloat(strconv.FormatFloat(local, 'f', d, 64), 64)
	return v == l
}

type diffs struct {
	cols  map[string]int
	first map[string]string
}

func (d *diffs) add(col, id, viz string, local any) {
	if d.cols == nil {
		d.cols, d.first = map[string]int{}, map[string]string{}
	}
	d.cols[col]++
	if _, ok := d.first[col]; !ok {
		d.first[col] = fmt.Sprintf("%s: VizieR %q, local %v", id, viz, local)
	}
}

func (d *diffs) report(out io.Writer) {
	if len(d.cols) == 0 {
		fmt.Fprintln(out, "  every value matches at VizieR's printed precision")
		return
	}
	keys := make([]string, 0, len(d.cols))
	for k := range d.cols {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(out, "  %s differs in %d rows, first %s\n", k, d.cols[k], d.first[k])
	}
}

func checkGaia(ctx context.Context, out io.Writer, g *starcat.Gaia, ra, dec, radius, faint float64) error {
	keep := func(s *starcat.GaiaStar) bool { return !math.IsNaN(float64(s.G)) && float64(s.G) < faint }
	t0 := time.Now()
	local, err := g.Cone(ctx, ra, dec, radius, keep)
	if err != nil {
		return err
	}
	tl := time.Since(t0)
	q := url.Values{}
	q.Set("-source", "I/355/gaiadr3")
	q.Set("-c", fmt.Sprintf("%.6f %+.6f", ra, dec))
	q.Set("-c.rd", fmt.Sprintf("%.4f", radius))
	q.Set("-out", "Source,RA_ICRS,DE_ICRS,e_RA_ICRS,e_DE_ICRS,Plx,e_Plx,pmRA,e_pmRA,pmDE,e_pmDE,RUWE,Gmag,BPmag,RPmag,VarFlag")
	q.Set("Gmag", fmt.Sprintf("<%g", faint))
	q.Set("-out.max", "5000000")
	viz, tv, err := vizierRows(ctx, q)
	if err != nil {
		fmt.Fprintf(out, "gaia: local %d stars in %s; VizieR failed: %v\n", len(local), tl.Round(time.Millisecond), err)
		return nil
	}
	fmt.Fprintf(out, "gaia G<%g: local %d stars in %s, VizieR %d in %s\n", faint, len(local), tl.Round(time.Millisecond), len(viz), tv.Round(time.Millisecond))
	byID := make(map[int64]*starcat.GaiaStar, len(local))
	for i := range local {
		byID[local[i].SourceID] = &local[i]
	}
	var d diffs
	seen := map[int64]bool{}
	missing := 0
	for _, r := range viz {
		id, err := strconv.ParseInt(r["Source"], 10, 64)
		if err != nil {
			return fmt.Errorf("vizier source %q: %w", r["Source"], err)
		}
		seen[id] = true
		s, ok := byID[id]
		if !ok {
			missing++
			if missing <= 5 {
				fmt.Fprintf(out, "  only in VizieR: %d at %s %s G %s\n", id, r["RA_ICRS"], r["DE_ICRS"], r["Gmag"])
			}
			continue
		}
		sid := strconv.FormatInt(id, 10)
		for _, c := range []struct {
			col string
			v   float64
		}{{"RA_ICRS", s.RA}, {"DE_ICRS", s.Dec}, {"e_RA_ICRS", float64(s.RAError)}, {"e_DE_ICRS", float64(s.DecError)},
			{"Plx", s.Parallax}, {"e_Plx", float64(s.ParallaxError)}, {"pmRA", s.PMRA}, {"e_pmRA", float64(s.PMRAError)},
			{"pmDE", s.PMDec}, {"e_pmDE", float64(s.PMDecError)}, {"RUWE", float64(s.RUWE)}, {"Gmag", float64(s.G)},
			{"BPmag", float64(s.BP)}, {"RPmag", float64(s.RP)}} {
			if !sameAtPrecision(r[c.col], c.v) {
				d.add(c.col, sid, r[c.col], c.v)
			}
		}
		flag := map[uint8]string{starcat.VariableNotAvailable: "NOT_AVAILABLE", starcat.VariableVariable: "VARIABLE"}[s.Variable]
		if r["VarFlag"] != flag {
			d.add("VarFlag", sid, r["VarFlag"], flag)
		}
	}
	extra := 0
	for _, s := range local {
		if !seen[s.SourceID] {
			extra++
			if extra <= 5 {
				fmt.Fprintf(out, "  only local: %d at %.11f %+.11f G %.6f, %.9f deg from the centre\n", s.SourceID, s.RA, s.Dec, s.G, starcat.Separation(ra, dec, s.RA, s.Dec))
			}
		}
	}
	fmt.Fprintf(out, "  same source_ids: %d; only in VizieR: %d; only local: %d\n", len(viz)-missing, missing, extra)
	d.report(out)
	return nil
}

func checkVSX(ctx context.Context, out io.Writer, v *starcat.VSX, ra, dec, radius float64) error {
	t0 := time.Now()
	local, err := v.Cone(ctx, ra, dec, radius, nil)
	if err != nil {
		return err
	}
	tl := time.Since(t0)
	q := url.Values{}
	q.Set("-source", "B/vsx/vsx")
	q.Set("-c", fmt.Sprintf("%.6f %+.6f", ra, dec))
	q.Set("-c.rd", fmt.Sprintf("%.4f", radius))
	q.Set("-out", "OID,Name,V,RAJ2000,DEJ2000,Type,l_max,max,u_max,n_max,f_min,l_min,min,u_min,n_min,Epoch,u_Epoch,l_Period,Period,u_Period,Sp")
	q.Set("-out.max", "1000000")
	viz, tv, err := vizierRows(ctx, q)
	if err != nil {
		fmt.Fprintf(out, "vsx: local %d stars in %s; VizieR failed: %v\n", len(local), tl.Round(time.Millisecond), err)
		return nil
	}
	fmt.Fprintf(out, "vsx: local %d stars in %s, VizieR %d in %s\n", len(local), tl.Round(time.Millisecond), len(viz), tv.Round(time.Millisecond))
	byID := make(map[int64]*starcat.VSXStar, len(local))
	for i := range local {
		byID[local[i].OID] = &local[i]
	}
	var d diffs
	seen := map[int64]bool{}
	missing := 0
	for _, r := range viz {
		id, err := strconv.ParseInt(r["OID"], 10, 64)
		if err != nil {
			return fmt.Errorf("vizier OID %q: %w", r["OID"], err)
		}
		seen[id] = true
		s, ok := byID[id]
		if !ok {
			missing++
			if missing <= 5 {
				fmt.Fprintf(out, "  only in VizieR: OID %d %s\n", id, r["Name"])
			}
			continue
		}
		sid := "OID " + strconv.FormatInt(id, 10)
		vflag := ""
		if s.V != starcat.VSXNoFlag {
			vflag = strconv.Itoa(int(s.V))
		}
		for _, c := range []struct{ col, v string }{{"Name", s.Name}, {"V", vflag}, {"Type", s.Type}, {"l_max", s.LMax}, {"u_max", s.UMax},
			{"n_max", s.NMax}, {"f_min", s.FMin}, {"l_min", s.LMin}, {"u_min", s.UMin}, {"n_min", s.NMin}, {"u_Epoch", s.UEpoch},
			{"l_Period", s.LPeriod}, {"u_Period", s.UPeriod}, {"Sp", s.SpectralTy}} {
			if r[c.col] != c.v {
				d.add(c.col, sid, r[c.col], c.v)
			}
		}
		for _, c := range []struct {
			col string
			v   float64
		}{{"RAJ2000", s.RA}, {"DEJ2000", s.Dec}, {"max", s.Max}, {"min", s.Min}, {"Epoch", s.Epoch}, {"Period", s.Period}} {
			if !sameAtPrecision(r[c.col], c.v) {
				d.add(c.col, sid, r[c.col], c.v)
			}
		}
	}
	extra := 0
	for _, s := range local {
		if !seen[s.OID] {
			extra++
			if extra <= 5 {
				fmt.Fprintf(out, "  only local: OID %d %s\n", s.OID, s.Name)
			}
		}
	}
	fmt.Fprintf(out, "  same OIDs: %d; only in VizieR: %d; only local: %d\n", len(viz)-missing, missing, extra)
	d.report(out)
	return nil
}
