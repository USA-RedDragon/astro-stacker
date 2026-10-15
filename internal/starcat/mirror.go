package starcat

import (
	"bufio"
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/klauspost/compress/gzip"
	"golang.org/x/sync/errgroup"
)

const (
	// GaiaOrder is the HEALPix order the Gaia mirror is partitioned at.
	GaiaOrder = 6
	// VSXOrder is the HEALPix order the VSX mirror is partitioned at.
	VSXOrder     = 3
	userAgent    = "astro-stacker-mirror/1 (+https://github.com/USA-RedDragon/astro-stacker)"
	stallTimeout = 2 * time.Minute
	attempts     = 6
)

// Limiter caps the bytes per second shared by every download.
type Limiter struct {
	mu   sync.Mutex
	rate float64
	next time.Time
}

// NewLimiter allows bytesPerSecond in total; 0 is unlimited.
func NewLimiter(bytesPerSecond float64) *Limiter { return &Limiter{rate: bytesPerSecond} }

func (l *Limiter) wait(ctx context.Context, n int) error {
	if l == nil || l.rate <= 0 {
		return nil
	}
	l.mu.Lock()
	now := time.Now()
	if l.next.Before(now) {
		l.next = now
	}
	at := l.next
	l.next = l.next.Add(time.Duration(float64(n) / l.rate * float64(time.Second)))
	l.mu.Unlock()
	d := time.Until(at)
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// countingReader counts and rate limits a download, and calls touch on
// every read so a watchdog can cancel a stalled one.
type countingReader struct {
	r     io.Reader
	wait  func(n int) error
	n     *atomic.Int64
	total *atomic.Int64
	touch func()
}

func (c *countingReader) Read(p []byte) (int, error) {
	if len(p) > 256<<10 {
		p = p[:256<<10]
	}
	n, err := c.r.Read(p)
	if n > 0 {
		c.n.Add(int64(n))
		c.total.Add(int64(n))
		c.touch()
		if werr := c.wait(n); werr != nil {
			return n, werr
		}
	}
	return n, err
}

// MirrorOptions configures a mirror run.
type MirrorOptions struct {
	Bucket  Bucket
	Prefix  string
	BaseURL string
	Client  *http.Client
	Limiter *Limiter
	Workers int
	// Limit stops after this many new input files; 0 does them all.
	Limit int
	// Verify re-reads every stored piece before the manifest is written.
	Verify bool
}

func (o *MirrorOptions) client() *http.Client {
	if o.Client != nil {
		return o.Client
	}
	return &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, ResponseHeaderTimeout: time.Minute,
		IdleConnTimeout: 90 * time.Second, MaxIdleConnsPerHost: 16, ForceAttemptHTTP2: true}}
}

// download streams url through fn, rate limited, cancelling it when no
// byte arrives for stallTimeout.
func (o *MirrorOptions) download(ctx context.Context, url string, counter, total *atomic.Int64, fn func(io.Reader, *http.Response) error) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	var last atomic.Int64
	last.Store(time.Now().UnixNano())
	done := make(chan struct{})
	defer close(done)
	go func() {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if time.Since(time.Unix(0, last.Load())) > stallTimeout {
					cancel(fmt.Errorf("no data for %s", stallTimeout))
					return
				}
			}
		}
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := o.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	cr := &countingReader{r: resp.Body, wait: func(n int) error { return o.Limiter.wait(ctx, n) }, n: counter, total: total, touch: func() { last.Store(time.Now().UnixNano()) }}
	err = fn(cr, resp)
	if c := context.Cause(ctx); err != nil && c != nil && !errors.Is(c, context.Canceled) {
		return fmt.Errorf("%w (%w)", err, c)
	}
	return err
}

