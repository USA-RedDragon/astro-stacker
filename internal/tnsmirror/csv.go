// Package tnsmirror keeps a local copy of the Transient Name Server's
// public objects list in S3 and answers position searches from it in
// memory. The list is the daily tns_public_objects.csv.zip, kept current
// with the hourly delta files; both need a TNS bot's key.
package tnsmirror

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"
)

// Object is one TNS public object.
type Object struct {
	ObjID          int64     `json:"objid"`
	Prefix         string    `json:"name_prefix"`
	Name           string    `json:"name"`
	RA             float64   `json:"ra"`
	Dec            float64   `json:"dec"`
	Redshift       *float64  `json:"redshift,omitempty"`
	Type           string    `json:"type,omitempty"`
	ReportingGroup string    `json:"reporting_group,omitempty"`
	SourceGroup    string    `json:"source_group,omitempty"`
	Discovered     time.Time `json:"discovered"`
	DiscoveryMag   *float64  `json:"discovery_mag,omitempty"`
	DiscoveryBand  string    `json:"discovery_filter,omitempty"`
	Reporters      string    `json:"reporters,omitempty"`
	InternalNames  string    `json:"internal_names,omitempty"`
	LastModified   string    `json:"last_modified,omitempty"`
}

// FullName is the designation with its prefix, such as "AT 2026acsi".
func (o Object) FullName() string {
	return strings.TrimSpace(o.Prefix + " " + o.Name)
}

// Group is who reported the object: the reporting group, else the data
// source group, else the reporters.
func (o Object) Group() string {
	for _, s := range []string{o.ReportingGroup, o.SourceGroup, o.Reporters} {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return ""
}

// File is one parsed public objects file.
type File struct {
	// Skipped counts rows that could not be read.
	Skipped int
	// Stamp is the file's first line: the time the list was made, or for a
	// delta file the span it covers.
	Stamp   string
	From    time.Time
	To      time.Time
	Objects []Object
}

// ErrFormat says a file is not a TNS public objects list.
var ErrFormat = errors.New("tnsmirror: not a TNS public objects file")

func requiredColumns() []string {
	return []string{"objid", "name_prefix", "name", "ra", "declination", "discoverydate"}
}

const stampLayout = "2006-01-02 15:04:05"

// parseStamp reads the first line: "2021-03-15 00:00:00" for the full
// list, "2025-01-17 08:00:00 - 09:00:00" for an hourly file and
// "2021-03-14 00:00:00 - 23:59:59" for a daily one.
func parseStamp(s string) (time.Time, time.Time, error) {
	s = strings.Trim(strings.TrimSpace(s), `"`)
	from, rest, span := strings.Cut(s, " - ")
	t0, err := time.ParseInLocation(stampLayout, strings.TrimSpace(from), time.UTC)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: first line %q", ErrFormat, s)
	}
	if !span {
		return t0, t0, nil
	}
	rest = strings.TrimSpace(rest)
	t1, err := time.ParseInLocation(stampLayout, rest, time.UTC)
	if err != nil {
		hms, err2 := time.ParseInLocation("15:04:05", rest, time.UTC)
		if err2 != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("%w: first line %q", ErrFormat, s)
		}
		t1 = time.Date(t0.Year(), t0.Month(), t0.Day(), hms.Hour(), hms.Minute(), hms.Second(), 0, time.UTC)
		if !t1.After(t0) {
			t1 = t1.AddDate(0, 0, 1)
		}
	}
	return t0, t1, nil
}

