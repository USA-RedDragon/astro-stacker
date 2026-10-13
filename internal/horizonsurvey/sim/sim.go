package sim

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"time"

	hs "github.com/USA-RedDragon/astro-stacker/internal/horizonsurvey"
	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
)

const jsonrpc = "2.0"

type Clock struct {
	mu  sync.Mutex
	now time.Time
}

func NewClock(t time.Time) *Clock { return &Clock{now: t} }

func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func (c *Clock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.Advance(d)
	return nil
}

type Skyline func(az float64) float64

func DemoSkyline(az float64) float64 {
	alt := 4 + 2*math.Sin(3*az*math.Pi/180) + 1.5*math.Sin(7*az*math.Pi/180+1)
	switch {
	case az >= 40 && az <= 75:
		alt = 18
	case az >= 200 && az <= 201.5:
		alt = 27
	case az >= 290 && az <= 330:
		alt = math.Max(alt, 9+6*math.Sin((az-290)*math.Pi/40))
	}
	return alt
}

func (sky Skyline) Error(alt, az, halfWidth float64) float64 {
	lo, hi := math.Inf(1), math.Inf(-1)
	for a := az - halfWidth; a <= az+halfWidth+1e-9; a += 0.005 {
		v := sky(a)
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	switch {
	case alt < lo:
		return lo - alt
	case alt > hi:
		return alt - hi
	}
	return 0
}

func (c Camera) AzResolution() float64 {
	return hs.BinWidth/2 + 2*float64(hs.BlockSize(c.W))*c.Scale/3600
}

type Camera struct {
	W, H       int
	Scale      float64
	Theta      float64
	ThetaPier  string
	Parity     int
	Noise      float64
	SkyPerMS   float64
	GroundRate float64
	Glow       float64
	StarsPerMP float64
}

func DuskCamera() Camera {
	return Camera{W: 480, H: 270, Scale: 27.5, Theta: 23, ThetaPier: hs.PierWest, Parity: 1, Noise: 0.004, SkyPerMS: 0.02, GroundRate: 0.0006}
}

func NightCamera() Camera {
	c := DuskCamera()
	c.SkyPerMS, c.GroundRate, c.Glow, c.StarsPerMP, c.Noise = 0.00004, 0.000008, 0.5, 900, 0.004
	return c
}

func (c Camera) Calibration() hs.Calibration {
	return hs.Calibration{Theta: c.Theta, PierSide: c.ThetaPier, Parity: c.Parity, Scale: c.Scale, Source: "simulation truth"}
}

func (c Camera) Geometry(lat, ha, dec float64, pier string) hs.Geometry {
	cal := c.Calibration()
	th, _ := cal.ThetaFor(pier)
	return hs.NewMountGeometry(c.W, c.H, lat, ha, dec, th, c.Parity, c.Scale)
}

func Render(g hs.Geometry, cam Camera, sky Skyline, expMS int, seed uint64) []float32 {
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	w, h := g.Size()
	pix := make([]float32, w*h)
	isSky := make([]bool, w*h)
	for y := range h {
		for x := range w {
			az, alt := hs.AltAz(g.Dir(float64(x), float64(y)))
			v := cam.GroundRate
			if alt > sky(az) {
				isSky[y*w+x] = true
				v = cam.SkyPerMS * (1 + 0.15*math.Cos(az*math.Pi/180)) * (1 + cam.Glow*math.Exp(-math.Max(alt, 0)/10))
			}
			pix[y*w+x] = float32(v * float64(expMS))
		}
	}
	nStars := int(cam.StarsPerMP * float64(w*h) / 1e6)
	for range nStars {
		sx, sy := rng.Float64()*float64(w), rng.Float64()*float64(h)
		ix, iy := int(sx), int(sy)
		if !isSky[iy*w+ix] {
			continue
		}
		amp := 0.05 + 0.4*rng.Float64()
		for dy := -4; dy <= 4; dy++ {
			for dx := -4; dx <= 4; dx++ {
				px, py := ix+dx, iy+dy
				if px < 0 || py < 0 || px >= w || py >= h {
					continue
				}
				r2 := (float64(px)-sx)*(float64(px)-sx) + (float64(py)-sy)*(float64(py)-sy)
				pix[py*w+px] += float32(amp * math.Exp(-r2/(2*1.3*1.3)))
			}
		}
	}
	for i := range pix {
		v := float64(pix[i]) + cam.Noise*rng.NormFloat64()
		pix[i] = float32(math.Min(1, math.Max(0, v)))
	}
	return pix
}

type Options struct {
	Site             hs.Site
	Camera           Camera
	Skyline          Skyline
	Clock            *Clock
	SlewTime         time.Duration
	FailSlewAt       int
	UnsafeAfter      int
	SequenceBusy     bool
	SafetyOff        bool
	PHD2State        string
	Solve            bool
	PointingError    float64
	NoPixelScale     bool
	ReportOffset     float64
	ForcePier        string
	ClockSkew        float64
	SequenceAfter    int
	TrackingStuck    bool
	TrackingFailures int
	NoFrameEvent     bool
	SaveDir          string
}

type Mount struct {
	HA, Dec    float64
	Pier       string
	Tracking   bool
	AtHome     bool
	Slewing    bool
	Slews      int
	Homes      int
	Stops      int
	TrackOffs  int
	TrackFails int
}

type Sim struct {
	opts      Options
	trackFail int
	unsaved   []byte
	saved     []string
	mu        sync.Mutex
	mount     Mount
	calls     []string
	phdCall   []string
	frames    uint64
	nina      *httptest.Server
	phdLn     net.Listener
	wg        sync.WaitGroup
}

func Start(ctx context.Context, opts Options) (*Sim, error) {
	if opts.Skyline == nil {
		opts.Skyline = DemoSkyline
	}
	if opts.PHD2State == "" {
		opts.PHD2State = "Stopped"
	}
	if opts.SlewTime == 0 {
		opts.SlewTime = 6 * time.Second
	}
	s := &Sim{opts: opts}
	s.mount = Mount{HA: -90, Dec: 90, Pier: hs.PierWest, AtHome: true}
	s.nina = httptest.NewServer(http.HandlerFunc(s.serveNINA))
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		s.nina.Close()
		return nil, err
	}
	s.phdLn = ln
	s.wg.Add(1)
	go s.acceptPHD2()
	return s, nil
}

