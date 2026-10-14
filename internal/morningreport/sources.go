package morningreport

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Point is one sample of a series.
type Point struct {
	T time.Time
	V float64
}

// Metrics reads series from Prometheus or Thanos.
type Metrics struct {
	URL    string
	Client *http.Client
}

// Step is the resolution metrics are read at.
const Step = time.Minute

// Range runs a range query over [start, end) and returns the first series,
// oldest first, or nil when the query matched nothing.
func (m Metrics) Range(ctx context.Context, q string, start, end time.Time) ([]Point, error) {
	if m.URL == "" {
		return nil, fmt.Errorf("no metrics URL")
	}
	if end.Sub(start) > Step {
		end = end.Add(-Step)
	}
	v := url.Values{}
	v.Set("query", q)
	v.Set("start", strconv.FormatInt(start.Unix(), 10))
	v.Set("end", strconv.FormatInt(end.Unix(), 10))
	v.Set("step", strconv.Itoa(int(Step/time.Second)))
	var out struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			Result []struct {
				Values [][2]any `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := getJSON(ctx, m.Client, strings.TrimRight(m.URL, "/")+"/api/v1/query_range?"+v.Encode(), &out); err != nil {
		return nil, err
	}
	if out.Status != "success" {
		return nil, fmt.Errorf("metrics query failed: %s", out.Error)
	}
	if len(out.Data.Result) == 0 {
		return nil, nil
	}
	pts := make([]Point, 0, len(out.Data.Result[0].Values))
	for _, p := range out.Data.Result[0].Values {
		ts, ok := p[0].(float64)
		s, ok2 := p[1].(string)
		if !ok || !ok2 {
			continue
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			continue
		}
		pts = append(pts, Point{T: time.Unix(0, int64(ts*1e9)).UTC(), V: f})
	}
	return pts, nil
}

// Line is one log line.
type Line struct {
	T    time.Time
	Text string
}

// Logs reads log lines from Loki.
type Logs struct {
	URL    string
	Client *http.Client
}

// pageSize is how many lines one Loki request returns.
const pageSize = 5000

// Range returns the lines matching q in [start, end), oldest first. Each
// page after the first starts at the last page's newest time, so lines that
// share it are not lost, and the lines already read at that time are skipped.
func (l Logs) Range(ctx context.Context, q string, start, end time.Time) ([]Line, error) {
	var all []Line
	from := start
	seen := map[Line]bool{}
	for {
		page, err := l.page(ctx, q, from, end, "forward", pageSize)
		if err != nil {
			return nil, err
		}
		added := 0
		for _, ln := range page {
			if seen[ln] {
				continue
			}
			all = append(all, ln)
			added++
		}
		if len(page) < pageSize || added == 0 {
			break
		}
		last := page[len(page)-1].T
		seen = map[Line]bool{}
		for _, ln := range page {
			if ln.T.Equal(last) {
				seen[ln] = true
			}
		}
		from = last
	}
	return all, nil
}

// Last returns the newest line matching q in [start, end), if any.
func (l Logs) Last(ctx context.Context, q string, start, end time.Time) (*Line, error) {
	page, err := l.page(ctx, q, start, end, "backward", 1)
	if err != nil || len(page) == 0 {
		return nil, err
	}
	return &page[len(page)-1], nil
}

func (l Logs) page(ctx context.Context, q string, start, end time.Time, dir string, limit int) ([]Line, error) {
	if l.URL == "" {
		return nil, fmt.Errorf("no log store URL")
	}
	v := url.Values{}
	v.Set("query", q)
	v.Set("start", strconv.FormatInt(start.UnixNano(), 10))
	v.Set("end", strconv.FormatInt(end.UnixNano(), 10))
	v.Set("limit", strconv.Itoa(limit))
	v.Set("direction", dir)
	var out struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Values [][2]string `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := getJSON(ctx, l.Client, strings.TrimRight(l.URL, "/")+"/loki/api/v1/query_range?"+v.Encode(), &out); err != nil {
		return nil, err
	}
	if out.Status != "success" {
		return nil, fmt.Errorf("log query failed")
	}
	var lines []Line
	for _, s := range out.Data.Result {
		for _, p := range s.Values {
			ns, err := strconv.ParseInt(p[0], 10, 64)
			if err != nil {
				continue
			}
			lines = append(lines, Line{T: time.Unix(0, ns).UTC(), Text: p[1]})
		}
	}
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].T.Before(lines[j].T) })
	return lines, nil
}

func getJSON(ctx context.Context, c *http.Client, u string, out any) error {
	if c == nil {
		c = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %s: %s", req.URL.Host, resp.Status, strings.TrimSpace(string(body[:min(len(body), 200)])))
	}
	return json.Unmarshal(body, out)
}
