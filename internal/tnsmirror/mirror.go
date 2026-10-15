package tnsmirror

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/starcat"
	"github.com/klauspost/compress/zstd"
)

const (
	currentName  = "current.json.zst"
	manifestName = "manifest.json.zst"
	objectsName  = "objects.csv.zst"
	// DefaultKeep is how long old daily versions stay in the bucket.
	DefaultKeep = 30 * 24 * time.Hour
	// fullAt is when, after midnight UT, the day's full list is fetched;
	// TNS stages it around midnight.
	fullAt = 75 * time.Minute
	// hourAt is how long after the hour its delta file is fetched.
	hourAt     = 7 * time.Minute
	retryAfter = 30 * time.Minute
	maxBackoff = 6 * time.Hour
	hourRetry  = 20 * time.Minute
	hourTries  = 3
	// hourFiles is how many hourly files TNS keeps; they overwrite each
	// other on a 24 hour cycle.
	hourFiles = 23
)

// Delta is one hourly file applied on top of a full list.
type Delta struct {
	Key    string    `json:"key"`
	Stamp  string    `json:"stamp"`
	To     time.Time `json:"to"`
	Rows   int       `json:"rows"`
	SHA256 string    `json:"sha256"`
}

// Manifest describes the mirror: the full list it was built from and the
// hourly files applied since. The current one lives at
// <prefix>/current.json.zst; replacing that one object switches readers to
// a new version at once.
type Manifest struct {
	Catalog    string    `json:"catalog"`
	Version    string    `json:"version"`
	Source     string    `json:"source"`
	Stamp      string    `json:"stamp"`
	Downloaded time.Time `json:"downloaded"`
	Key        string    `json:"key"`
	Rows       int       `json:"rows"`
	Bytes      int       `json:"bytes"`
	SHA256     string    `json:"sha256"`
	Deltas     []Delta   `json:"deltas"`
	// AsOf is the end of the newest file applied, empty hours included.
	AsOf time.Time `json:"as_of"`
}

// Status is what the mirror holds, for logs and pages.
type Status struct {
	Live    bool      `json:"live"`
	Version string    `json:"version,omitempty"`
	AsOf    time.Time `json:"as_of"`
	Objects int       `json:"objects"`
}

// Mirror keeps the public objects list in a bucket and in memory.
type Mirror struct {
	Bucket starcat.Bucket
	Prefix string
	Client *Client
	// Keep is how long old versions stay; DefaultKeep when zero.
	Keep time.Duration
	Now  func() time.Time

	mu        sync.Mutex
	cur       *Manifest
	idx       atomic.Pointer[Index]
	nextFull  time.Time
	fullFails int
	retryHour time.Time
	retryAt   time.Time
	hourTries int
	encOnce   sync.Once
	enc       *zstd.Encoder
	decOnce   sync.Once
	dec       *zstd.Decoder
}

func (m *Mirror) now() time.Time {
	if m.Now != nil {
		return m.Now().UTC()
	}
	return time.Now().UTC()
}

func (m *Mirror) key(parts ...string) string {
	return path.Join(append([]string{strings.Trim(m.Prefix, "/")}, parts...)...)
}

// Index is the in-memory index, or nil before the first load.
func (m *Mirror) Index() *Index { return m.idx.Load() }

// Status says what the mirror holds.
func (m *Mirror) Status() Status {
	ix := m.idx.Load()
	if ix == nil {
		return Status{}
	}
	return Status{Live: true, Version: ix.Version, AsOf: ix.AsOf, Objects: ix.Len()}
}

func (m *Mirror) zenc() *zstd.Encoder {
	m.encOnce.Do(func() { m.enc, _ = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBetterCompression)) })
	return m.enc
}

func (m *Mirror) zdec() *zstd.Decoder {
	m.decOnce.Do(func() { m.dec, _ = zstd.NewReader(nil, zstd.WithDecoderMaxMemory(4<<30)) })
	return m.dec
}

func (m *Mirror) putJSON(ctx context.Context, key string, v any) error {
	j, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return m.Bucket.Put(ctx, key, m.zenc().EncodeAll(j, nil))
}

