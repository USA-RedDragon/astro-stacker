package collabswitch

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
)

const (
	Constant     = "collab"
	pathStart    = "/application-start"
	pathOutline  = "/sequence/json"
	pathState    = "/sequence/state"
	pathEdit     = "/sequence/edit"
	stateTimeout = 3 * time.Minute
	quickTimeout = 20 * time.Second
	maxState     = 512 << 20
	maxQuick     = 8 << 20
	statusRun    = "RUNNING"
	statusNew    = "CREATED"
	containerTag = "_Container"

	fingerprintDepth = 3
)

var (
	ErrNINA        = errors.New("NINA Advanced API")
	ErrNoSequence  = errors.New("NINA has no sequence loaded")
	ErrNoConstant  = errors.New("the running sequence has no Powerups constant named collab")
	ErrUnreadValue = errors.New("NINA did not report the collab constant's value")
)

func areas() []string { return []string{"Start", "Imaging", "End"} }

type NINA struct {
	Base string
	HTTP *http.Client
}

func NewNINA(base string) *NINA {
	return &NINA{Base: strings.TrimRight(strings.TrimSpace(base), "/"), HTTP: &http.Client{}}
}

func (n *NINA) Configured() bool { return n != nil && n.Base != "" }

type envelope[T any] struct {
	Response   T      `json:"Response"`
	Error      string `json:"Error"`
	StatusCode int    `json:"StatusCode"`
	Success    bool   `json:"Success"`
}

func (n *NINA) get(ctx context.Context, path string, q url.Values, timeout time.Duration, limit int64, out any) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	u := n.Base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	client := n.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%w %s: %w", ErrNINA, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	dec := json.NewDecoder(io.LimitReader(resp.Body, limit))
	if err := dec.Decode(out); err != nil {
		var te *json.UnmarshalTypeError
		if !errors.As(err, &te) {
			return fmt.Errorf("%w %s: %s: unreadable answer: %w", ErrNINA, path, resp.Status, err)
		}
	}
	return nil
}

func check(path string, success bool, status int, msg string) error {
	if success {
		return nil
	}
	if status == http.StatusConflict || strings.Contains(strings.ToLower(msg), "not initialized") {
		return fmt.Errorf("%w: %w", ErrNINA, ErrNoSequence)
	}
	return fmt.Errorf("%w %s: %d %s", ErrNINA, path, status, msg)
}

func (n *NINA) Started(ctx context.Context) (string, error) {
	var env envelope[string]
	if err := n.get(ctx, pathStart, nil, quickTimeout, maxQuick, &env); err != nil {
		return "", err
	}
	return env.Response, check(pathStart, env.Success, env.StatusCode, env.Error)
}

type text string

func (t *text) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*t = text(s)
		return nil
	}
	if len(b) > 0 && b[0] != '{' && b[0] != '[' && string(b) != "null" {
		*t = text(b)
	}
	return nil
}

type node struct {
	Name       text   `json:"Name"`
	Status     text   `json:"Status"`
	Identifier text   `json:"Identifier"`
	Definition *text  `json:"Definition"`
	Items      []node `json:"Items"`
}

type Read struct {
	Present bool
	Path    string
	Value   string
	Running []string
}

func (n *NINA) State(ctx context.Context) (Read, error) {
	var env envelope[[]node]
	if err := n.get(ctx, pathState, nil, stateTimeout, maxState, &env); err != nil {
		return Read{}, err
	}
	if err := check(pathState, env.Success, env.StatusCode, env.Error); err != nil {
		return Read{}, err
	}
	return find(env.Response), nil
}

type Outline struct {
	Running     []string
	Fingerprint string
	Fresh       bool
}

func (n *NINA) Outline(ctx context.Context) (Outline, error) {
	var env envelope[[]node]
	if err := n.get(ctx, pathOutline, nil, quickTimeout, maxQuick, &env); err != nil {
		return Outline{}, err
	}
	if err := check(pathOutline, env.Success, env.StatusCode, env.Error); err != nil {
		return Outline{}, err
	}
	return outline(env.Response), nil
}

func (n *NINA) Edit(ctx context.Context, path, value string) error {
	var env envelope[json.RawMessage]
	q := url.Values{"path": {path}, "value": {value}}
	if err := n.get(ctx, pathEdit, q, quickTimeout, maxQuick, &env); err != nil {
		return err
	}
	return check(pathEdit, env.Success, env.StatusCode, env.Error)
}

func isConstant(x node) bool {
	return string(x.Identifier) == Constant && x.Definition != nil && !strings.Contains(strings.ToLower(string(x.Name)), "variable")
}

func find(top []node) Read {
	var r Read
	names := areas()
	for i, area := range top {
		if i == 0 || i > len(names) {
			continue
		}
		walk(area.Items, names[i-1], &r)
		if r.Present {
			break
		}
	}
	r.Running = running(top)
	return r
}

func walk(items []node, prefix string, r *Read) {
	for i, x := range items {
		p := prefix + "-Items-" + strconv.Itoa(i)
		if isConstant(x) {
			r.Present, r.Path, r.Value = true, p+"-Definition", strings.TrimSpace(string(*x.Definition))
			return
		}
		walk(x.Items, p, r)
		if r.Present {
			return
		}
	}
}

func running(top []node) []string {
	var out []string
	items := top
	for {
		next := -1
		for i, x := range items {
			if strings.EqualFold(string(x.Status), statusRun) && strings.HasSuffix(string(x.Name), containerTag) {
				next = i
				break
			}
		}
		if next < 0 {
			return out
		}
		out = append(out, strings.TrimSuffix(string(items[next].Name), containerTag))
		items = items[next].Items
	}
}

func outline(top []node) Outline {
	o := Outline{Running: running(top), Fresh: true}
	var sb strings.Builder
	var shape func([]node, int)
	shape = func(items []node, depth int) {
		for _, x := range items {
			if x.Status != "" && !strings.EqualFold(string(x.Status), statusNew) {
				o.Fresh = false
			}
			if depth > fingerprintDepth {
				continue
			}
			sb.WriteString(string(x.Name))
			if len(x.Items) > 0 {
				sb.WriteByte('[')
				shape(x.Items, depth+1)
				sb.WriteByte(']')
			}
			sb.WriteByte(';')
		}
	}
	shape(top, 0)
	o.Fingerprint = sb.String()
	return o
}