func (s *Sim) NINAURL() string  { return s.nina.URL }
func (s *Sim) PHD2Addr() string { return s.phdLn.Addr().String() }

func (s *Sim) Close() {
	s.nina.Close()
	_ = s.phdLn.Close()
	s.wg.Wait()
}

func (s *Sim) Mount() Mount {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mount
}

func (s *Sim) Calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

func (s *Sim) PHD2Calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.phdCall...)
}

func reply(w http.ResponseWriter, ok bool, resp any, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	b, err := json.Marshal(map[string]any{"Response": resp, "Error": msg, "StatusCode": code, "Success": ok, "Type": "API"})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(b)
}

func (s *Sim) lst() float64 { return s.opts.Site.LST(s.opts.Clock.Now()) }

func (s *Sim) mountInfo() map[string]any {
	lst := s.lst()
	m := s.mount
	ha := m.HA
	if !m.AtHome {
		ha += s.opts.ReportOffset
	}
	ra := math.Mod(math.Mod(lst-ha, 360)+360, 360) / 15
	az, alt := s.opts.Site.HADecToAltAz(ha, m.Dec)
	return map[string]any{
		"Connected": true, "AtPark": false, "AtHome": m.AtHome, "Slewing": m.Slewing, "TrackingEnabled": m.Tracking,
		"RightAscension": ra, "Declination": m.Dec, "SiderealTime": (lst + s.opts.ClockSkew) / 15, "Altitude": alt, "Azimuth": az,
		"SiteLatitude": s.opts.Site.Lat, "SiteLongitude": s.opts.Site.Lon, "SideOfPier": m.Pier,
	}
}