func retry(ctx context.Context, what string, fn func() error) error {
	var err error
	for a := 1; a <= attempts; a++ {
		if err = fn(); err == nil || ctx.Err() != nil {
			return err
		}
		slog.Warn("Mirror step failed; retrying", "step", what, "attempt", a, "error", err)
		select {
		case <-time.After(time.Duration(a*a) * 15 * time.Second):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return fmt.Errorf("%s: %w", what, err)
}

type gaiaFile struct {
	Name string
	MD5  string
}

// doneMarker records one input file stored in full.
type doneMarker struct {
	Input  Input   `json:"input"`
	Pieces []Piece `json:"pieces"`
	Stored time.Time
}

var md5Line = regexp.MustCompile(`^([0-9a-f]{32})\s+(GaiaSource_\d{6}-\d{6}\.csv\.gz)$`)

func parseMD5Sums(b []byte) ([]gaiaFile, error) {
	var out []gaiaFile
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if m := md5Line.FindStringSubmatch(line); m != nil {
			out = append(out, gaiaFile{Name: m[2], MD5: m[1]})
		} else if line != "" && !strings.HasSuffix(line, "_MD5SUM.txt") {
			return nil, fmt.Errorf("gaia: unexpected checksum line %q", line)
		}
	}
	return out, sc.Err()
}

func (o *MirrorOptions) fetchSmall(ctx context.Context, url string) ([]byte, error) {
	var b []byte
	var c, t atomic.Int64
	err := retry(ctx, url, func() error {
		return o.download(ctx, url, &c, &t, func(r io.Reader, _ *http.Response) error {
			var err error
			b, err = io.ReadAll(io.LimitReader(r, 64<<20))
			return err
		})
	})
	return b, err
}

// MirrorGaia mirrors ESA's gaia_source files: each file is downloaded,
// checked against ESA's MD5, split into pieces by HEALPix order-6 pixel of
// each star's position and stored before the next; a done marker per file
// makes reruns skip it. Once every file is stored the manifest is written.
func MirrorGaia(ctx context.Context, o MirrorOptions) error {
	sums, err := o.fetchSmall(ctx, o.BaseURL+"_MD5SUM.txt")
	if err != nil {
		return err
	}
	files, err := parseMD5Sums(sums)
	if err != nil {
		return err
	}
	if err := o.Bucket.Put(ctx, path.Join(o.Prefix, "source", "_MD5SUM.txt"), sums); err != nil {
		return err
	}
	done, err := o.doneMarkers(ctx)
	if err != nil {
		return err
	}
	var todo []gaiaFile
	for _, f := range files {
		if _, ok := done[f.Name]; !ok {
			todo = append(todo, f)
		}
	}
	slog.Info("Gaia mirror", "files", len(files), "done", len(done), "to do", len(todo))
	if o.Limit > 0 && len(todo) > o.Limit {
		todo = todo[:o.Limit]
	}
	var downloaded, finished, rows atomic.Int64
	start := time.Now()
	stop := make(chan struct{})
	go progress(stop, start, &downloaded, &finished, len(todo), len(files)-len(done))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(max(1, o.Workers))
	for _, f := range todo {
		g.Go(func() error {
			m, err := o.gaiaFile(gctx, f, &downloaded)
			if err != nil {
				return err
			}
			finished.Add(1)
			rows.Add(m.Input.Rows)
			return nil
		})
	}
	err = g.Wait()
	close(stop)
	if err != nil {
		return err
	}
	el := time.Since(start)
	slog.Info("Gaia files stored", "files", len(todo), "rows", rows.Load(), "downloaded MB", downloaded.Load()>>20,
		"took", el.Round(time.Second), "MB/s", fmt.Sprintf("%.1f", float64(downloaded.Load())/1e6/el.Seconds()))
	done, err = o.doneMarkers(ctx)
	if err != nil {
		return err
	}
	if len(done) < len(files) {
		slog.Info("Gaia mirror incomplete; manifest not written", "done", len(done), "files", len(files))
		return nil
	}
	return o.writeManifest(ctx, files, done)
}

func progress(stop <-chan struct{}, start time.Time, downloaded, finished *atomic.Int64, todo, remaining int) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	var prev int64
	prevAt := start
	for {
		select {
		case <-stop:
			return
		case now := <-t.C:
			d := downloaded.Load()
			f := finished.Load()
			rate := float64(d-prev) / now.Sub(prevAt).Seconds()
			avg := float64(d) / now.Sub(start).Seconds()
			eta := "unknown"
			if f > 0 {
				eta = time.Duration(float64(now.Sub(start)) / float64(f) * float64(int64(todo)-f)).Round(time.Minute).String()
			}
			slog.Info("Gaia mirror progress", "files done", f, "of", todo, "remaining before run", remaining,
				"MB/s last minute", fmt.Sprintf("%.1f", rate/1e6), "MB/s average", fmt.Sprintf("%.1f", avg/1e6), "eta", eta)
			prev, prevAt = d, now
		}
	}
}

