package horizonsurvey

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var (
	ErrPHD2       = errors.New("PHD2")
	ErrPHD2Busy   = errors.New("PHD2 is guiding or calibrating; stop the NINA sequence and guiding first")
	ErrPHD2Closed = errors.New("PHD2 connection closed")
)

func PHD2Allowed(method string) bool {
	switch method {
	case "get_app_state", "stop_capture", "get_pixel_scale", "get_camera_frame_size", "capture_single_frame", "save_image", "get_connected":
		return true
	}
	return false
}

func phd2Idle(state string) bool { return state == "Stopped" || state == "Selected" }

func phd2Stoppable(state string) bool { return state == "Looping" }

type rpcReply struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type phdEvent struct {
	Event      string `json:"Event"`
	PHDVersion string `json:"PHDVersion"`
	Success    bool   `json:"Success"`
	Error      string `json:"Error"`
	Path       string `json:"Path"`
	State      string `json:"State"`
}

type PHD2 struct {
	conn    net.Conn
	mu      sync.Mutex
	nextID  int
	pending map[int]chan rpcReply
	events  chan phdEvent
	done    chan struct{}
	Version string
	verCh   chan struct{}
	verOnce sync.Once
}

func DialPHD2(ctx context.Context, addr string) (*PHD2, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("%w %s: %w", ErrPHD2, addr, err)
	}
	p := &PHD2{conn: conn, pending: map[int]chan rpcReply{}, events: make(chan phdEvent, 256), done: make(chan struct{}), verCh: make(chan struct{})}
	go p.read()
	select {
	case <-p.verCh:
	case <-time.After(3 * time.Second):
	case <-ctx.Done():
	}
	return p, nil
}

func (p *PHD2) read() {
	defer close(p.done)
	sc := bufio.NewScanner(p.conn)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		line := sc.Bytes()
		var probe struct {
			ID    *int   `json:"id"`
			Event string `json:"Event"`
		}
		if json.Unmarshal(line, &probe) != nil {
			continue
		}
		if probe.ID != nil {
			var r rpcReply
			_ = json.Unmarshal(line, &r)
			p.mu.Lock()
			ch := p.pending[*probe.ID]
			delete(p.pending, *probe.ID)
			p.mu.Unlock()
			if ch != nil {
				ch <- r
			}
			continue
		}
		var ev phdEvent
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		if ev.Event == "Version" {
			p.Version = ev.PHDVersion
			p.verOnce.Do(func() { close(p.verCh) })
		}
		select {
		case p.events <- ev:
		default:
		}
	}
}

func (p *PHD2) Close() error { return p.conn.Close() }

func (p *PHD2) Call(ctx context.Context, method string, params any, out any) error {
	if !PHD2Allowed(method) {
		return fmt.Errorf("%w: PHD2 %s", ErrNotAllowed, method)
	}
	p.mu.Lock()
	p.nextID++
	id := p.nextID
	ch := make(chan rpcReply, 1)
	p.pending[id] = ch
	p.mu.Unlock()
	req := map[string]any{"method": method, "id": id}
	if params != nil {
		req["params"] = params
	}
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if _, err := p.conn.Write(append(b, '\r', '\n')); err != nil {
		return fmt.Errorf("%w %s: %w", ErrPHD2, method, err)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	select {
	case r := <-ch:
		if r.Error != nil {
			return fmt.Errorf("%w %s: %d %s", ErrPHD2, method, r.Error.Code, r.Error.Message)
		}
		if out != nil {
			return json.Unmarshal(r.Result, out)
		}
		return nil
	case <-p.done:
		return ErrPHD2Closed
	case <-ctx.Done():
		return fmt.Errorf("%w %s: %w", ErrPHD2, method, ctx.Err())
	}
}

func (p *PHD2) AppState(ctx context.Context) (string, error) {
	var s string
	err := p.Call(ctx, "get_app_state", nil, &s)
	return s, err
}

func (p *PHD2) EnsureIdle(ctx context.Context) error {
	st, err := p.AppState(ctx)
	if err != nil {
		return err
	}
	if phd2Idle(st) {
		return nil
	}
	if !phd2Stoppable(st) {
		return fmt.Errorf("%w (state %s)", ErrPHD2Busy, st)
	}
	if err := p.Call(ctx, "stop_capture", nil, nil); err != nil {
		return err
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if st, err = p.AppState(ctx); err != nil {
			return err
		}
		if phd2Idle(st) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("%w: PHD2 did not stop looping (state %s)", ErrPHD2, st)
}

func (p *PHD2) PixelScale(ctx context.Context) (float64, error) {
	var v *float64
	if err := p.Call(ctx, "get_pixel_scale", nil, &v); err != nil {
		return 0, err
	}
	if v == nil {
		return 0, nil
	}
	return *v, nil
}

func (p *PHD2) FrameSize(ctx context.Context) (int, int, error) {
	var v []int
	if err := p.Call(ctx, "get_camera_frame_size", nil, &v); err != nil {
		return 0, 0, err
	}
	if len(v) != 2 {
		return 0, 0, fmt.Errorf("%w: frame size %v", ErrPHD2, v)
	}
	return v[0], v[1], nil
}

func (p *PHD2) drain() {
	for {
		select {
		case <-p.events:
		default:
			return
		}
	}
}

func (p *PHD2) Capture(ctx context.Context, exposureMS int, path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if err := p.EnsureIdle(ctx); err != nil {
		return err
	}
	p.drain()
	params := map[string]any{"exposure": max(1, exposureMS), "path": abs}
	if err := p.Call(ctx, "capture_single_frame", params, nil); err != nil {
		return err
	}
	timeout := time.Duration(exposureMS)*time.Millisecond + 60*time.Second
	wait, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	poll := time.NewTicker(500 * time.Millisecond)
	defer poll.Stop()
	idleAfter := time.Now().Add(time.Duration(exposureMS)*time.Millisecond + 2*time.Second)
	idleSeen := 0
	for {
		select {
		case <-poll.C:
			if time.Now().Before(idleAfter) {
				continue
			}
			st, err := p.AppState(ctx)
			if err != nil || !phd2Idle(st) {
				idleSeen = 0
				continue
			}
			if idleSeen++; idleSeen < 3 {
				continue
			}
			if _, err := os.Stat(abs); err == nil {
				return nil
			}
			return p.saveFallback(ctx, abs)
		case ev := <-p.events:
			if ev.Event != "SingleFrameComplete" {
				continue
			}
			if !ev.Success {
				return fmt.Errorf("%w: single frame failed: %s", ErrPHD2, ev.Error)
			}
			if _, err := os.Stat(abs); err == nil {
				return nil
			}
			return p.saveFallback(ctx, abs)
		case <-p.done:
			return ErrPHD2Closed
		case <-wait.Done():
			return fmt.Errorf("%w: no SingleFrameComplete event within %s", ErrPHD2, timeout)
		}
	}
}

func (p *PHD2) saveFallback(ctx context.Context, abs string) error {
	var r struct {
		Filename string `json:"filename"`
	}
	if err := p.Call(ctx, "save_image", nil, &r); err != nil {
		return err
	}
	b, err := os.ReadFile(filepath.Clean(r.Filename))
	if err != nil {
		return fmt.Errorf("%w: read saved image: %w", ErrPHD2, err)
	}
	if err := os.WriteFile(abs, b, 0o600); err != nil {
		return err
	}
	_ = os.Remove(r.Filename)
	return nil
}