func (m *Mirror) getZ(ctx context.Context, key string) ([]byte, error) {
	b, err := m.Bucket.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	return m.zdec().DecodeAll(b, nil)
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Load reads the current version from the bucket into memory. A bucket
// with no mirror yet is not an error.
func (m *Mirror) Load(ctx context.Context) error {
	raw, err := m.getZ(ctx, m.key(currentName))
	if errors.Is(err, starcat.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	var man Manifest
	if err := json.Unmarshal(raw, &man); err != nil {
		return fmt.Errorf("tnsmirror: %s: %w", currentName, err)
	}
	csv, err := m.getZ(ctx, man.Key)
	if err != nil {
		return err
	}
	if sum(csv) != man.SHA256 {
		return fmt.Errorf("tnsmirror: %s does not match its checksum", man.Key)
	}
	f, err := Parse(bytes.NewReader(csv))
	if err != nil {
		return err
	}
	objs := f.Objects
	for _, d := range man.Deltas {
		b, err := m.getZ(ctx, d.Key)
		if err != nil {
			return err
		}
		if sum(b) != d.SHA256 {
			return fmt.Errorf("tnsmirror: %s does not match its checksum", d.Key)
		}
		df, err := Parse(bytes.NewReader(b))
		if err != nil {
			return err
		}
		objs = append(objs, df.Objects...)
	}
	m.mu.Lock()
	m.cur = &man
	m.mu.Unlock()
	m.idx.Store(NewIndex(objs, man.Version, man.AsOf))
	return nil
}

// Full downloads the day's full list and switches to it when it is newer
// than the current version. It reports whether it switched.
func (m *Mirror) Full(ctx context.Context) (bool, error) {
	csv, err := m.Client.Fetch(ctx, FullFile)
	if err != nil {
		return false, err
	}
	f, err := Parse(bytes.NewReader(csv))
	if err != nil {
		return false, err
	}
	if len(f.Objects) == 0 {
		return false, fmt.Errorf("%w: the full list has no objects", ErrFormat)
	}
	version := f.From.Format("20060102")
	m.mu.Lock()
	cur := m.cur
	m.mu.Unlock()
	if cur != nil && version <= cur.Version {
		return false, nil
	}
	man := Manifest{Catalog: "tns", Version: version, Source: m.Client.base() + FullFile, Stamp: f.Stamp, Downloaded: m.now(),
		Key: m.key(version, objectsName), Rows: len(f.Objects), Bytes: len(csv), SHA256: sum(csv), Deltas: []Delta{}, AsOf: f.From}
	if err := m.Bucket.Put(ctx, man.Key, m.zenc().EncodeAll(csv, nil)); err != nil {
		return false, err
	}
	if err := m.putJSON(ctx, m.key(version, manifestName), man); err != nil {
		return false, err
	}
	if err := m.putJSON(ctx, m.key(currentName), man); err != nil {
		return false, err
	}
	m.mu.Lock()
	m.cur = &man
	m.mu.Unlock()
	m.idx.Store(NewIndex(f.Objects, version, f.From))
	slog.Info("TNS mirror switched to a new full list", "version", version, "stamp", f.Stamp, "objects", len(f.Objects), "skipped_rows", f.Skipped, "bytes", len(csv))
	m.prune(ctx, version)
	return true, nil
}

// Hour applies the hourly file that ends at end. It reports false, with
// no error, when TNS still has the file of a day earlier there.
func (m *Mirror) Hour(ctx context.Context, end time.Time) (bool, error) {
	csv, err := m.Client.Fetch(ctx, HourFile(end.Hour()))
	if err != nil {
		return false, err
	}
	f, err := Parse(bytes.NewReader(csv))
	if err != nil {
		return false, err
	}
	if !f.To.Equal(end) {
		return false, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur == nil {
		return false, errors.New("tnsmirror: no full list to apply an hourly file to")
	}
	man := *m.cur
	man.Deltas = slices.Clone(man.Deltas)
	if len(f.Objects) > 0 {
		d := Delta{Key: m.key(man.Version, "deltas", end.Format("2006010215")+".csv.zst"), Stamp: f.Stamp, To: f.To, Rows: len(f.Objects), SHA256: sum(csv)}
		if err := m.Bucket.Put(ctx, d.Key, m.zenc().EncodeAll(csv, nil)); err != nil {
			return false, err
		}
		man.Deltas = append(man.Deltas, d)
	}
	if f.To.After(man.AsOf) {
		man.AsOf = f.To
	}
	if err := m.putJSON(ctx, m.key(currentName), man); err != nil {
		return false, err
	}
	m.cur = &man
	if len(f.Objects) > 0 {
		old := m.idx.Load()
		objs := make([]Object, 0, old.Len()+len(f.Objects))
		if old != nil {
			objs = append(objs, old.objs...)
		}
		m.idx.Store(NewIndex(append(objs, f.Objects...), man.Version, man.AsOf))
		slog.Info("TNS mirror applied an hourly file", "stamp", f.Stamp, "objects", len(f.Objects))
	} else if old := m.idx.Load(); old != nil {
		ix := *old
		ix.AsOf = man.AsOf
		m.idx.Store(&ix)
	}
	return true, nil
}

var versionDir = regexp.MustCompile(`^\d{8}$`)

// prune deletes versions older than Keep, never the current one.
func (m *Mirror) prune(ctx context.Context, current string) {
	keep := m.Keep
	if keep <= 0 {
		keep = DefaultKeep
	}
	cutoff := m.now().Add(-keep).Format("20060102")
	base := m.key() + "/"
	keys, err := m.Bucket.List(ctx, base)
	if err != nil {
		slog.Warn("TNS mirror could not list old versions", "error", err)
		return
	}
	for _, k := range keys {
		v, _, ok := strings.Cut(strings.TrimPrefix(k, base), "/")
		if !ok || !versionDir.MatchString(v) || v == current || v >= cutoff {
			continue
		}
		if err := m.Bucket.Delete(ctx, k); err != nil {
			slog.Warn("TNS mirror could not delete an old version", "key", k, "error", err)
		}
	}
}

// fullBackoff is the wait before the next try at the full list after
// fails failed or not-yet-new tries: 30 minutes, doubling to 6 hours.
func fullBackoff(fails int) time.Duration {
	d := retryAfter
	for i := 1; i < fails && d < maxBackoff; i++ {
		d *= 2
	}
	return min(d, maxBackoff)
}

// step does the next due download, if any, and returns when to look
// again; the zero time once TNS refused the bot's key.
func (m *Mirror) step(ctx context.Context) time.Time {
	now := m.now()
	day := now.Truncate(24 * time.Hour)
	m.mu.Lock()
	cur := m.cur
	m.mu.Unlock()
	if (cur == nil || cur.Version < day.Format("20060102") && !now.Before(day.Add(fullAt))) && !now.Before(m.nextFull) {
		switched, err := m.Full(ctx)
		switch {
		case errors.Is(err, ErrRefused):
			slog.Error("TNS refused the bot's key; the TNS mirror stops until the stacker restarts", "error", err)
			return time.Time{}
		case err != nil || !switched:
			m.fullFails++
			m.nextFull = now.Add(fullBackoff(m.fullFails))
			if err != nil {
				slog.Warn("TNS mirror could not fetch the full list", "error", err, "next_try", m.nextFull)
			}
		default:
			m.fullFails = 0
		}
		return m.now()
	}
	if cur == nil {
		return m.nextFull
	}
	end := cur.AsOf.Truncate(time.Hour).Add(time.Hour)
	if oldest := now.Truncate(time.Hour).Add(-hourFiles * time.Hour); end.Before(oldest) {
		end = oldest
	}
	nextFull := day.Add(24*time.Hour + fullAt)
	if cur.Version < day.Format("20060102") {
		nextFull = maxTime(day.Add(fullAt), m.nextFull)
	}
	due := end.Add(hourAt)
	m.mu.Lock()
	if m.retryHour.Equal(end) {
		due = maxTime(due, m.retryAt)
	}
	m.mu.Unlock()
	if now.Before(due) {
		return minTime(due, nextFull)
	}
	ok, err := m.Hour(ctx, end)
	if errors.Is(err, ErrRefused) {
		slog.Error("TNS refused the bot's key; the TNS mirror stops until the stacker restarts", "error", err)
		return time.Time{}
	}
	if err != nil {
		slog.Warn("TNS mirror could not fetch an hourly file", "hour", end.Format("2006-01-02 15:04"), "error", err)
	}
	if !ok {
		m.mu.Lock()
		if !m.retryHour.Equal(end) {
			m.retryHour, m.hourTries = end, 0
		}
		m.hourTries++
		m.retryAt = m.now().Add(hourRetry)
		if m.hourTries >= hourTries {
			slog.Warn("TNS mirror skipped an hourly file it could not get; the next full list has its objects", "hour", end.Format("2006-01-02 15:04"))
			c := *m.cur
			c.AsOf = end
			m.cur = &c
		}
		m.mu.Unlock()
	}
	return m.now()
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// Run loads the stored mirror, then keeps it current until ctx ends: the
// full list once a day after it is staged, and each hourly file a few
// minutes after its hour.
func (m *Mirror) Run(ctx context.Context) {
	if err := m.Load(ctx); err != nil {
		slog.Warn("TNS mirror could not load the stored copy; fetching a new one", "error", err)
	}
	if st := m.Status(); st.Live {
		slog.Info("TNS mirror loaded", "version", st.Version, "as_of", st.AsOf, "objects", st.Objects)
	}
	for ctx.Err() == nil {
		next := m.step(ctx)
		if next.IsZero() {
			return
		}
		if err := sleepCtx(ctx, time.Until(next)); err != nil {
			return
		}
	}
}