func (s *Sim) serveNINA(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := r.URL.Path
	s.calls = append(s.calls, path)
	q := r.URL.Query()
	switch path {
	case "/equipment/mount/info":
		reply(w, true, s.mountInfo(), "", 200)
	case "/equipment/mount/slew":
		s.mount.Slews++
		if s.opts.FailSlewAt > 0 && s.mount.Slews == s.opts.FailSlewAt {
			reply(w, false, "Slew failed", "", 200)
			return
		}
		ra, _ := strconv.ParseFloat(q.Get("ra"), 64)
		dec, _ := strconv.ParseFloat(q.Get("dec"), 64)
		now := s.opts.Clock.Now()
		rd, dd := hs.J2000ToDate(ra, dec, now)
		ha := math.Mod(math.Mod(s.lst()-rd, 360)+540, 360) - 180
		pier := hs.PierEast
		if ha < 0 {
			pier = hs.PierWest
		}
		if s.opts.ForcePier != "" {
			pier = s.opts.ForcePier
		}
		s.mount.HA, s.mount.Dec, s.mount.Pier, s.mount.Tracking, s.mount.AtHome = ha, dd, pier, true, false
		s.trackFail = s.opts.TrackingFailures
		s.opts.Clock.Advance(s.opts.SlewTime)
		reply(w, true, "Slew finished", "", 200)
	case "/equipment/mount/slew/stop":
		s.mount.Stops++
		reply(w, true, "Stopped slew", "", 200)
	case "/equipment/mount/tracking":
		if q.Get("mode") != "4" {
			reply(w, false, nil, "unexpected tracking mode "+q.Get("mode"), 400)
			return
		}
		if s.trackFail > 0 {
			s.trackFail--
			s.mount.TrackFails++
			reply(w, false, nil, "Mount is still settling", 409)
			return
		}
		s.mount.Tracking = s.opts.TrackingStuck && s.mount.AtHome
		s.mount.TrackOffs++
		reply(w, true, "Tracking mode changed", "", 200)
	case "/equipment/mount/home":
		s.mount.Homes++
		s.mount.HA, s.mount.Dec, s.mount.Pier, s.mount.AtHome = -90, 90, hs.PierWest, true
		reply(w, true, "Homing", "", 200)
	case "/sequence/state":
		status := "FINISHED"
		if s.opts.SequenceBusy || (s.opts.SequenceAfter > 0 && s.mount.Slews >= s.opts.SequenceAfter) {
			status = "RUNNING"
		}
		reply(w, true, []any{map[string]any{"GlobalTriggers": []any{}}, map[string]any{"Name": "Start_Container", "Status": status}}, "", 200)
	case "/equipment/safetymonitor/info":
		safe := s.opts.UnsafeAfter <= 0 || s.mount.Slews < s.opts.UnsafeAfter
		reply(w, true, map[string]any{"Connected": !s.opts.SafetyOff, "IsSafe": safe}, "", 200)
	case "/profile/horizon":
		reply(w, true, map[string]any{"Altitudes": []float64{}, "Azimuths": []float64{}}, "", 200)
	default:
		reply(w, false, nil, "not simulated: "+path, 404)
	}
}

func (s *Sim) acceptPHD2() {
	defer s.wg.Done()
	for {
		c, err := s.phdLn.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.servePHD2(c)
		}()
	}
}