func (o *MirrorOptions) doneMarkers(ctx context.Context) (map[string]string, error) {
	keys, err := o.Bucket.List(ctx, path.Join(o.Prefix, "done")+"/")
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, k := range keys {
		out[strings.TrimSuffix(path.Base(k), ".json")] = k
	}
	return out, nil
}

func (o *MirrorOptions) gaiaFile(ctx context.Context, f gaiaFile, downloaded *atomic.Int64) (*doneMarker, error) {
	var m *doneMarker
	err := retry(ctx, f.Name, func() error {
		var err error
		m, err = o.gaiaFileOnce(ctx, f, downloaded)
		return err
	})
	return m, err
}

func (o *MirrorOptions) gaiaFileOnce(ctx context.Context, f gaiaFile, downloaded *atomic.Int64) (*doneMarker, error) {
	t0 := time.Now()
	parts := map[int64][]GaiaStar{}
	var n atomic.Int64
	h := md5.New()
	var rows int
	err := o.download(ctx, o.BaseURL+f.Name, &n, downloaded, func(r io.Reader, _ *http.Response) error {
		tee := io.TeeReader(r, h)
		zr, err := gzip.NewReader(tee)
		if err != nil {
			return err
		}
		zr.Multistream(true)
		rows, err = ParseGaiaECSV(zr, func(s GaiaStar) error {
			p := Pixel(GaiaOrder, s.RA, s.Dec)
			parts[p] = append(parts[p], s)
			return nil
		})
		if err != nil {
			return err
		}
		_, err = io.Copy(io.Discard, tee)
		return err
	})
	if err != nil {
		return nil, err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != f.MD5 {
		return nil, fmt.Errorf("%s: MD5 %s, ESA lists %s", f.Name, got, f.MD5)
	}
	tDown := time.Since(t0)
	stem := strings.TrimSuffix(f.Name, ".csv.gz")
	m := &doneMarker{Input: Input{File: f.Name, MD5: f.MD5, Bytes: n.Load(), Rows: int64(rows)}}
	keys := make([]int64, 0, len(parts))
	for p := range parts {
		keys = append(keys, p)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	var stored int64
	for _, p := range keys {
		pc, data, err := gaiaPiece(o.Prefix, p, stem, parts[p])
		if err != nil {
			return nil, err
		}
		if err := o.Bucket.Put(ctx, pc.Key, data); err != nil {
			return nil, err
		}
		stored += pc.Bytes
		m.Pieces = append(m.Pieces, pc)
	}
	m.Stored = time.Now().UTC()
	j, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	if err := o.Bucket.Put(ctx, path.Join(o.Prefix, "done", f.Name+".json"), j); err != nil {
		return nil, err
	}
	slog.Info("Gaia file stored", "file", f.Name, "rows", rows, "pieces", len(m.Pieces), "downloaded MB", n.Load()>>20,
		"stored MB", fmt.Sprintf("%.1f", float64(stored)/1e6), "download s", tDown.Round(time.Second).Seconds(),
		"total s", time.Since(t0).Round(time.Second).Seconds())
	return m, nil
}

// gaiaPiece encodes one partition's stars from one input file, checking
// each lies within its pixel.
func gaiaPiece(prefix string, p int64, stem string, stars []GaiaStar) (Piece, []byte, error) {
	c := pixelCenter(GaiaOrder, p)
	lim := MaxPixelRadius(GaiaOrder) + coverSlack
	for i := range stars {
		if Angle(c, Vec(stars[i].RA, stars[i].Dec)) > lim {
			return Piece{}, nil, fmt.Errorf("gaia: source %d lies outside pixel %d", stars[i].SourceID, p)
		}
	}
	data, err := encodePiece(gaiaSchema(), stars)
	if err != nil {
		return Piece{}, nil, err
	}
	sum := sha256.Sum256(data)
	key := path.Join(prefix, "order"+strconv.Itoa(GaiaOrder), strconv.FormatInt(p, 10), stem+".scat")
	return Piece{Partition: p, Key: key, Rows: int64(len(stars)), Bytes: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}, data, nil
}

func (o *MirrorOptions) writeManifest(ctx context.Context, files []gaiaFile, done map[string]string) error {
	man := &Manifest{Catalog: "gaia-dr3", Schema: gaiaSchema().name, Order: GaiaOrder, Epoch: GaiaEpoch, Version: "DR3",
		Source: o.BaseURL, Created: time.Now().UTC()}
	for _, f := range files {
		b, err := o.Bucket.Get(ctx, done[f.Name])
		if err != nil {
			return err
		}
		var m doneMarker
		if err := json.Unmarshal(b, &m); err != nil {
			return fmt.Errorf("%s: %w", done[f.Name], err)
		}
		if m.Input.MD5 != f.MD5 {
			return fmt.Errorf("%s was stored from MD5 %s, ESA now lists %s", f.Name, m.Input.MD5, f.MD5)
		}
		var pr int64
		for _, p := range m.Pieces {
			pr += p.Rows
			man.Bytes += p.Bytes
		}
		if pr != m.Input.Rows {
			return fmt.Errorf("%s: pieces hold %d rows, the file %d", f.Name, pr, m.Input.Rows)
		}
		man.Rows += m.Input.Rows
		man.Inputs = append(man.Inputs, m.Input)
		man.Pieces = append(man.Pieces, m.Pieces...)
	}
	if o.Verify {
		if err := verifyPieces(ctx, o.Bucket, gaiaSchema(), GaiaOrder, man.Pieces); err != nil {
			return err
		}
		man.Verified = time.Now().UTC()
	}
	b, err := encodeManifest(man)
	if err != nil {
		return err
	}
	if err := o.Bucket.Put(ctx, path.Join(o.Prefix, manifestName), b); err != nil {
		return err
	}
	slog.Info("Gaia manifest written", "rows", man.Rows, "pieces", len(man.Pieces), "stored GB", fmt.Sprintf("%.2f", float64(man.Bytes)/1e9),
		"verified", !man.Verified.IsZero())
	return nil
}

// verifyPieces reads every piece back: its checksum, its rows, and that
// each row lies within its partition.
func verifyPieces[T any](ctx context.Context, b Bucket, s schema[T], order int, pieces []Piece) error {
	var checked, rows atomic.Int64
	start := time.Now()
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(fetchParallel)
	for _, p := range pieces {
		g.Go(func() error {
			data, err := b.Get(gctx, p.Key)
			if err != nil {
				return err
			}
			if h := sha256.Sum256(data); hex.EncodeToString(h[:]) != p.SHA256 || int64(len(data)) != p.Bytes {
				return fmt.Errorf("%s: checksum or size differs from the marker", p.Key)
			}
			outside := 0
			n, err := decodePiece(s, data, [3]float64{}, -1, func(rs []T) {
				for i := range rs {
					if a, d := s.pos(&rs[i]); Pixel(order, a, d) != p.Partition {
						outside++
					}
				}
			})
			if err != nil {
				return fmt.Errorf("%s: %w", p.Key, err)
			}
			if outside > 0 {
				return fmt.Errorf("%s: %d rows lie outside partition %d", p.Key, outside, p.Partition)
			}
			if int64(n) != p.Rows {
				return fmt.Errorf("%s: %d rows, the marker says %d", p.Key, n, p.Rows)
			}
			rows.Add(int64(n))
			if c := checked.Add(1); c%5000 == 0 {
				slog.Info("Verifying pieces", "checked", c, "of", len(pieces), "rows", rows.Load(), "took", time.Since(start).Round(time.Second))
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}
	slog.Info("Pieces verified", "pieces", len(pieces), "rows", rows.Load(), "took", time.Since(start).Round(time.Second))
	return nil
}

var vsxReadMe = regexp.MustCompile(`(?m)^vsx\.dat\s+(\d+)\s+(\d+)\s+.*Version (\d{4}-\d{2}-\d{2})`)

// MirrorVSX mirrors CDS's vsx.dat when its version is newer than the
// stored one: the rows are split by HEALPix order-3 pixel into a new
// version's pieces, verified, the manifest switched to it, and older
// versions deleted.
func MirrorVSX(ctx context.Context, o MirrorOptions) error {
	readme, err := o.fetchSmall(ctx, o.BaseURL+"ReadMe")
	if err != nil {
		return err
	}
	mm := vsxReadMe.FindSubmatch(readme)
	if mm == nil {
		return errors.New("vsx: no vsx.dat line with a version in the ReadMe")
	}
	lrecl, _ := strconv.Atoi(string(mm[1]))
	records, _ := strconv.ParseInt(string(mm[2]), 10, 64)
	version := string(mm[3])
	if lrecl != vsxLineLen {
		return fmt.Errorf("vsx: record length %d, expected %d", lrecl, vsxLineLen)
	}
	if b, err := o.Bucket.Get(ctx, path.Join(o.Prefix, manifestName)); err == nil {
		if m, err := decodeManifest(b); err == nil && m.Version == version && !m.Verified.IsZero() {
			slog.Info("VSX mirror is current", "version", version, "rows", m.Rows)
			return nil
		}
	}
	vprefix := path.Join(o.Prefix, version)
	parts := map[int64][]byte{}
	var rows int64
	var size int64
	sum := sha256.New()
	var n, total atomic.Int64
	err = retry(ctx, "vsx.dat", func() error {
		clear(parts)
		rows, size = 0, 0
		sum.Reset()
		n.Store(0)
		return o.download(ctx, o.BaseURL+"vsx.dat", &n, &total, func(r io.Reader, resp *http.Response) error {
			sc := bufio.NewScanner(io.TeeReader(r, sum))
			sc.Buffer(make([]byte, 4096), 1<<16)
			for sc.Scan() {
				line := sc.Bytes()
				if len(bytes.TrimSpace(line)) == 0 {
					continue
				}
				s, err := vsxRow(line)
				if err != nil {
					return fmt.Errorf("vsx: line %d: %w", rows+1, err)
				}
				p := Pixel(VSXOrder, s.RA, s.Dec)
				parts[p] = append(append(parts[p], line...), '\n')
				rows++
			}
			if err := sc.Err(); err != nil {
				return err
			}
			size = n.Load()
			if resp.ContentLength >= 0 && size != resp.ContentLength {
				return fmt.Errorf("vsx: read %d bytes of %d", size, resp.ContentLength)
			}
			return nil
		})
	})
	if err != nil {
		return err
	}
	if rows != records {
		return fmt.Errorf("vsx: %d rows, the ReadMe lists %d", rows, records)
	}
	man := &Manifest{Catalog: "vsx", Schema: vsxSchema().name, Order: VSXOrder, Version: version, Source: o.BaseURL + "vsx.dat",
		Created: time.Now().UTC(), Rows: rows, Inputs: []Input{{File: "vsx.dat", SHA256: hex.EncodeToString(sum.Sum(nil)), Bytes: size, Rows: rows}}}
	keys := make([]int64, 0, len(parts))
	for p := range parts {
		keys = append(keys, p)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	for _, p := range keys {
		var stars []VSXStar
		if _, err := ParseVSXDat(bytes.NewReader(parts[p]), func(s VSXStar) error { stars = append(stars, s); return nil }); err != nil {
			return err
		}
		data, err := encodePiece(vsxSchema(), stars)
		if err != nil {
			return err
		}
		h := sha256.Sum256(data)
		pc := Piece{Partition: p, Key: path.Join(vprefix, "order"+strconv.Itoa(VSXOrder), strconv.FormatInt(p, 10)+".scat"),
			Rows: int64(len(stars)), Bytes: int64(len(data)), SHA256: hex.EncodeToString(h[:])}
		if err := o.Bucket.Put(ctx, pc.Key, data); err != nil {
			return err
		}
		man.Pieces = append(man.Pieces, pc)
		man.Bytes += pc.Bytes
		delete(parts, p)
	}
	if err := verifyPieces(ctx, o.Bucket, vsxSchema(), VSXOrder, man.Pieces); err != nil {
		return err
	}
	man.Verified = time.Now().UTC()
	b, err := encodeManifest(man)
	if err != nil {
		return err
	}
	if err := o.Bucket.Put(ctx, path.Join(vprefix, manifestName), b); err != nil {
		return err
	}
	if err := o.Bucket.Put(ctx, path.Join(o.Prefix, manifestName), b); err != nil {
		return err
	}
	slog.Info("VSX mirrored", "version", version, "rows", rows, "pieces", len(man.Pieces), "stored MB", fmt.Sprintf("%.1f", float64(man.Bytes)/1e6))
	all, err := o.Bucket.List(ctx, o.Prefix+"/")
	if err != nil {
		return err
	}
	for _, k := range all {
		rel := strings.TrimPrefix(k, o.Prefix+"/")
		if rel != manifestName && !strings.HasPrefix(rel, version+"/") {
			if err := o.Bucket.Delete(ctx, k); err != nil {
				return err
			}
		}
	}
	return nil
}
