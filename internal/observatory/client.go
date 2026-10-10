package observatory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/coder/websocket"
)

const (
	RequestTimeout = 5 * time.Second
	CommandTimeout = 20 * time.Second
	DialTimeout    = 5 * time.Second
	PreviewTimeout = 30 * time.Second
	maxBody        = 8 << 20
	pathCommands   = "/os/v1/commands"
	pathPreview    = "/os/v1/preview"
)

var (
	ErrUnconfigured = errors.New("scheduler API is not configured")
	ErrUnauthorized = errors.New("the scheduler API refused the token")
	ErrDisabled     = errors.New("the scheduler API is disabled on the observatory")
	ErrNotFound     = errors.New("not found on the observatory")
)

type Client struct {
	base  string
	token string
	http  *http.Client
}

func NewClient(baseURL, token string) *Client {
	return &Client{
		base:  strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		token: token,
		http:  &http.Client{Transport: dialLimited()},
	}
}

func dialLimited() *http.Transport {
	t, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		t = &http.Transport{Proxy: http.ProxyFromEnvironment}
	}
	t = t.Clone()
	t.DialContext = (&net.Dialer{Timeout: DialTimeout, KeepAlive: 30 * time.Second}).DialContext
	t.TLSHandshakeTimeout = DialTimeout
	return t
}

func (c *Client) Configured() bool {
	return c != nil && c.base != ""
}

func unreachable(err error) error {
	return fmt.Errorf("%w: %w", schedcmd.ErrUnreachable, err)
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	return c.doWithin(ctx, RequestTimeout, method, path, body, out)
}

func (c *Client) doWithin(ctx context.Context, timeout time.Duration, method, path string, body any, out any) error {
	if !c.Configured() {
		return unreachable(ErrUnconfigured)
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return unreachable(err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return unreachable(err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, maxBody))
	if err != nil {
		return unreachable(err)
	}
	switch {
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		return ErrUnauthorized
	case res.StatusCode == http.StatusServiceUnavailable:
		return unreachable(ErrDisabled)
	case res.StatusCode == http.StatusNotFound:
		return notFound(data)
	case res.StatusCode >= http.StatusBadGateway:
		return unreachable(fmt.Errorf("%s %s: %s", method, path, res.Status))
	case res.StatusCode >= http.StatusBadRequest:
		return fmt.Errorf("%s %s: %s: %s", method, path, res.Status, strings.TrimSpace(string(data)))
	}
	if out == nil {
		return nil
	}
	if raw, ok := out.(*json.RawMessage); ok {
		if !json.Valid(data) {
			return fmt.Errorf("%s %s: invalid JSON", method, path)
		}
		*raw = append((*raw)[:0], data...)
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	return nil
}

func (c *Client) Status(ctx context.Context) (Status, error) {
	var s Status
	err := c.do(ctx, http.MethodGet, "/os/v1/status", nil, &s)
	return s, err
}

func (c *Client) Send(ctx context.Context, e schedcmd.Envelope) (schedcmd.Result, error) {
	var r schedcmd.Result
	err := c.doWithin(ctx, CommandTimeout, http.MethodPost, pathCommands, e, &r)
	if errors.Is(err, ErrNotFound) {
		return r, unreachable(err)
	}
	return r, err
}

func (c *Client) Command(ctx context.Context, id string) (schedcmd.Result, error) {
	var r schedcmd.Result
	err := c.do(ctx, http.MethodGet, pathCommands+"/"+url.PathEscape(id), nil, &r)
	return r, err
}

func (c *Client) Cancel(ctx context.Context, id string) (schedcmd.Result, error) {
	var r schedcmd.Result
	err := c.doWithin(ctx, CommandTimeout, http.MethodDelete, pathCommands+"/"+url.PathEscape(id), nil, &r)
	return r, err
}

func (c *Client) Preview(ctx context.Context, start *time.Time) (json.RawMessage, error) {
	p := pathPreview
	if start != nil {
		p += "?start=" + url.QueryEscape(start.UTC().Format(time.RFC3339))
	}
	var raw json.RawMessage
	err := c.doWithin(ctx, PreviewTimeout, http.MethodGet, p, nil, &raw)
	return raw, err
}

func (c *Client) PreviewWith(ctx context.Context, req PreviewRequest) (json.RawMessage, error) {
	var raw json.RawMessage
	err := c.doWithin(ctx, PreviewTimeout, http.MethodPost, pathPreview, req, &raw)
	return raw, err
}

func (c *Client) Dial(ctx context.Context) (*websocket.Conn, error) {
	if !c.Configured() {
		return nil, unreachable(ErrUnconfigured)
	}
	u := c.base + "/os/v1/ws"
	switch {
	case strings.HasPrefix(u, "https://"):
		u = "wss://" + strings.TrimPrefix(u, "https://")
	case strings.HasPrefix(u, "http://"):
		u = "ws://" + strings.TrimPrefix(u, "http://")
	}
	dctx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()
	conn, res, err := websocket.Dial(dctx, u, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer " + c.token}},
	})
	if res != nil && res.Body != nil {
		_ = res.Body.Close()
	}
	if err != nil {
		if res != nil && (res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden) {
			return nil, ErrUnauthorized
		}
		return nil, unreachable(err)
	}
	conn.SetReadLimit(maxBody)
	return conn, nil
}
