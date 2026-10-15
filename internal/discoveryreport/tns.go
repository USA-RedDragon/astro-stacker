package discoveryreport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/tnsmirror"
)

// TNS sends AT reports through the Transient Name Server bulk report API
// with a bot's key.
type TNS struct {
	Key, BotID, BotName string
	// BaseURL is https://www.wis-tns.org/api by default.
	BaseURL string
	Client  *http.Client
	// ReplyWait is the pause between asks for a report's reply.
	ReplyWait time.Duration
}

// Enabled says whether a bot key is set.
func (t TNS) Enabled() bool { return t.Key != "" && t.BotID != "" && t.BotName != "" }

func (t TNS) base() string {
	if t.BaseURL != "" {
		return strings.TrimRight(t.BaseURL, "/")
	}
	return "https://www.wis-tns.org/api"
}

func (t TNS) client() *http.Client {
	if t.Client != nil {
		return t.Client
	}
	return &http.Client{Timeout: time.Minute}
}

// ErrTNS is an answer from TNS that is not a success.
var ErrTNS = errors.New("tns")

func (t TNS) post(ctx context.Context, path string, form url.Values) (json.RawMessage, error) {
	form.Set("api_key", t.Key)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.base()+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", tnsmirror.Bot{ID: t.BotID, Name: t.BotName}.Marker())
	resp, err := t.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w %s: %s", ErrTNS, path, strings.ReplaceAll(err.Error(), t.Key, "<key>"))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var env struct {
		Code    int             `json:"id_code"`
		Message string          `json:"id_message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("%w %s answered %s", ErrTNS, path, resp.Status)
	}
	if env.Code != http.StatusOK && env.Code != http.StatusCreated {
		return env.Data, fmt.Errorf("%w %s: %d %s", ErrTNS, path, env.Code, env.Message)
	}
	return env.Data, nil
}

// Values are the TNS id tables, by kind then name.
type Values map[string]map[string]string

// Values reads the id tables of filters, units and object types.
func (t TNS) Values(ctx context.Context) (Values, error) {
	data, err := t.post(ctx, "/get/values", url.Values{})
	if err != nil {
		return nil, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%w values: %w", ErrTNS, err)
	}
	out := Values{}
	for kind, v := range raw {
		var rows []map[string]any
		if json.Unmarshal(v, &rows) != nil {
			continue
		}
		m := map[string]string{}
		for _, r := range rows {
			id, name := fmt.Sprint(r["id"]), fmt.Sprint(r["name"])
			if id != "" && name != "" {
				m[name] = id
			}
		}
		out[kind] = m
	}
	return out, nil
}

// ID finds the id of a named value of one of the kinds.
func (v Values) ID(name string, kinds ...string) (string, bool) {
	for _, k := range kinds {
		if id, ok := v[k][name]; ok {
			return id, true
		}
	}
	return "", false
}

// Reply is what TNS said about a sent report.
type Reply struct {
	ReportID string          `json:"report_id"`
	Name     string          `json:"name,omitempty"`
	Raw      json.RawMessage `json:"raw,omitempty"`
}

// Send posts an AT report and asks for its reply a few times.
func (t TNS) Send(ctx context.Context, report map[string]any) (Reply, error) {
	body, err := json.Marshal(report)
	if err != nil {
		return Reply{}, err
	}
	data, err := t.post(ctx, "/set/bulk-report", url.Values{"data": {string(body)}})
	if err != nil {
		return Reply{}, err
	}
	var sent struct {
		ReportID json.Number `json:"report_id"`
	}
	if err := json.Unmarshal(data, &sent); err != nil || sent.ReportID == "" {
		return Reply{}, fmt.Errorf("%w: no report id in %s", ErrTNS, data)
	}
	r := Reply{ReportID: sent.ReportID.String()}
	wait := t.ReplyWait
	if wait <= 0 {
		wait = 3 * time.Second
	}
	for range 5 {
		raw, err := t.post(ctx, "/get/bulk-report-reply", url.Values{"report_id": {r.ReportID}})
		if err == nil {
			r.Raw = raw
			r.Name = objectName(raw)
			return r, nil
		}
		select {
		case <-ctx.Done():
			return r, ctx.Err()
		case <-time.After(wait):
		}
	}
	return r, nil
}

// objectName digs the TNS name out of a reply: the objname of a feedback
// that inserted or found the object.
func objectName(raw json.RawMessage) string {
	var walk func(v any) string
	walk = func(v any) string {
		switch x := v.(type) {
		case map[string]any:
			if n, ok := x["objname"].(string); ok && n != "" {
				prefix, _ := x["prefix"].(string)
				if prefix == "" {
					prefix = "AT"
				}
				return prefix + " " + n
			}
			for _, k := range sortedKeys(x) {
				if s := walk(x[k]); s != "" {
					return s
				}
			}
		case []any:
			for _, e := range x {
				if s := walk(e); s != "" {
					return s
				}
			}
		}
		return ""
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	return walk(v)
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

func itoa(i int) string { return strconv.Itoa(i) }