func (s *Sim) servePHD2(c net.Conn) {
	defer func() { _ = c.Close() }()
	var wmu sync.Mutex
	send := func(v any) {
		b, err := json.Marshal(v)
		if err != nil {
			return
		}
		wmu.Lock()
		_, _ = c.Write(append(b, '\r', '\n'))
		wmu.Unlock()
	}
	send(map[string]any{"Event": "Version", "PHDVersion": "2.6.13-sim", "Jsonrpc": jsonrpc})
	sc := bufio.NewScanner(c)
	for sc.Scan() {
		var req struct {
			Method string          `json:"method"`
			ID     int             `json:"id"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(bytes.TrimSpace(sc.Bytes()), &req) != nil {
			continue
		}
		s.mu.Lock()
		s.phdCall = append(s.phdCall, req.Method)
		s.mu.Unlock()
		ok := func(v any) { send(map[string]any{"jsonrpc": jsonrpc, "result": v, "id": req.ID}) }
		fail := func(msg string) {
			send(map[string]any{"jsonrpc": jsonrpc, "error": map[string]any{"code": 1, "message": msg}, "id": req.ID})
		}
		switch req.Method {
		case "get_app_state":
			s.mu.Lock()
			st := s.opts.PHD2State
			s.mu.Unlock()
			ok(st)
		case "stop_capture":
			s.mu.Lock()
			s.opts.PHD2State = "Stopped"
			s.mu.Unlock()
			ok(0)
		case "get_pixel_scale":
			if s.opts.NoPixelScale {
				ok(nil)
			} else {
				ok(s.opts.Camera.Scale)
			}
		case "get_camera_frame_size":
			ok([]int{s.opts.Camera.W, s.opts.Camera.H})
		case "capture_single_frame":
			var p struct {
				Exposure int    `json:"exposure"`
				Path     string `json:"path"`
			}
			_ = json.Unmarshal(req.Params, &p)
			if err := s.capture(p.Exposure, p.Path); err != nil {
				fail(err.Error())
				continue
			}
			ok(0)
			if !s.opts.NoFrameEvent {
				send(map[string]any{"Event": "SingleFrameComplete", "Success": true, "Path": p.Path})
			}
		case "save_image":
			name, err := s.saveImage()
			if err != nil {
				fail(err.Error())
				continue
			}
			ok(map[string]any{"filename": name})
		default:
			fail("not simulated: " + req.Method)
		}
	}
}

func (s *Sim) capture(expMS int, path string) error {
	s.mu.Lock()
	m := s.mount
	s.frames++
	seed := s.frames
	now := s.opts.Clock.Now()
	s.mu.Unlock()
	pier := m.Pier
	if pier != hs.PierEast && pier != hs.PierWest {
		pier = hs.PierWest
	}
	g := s.opts.Camera.Geometry(s.opts.Site.Lat, m.HA+s.opts.PointingError, m.Dec, pier)
	pix := Render(g, s.opts.Camera, s.opts.Skyline, expMS, seed)
	var cards []imagedata.Card
	if s.opts.Solve {
		mid := now.Add(time.Duration(expMS) * time.Millisecond / 2)
		for k, v := range hs.WCSFromGeometry(g, s.opts.Site, mid).Cards() {
			if k == "CTYPE1" || k == "CTYPE2" {
				cards = append(cards, imagedata.StringCard(k, v, ""))
			} else {
				cards = append(cards, imagedata.Card{Key: k, Value: v})
			}
		}
	}
	var buf bytes.Buffer
	if err := imagedata.WriteFITS(&buf, s.opts.Camera.W, s.opts.Camera.H, 1, pix, cards); err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("destination file already exists")
	}
	s.opts.Clock.Advance(time.Duration(expMS)*time.Millisecond + time.Second)
	if s.opts.NoFrameEvent {
		s.mu.Lock()
		s.unsaved = buf.Bytes()
		s.mu.Unlock()
		return nil
	}
	return os.WriteFile(path, buf.Bytes(), 0o600)
}

func (s *Sim) saveImage() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unsaved == nil {
		return "", fmt.Errorf("no image to save")
	}
	f, err := os.CreateTemp(s.opts.SaveDir, "phd2_sim_*.fits")
	if err != nil {
		return "", err
	}
	if _, err := f.Write(s.unsaved); err != nil {
		_ = f.Close()
		return "", err
	}
	s.saved = append(s.saved, f.Name())
	return f.Name(), f.Close()
}

func (s *Sim) SavedImages() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.saved...)
}

func (s *Sim) Camera() Camera { return s.opts.Camera }