// Parse reads a public objects CSV: a first line with the file's time,
// then a header row naming the columns, then one object per row. Columns
// are found by name, so added or reordered columns do not matter.
func Parse(r io.Reader) (File, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return File{}, err
	}
	first, rest, ok := bytes.Cut(data, []byte("\n"))
	if !ok && len(bytes.TrimSpace(first)) == 0 {
		return File{}, fmt.Errorf("%w: empty", ErrFormat)
	}
	var f File
	f.Stamp = strings.Trim(strings.TrimSpace(string(first)), `"`)
	if f.From, f.To, err = parseStamp(f.Stamp); err != nil {
		return File{}, err
	}
	if len(bytes.TrimSpace(rest)) == 0 {
		return f, nil
	}
	cr := csv.NewReader(bytes.NewReader(rest))
	cr.FieldsPerRecord = -1
	cr.ReuseRecord = true
	cr.LazyQuotes = true
	head, err := cr.Read()
	if err != nil {
		return File{}, fmt.Errorf("%w: header: %w", ErrFormat, err)
	}
	col := map[string]int{}
	for i, h := range head {
		col[strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\ufeff")))] = i
	}
	for _, c := range requiredColumns() {
		if _, ok := col[c]; !ok {
			return File{}, fmt.Errorf("%w: no %s column", ErrFormat, c)
		}
	}
	line := 2
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		line++
		if err != nil {
			f.Skipped++
			continue
		}
		get := func(name string) string {
			if i, ok := col[name]; ok && i < len(rec) {
				return strings.TrimSpace(rec[i])
			}
			return ""
		}
		o, err := objectOf(get)
		if err != nil {
			if f.Skipped == 0 {
				slog.Warn("TNS file row skipped", "line", line, "error", err)
			}
			f.Skipped++
			continue
		}
		f.Objects = append(f.Objects, o)
	}
	if f.Skipped > maxSkipped(len(f.Objects)) {
		return File{}, fmt.Errorf("%w: %d of %d rows unreadable", ErrFormat, f.Skipped, f.Skipped+len(f.Objects))
	}
	return f, nil
}

// maxSkipped is how many unreadable rows a file of good rows may have: 1%,
// at least 10.
func maxSkipped(good int) int { return max(10, good/100) }

func objectOf(get func(string) string) (Object, error) {
	var o Object
	var err error
	if o.ObjID, err = strconv.ParseInt(get("objid"), 10, 64); err != nil {
		return o, fmt.Errorf("objid %q", get("objid"))
	}
	o.Prefix, o.Name = get("name_prefix"), get("name")
	if o.Name == "" {
		return o, errors.New("no name")
	}
	ra, ok := angle(get("ra"), 15)
	if !ok || ra < 0 || ra >= 360 {
		return o, fmt.Errorf("ra %q", get("ra"))
	}
	dec, ok := angle(get("declination"), 1)
	if !ok || dec < -90 || dec > 90 {
		return o, fmt.Errorf("declination %q", get("declination"))
	}
	o.RA, o.Dec = ra, dec
	if o.Discovered, err = ParseTime(get("discoverydate")); err != nil {
		return o, fmt.Errorf("discoverydate %q", get("discoverydate"))
	}
	o.Redshift = optFloat(get("redshift"))
	o.DiscoveryMag = optFloat(get("discoverymag"))
	o.Type = get("type")
	o.ReportingGroup = get("reporting_group")
	o.SourceGroup = get("source_group")
	o.DiscoveryBand = get("filter")
	o.Reporters = get("reporters")
	o.InternalNames = get("internal_names")
	o.LastModified = get("lastmodified")
	return o, nil
}

func optFloat(s string) *float64 {
	if s == "" {
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return &v
}

// angle reads decimal degrees, or sexagesimal with mult 15 for hours and
// 1 for degrees.
func angle(s string, mult float64) (float64, bool) {
	if s == "" {
		return 0, false
	}
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		return v, !math.IsNaN(v) && !math.IsInf(v, 0)
	}
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == ':' || r == ' ' })
	if len(parts) != 3 {
		return 0, false
	}
	neg := strings.HasPrefix(parts[0], "-")
	var v [3]float64
	for i, p := range parts {
		x, err := strconv.ParseFloat(strings.TrimLeft(p, "+-"), 64)
		if err != nil || x < 0 {
			return 0, false
		}
		v[i] = x
	}
	d := (v[0] + v[1]/60 + v[2]/3600) * mult
	if neg {
		d = -d
	}
	return d, true
}

// ParseTime reads a TNS date: "2026-09-22 15:50:24.000", with or without
// fractions or seconds, ISO with a T, or a fractional day such as
// "2026-09-22.660".
func ParseTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, l := range []string{"2006-01-02 15:04:05.999999999", "2006-01-02T15:04:05.999999999", "2006-01-02 15:04", "2006-01-02T15:04:05Z07:00"} {
		if t, err := time.ParseInLocation(l, s, time.UTC); err == nil {
			return t.UTC(), nil
		}
	}
	day, frac, ok := strings.Cut(s, ".")
	t, err := time.ParseInLocation(time.DateOnly, day, time.UTC)
	if err != nil {
		return time.Time{}, err
	}
	if ok {
		f, err := strconv.ParseFloat("0."+frac, 64)
		if err != nil {
			return time.Time{}, err
		}
		t = t.Add(time.Duration(math.Round(f*86400e3)) * time.Millisecond)
	}
	return t, nil
}
