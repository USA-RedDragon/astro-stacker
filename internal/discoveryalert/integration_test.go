package discoveryalert_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/discoveryalert"
	"github.com/USA-RedDragon/astro-stacker/internal/discoveryfollowup"
	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/morningreport"
	"github.com/USA-RedDragon/astro-stacker/internal/planning"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/testdb"
	"gorm.io/gorm"
)

const (
	object     = "Synthetic Field"
	filter     = "Luminance"
	subSeconds = 60
	perNight   = 6
	border     = 12
	findNight  = 3
	followNite = 4
)

type memStore struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (s *memStore) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.m[key]
	if !ok {
		return nil, fmt.Errorf("no %s", key)
	}
	return b, nil
}

func (s *memStore) Put(_ context.Context, key string, data []byte, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = data
	return nil
}

func (s *memStore) RemovePrefix(_ context.Context, prefix string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.m {
		if strings.HasPrefix(k, prefix) {
			delete(s.m, k)
		}
	}
	return nil
}

func (s *memStore) Cutout(ctx context.Context, key string) ([]byte, error) { return s.Get(ctx, key) }

// field is a synthetic target imaged on five nights: a new point source
// appears on the fourth and stays on the fifth.
type field struct {
	w, h   int
	wcs    *discoveries.WCS
	stars  [][3]float64
	nights []time.Time
	newX   float64
	newY   float64
}

func wcsKeys() map[string]string {
	return map[string]string{"CTYPE1": "RA---TAN", "CTYPE2": "DEC--TAN", "CRPIX1": "900.5", "CRPIX2": "640.5", "CRVAL1": "150", "CRVAL2": "20",
		"CD1_1": fmt.Sprint(-2.0 / 3600), "CD1_2": "0", "CD2_1": "0", "CD2_2": fmt.Sprint(2.0 / 3600)}
}

func newField(t *testing.T, findOn time.Time) *field {
	t.Helper()
	rng := rand.New(rand.NewPCG(7, 11))
	f := &field{w: 1800, h: 1280}
	w, err := discoveries.ParseWCS(wcsKeys(), f.h)
	if err != nil {
		t.Fatal(err)
	}
	f.wcs = w
	for len(f.stars) < 1500 {
		f.stars = append(f.stars, [3]float64{20 + rng.Float64()*float64(f.w-40), 20 + rng.Float64()*float64(f.h-40), 0.05 * math.Pow(10, 2.2*rng.Float64())})
	}
	f.newX, f.newY = f.emptySpot(t, 600, 800)
	for n := range followNite + 1 {
		f.nights = append(f.nights, findOn.AddDate(0, 0, n-findNight))
	}
	return f
}

func (f *field) emptySpot(t *testing.T, x0, y0 float64) (float64, float64) {
	t.Helper()
	for r := 0.0; r < 200; r += 3 {
		for a := 0.0; a < 2*math.Pi; a += 0.3 {
			x, y := x0+r*math.Cos(a), y0+r*math.Sin(a)
			ok := true
			for _, s := range f.stars {
				if math.Hypot(s[0]-x, s[1]-y) < 18 {
					ok = false
					break
				}
			}
			if ok {
				return x, y
			}
		}
	}
	t.Fatal("no empty spot")
	return 0, 0
}

func (f *field) subTime(n, i int) time.Time {
	return f.nights[n].Add(4*time.Hour + time.Duration(i)*6*time.Minute)
}

func gauss(d []float32, w, h int, x, y, flux, sigma float64) {
	r := int(math.Ceil(5 * sigma))
	norm := flux / (2 * math.Pi * sigma * sigma)
	for yy := max(0, int(y)-r); yy <= min(h-1, int(y)+r); yy++ {
		for xx := max(0, int(x)-r); xx <= min(w-1, int(x)+r); xx++ {
			dx, dy := float64(xx)-x, float64(yy)-y
			d[yy*w+xx] += float32(norm * math.Exp(-(dx*dx+dy*dy)/(2*sigma*sigma)))
		}
	}
}

func (f *field) render(n, i int) []float32 {
	rng := rand.New(rand.NewChaCha8(sha256.Sum256(fmt.Appendf(nil, "%d/%d", n, i))))
	d := make([]float32, f.w*f.h)
	sigma, scale := 1.3, 1.0
	if n >= 0 {
		sigma = 1.15 + 0.25*rng.Float64()
		scale = 0.9 + 0.2*rng.Float64()
	}
	for _, s := range f.stars {
		gauss(d, f.w, f.h, s[0], s[1], s[2]*scale, sigma)
	}
	if n >= findNight {
		gauss(d, f.w, f.h, f.newX, f.newY, 0.6*scale, sigma)
	}
	for k := range d {
		v := float64(d[k]) + 0.05
		if n >= 0 {
			v += rng.NormFloat64() * 0.002
		}
		d[k] = float32(v)
	}
	if n >= 0 {
		for y := range f.h {
			for x := range f.w {
				if x < border || y < border || x >= f.w-border || y >= f.h-border {
					d[y*f.w+x] = 0
				}
			}
		}
	}
	return d
}

func (f *field) xisf(t *testing.T, n, i int) []byte {
	t.Helper()
	d := f.render(n, i)
	codes := make([]uint16, len(d))
	for k, v := range d {
		codes[k] = imagedata.Quantize16(v)
	}
	var b bytes.Buffer
	if err := imagedata.WriteXISF16(&b, f.w, f.h, codes, nil, imagedata.Pedestal16Properties()); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func (f *field) catalogs(t *testing.T) *discoveries.Catalogs {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/vizier", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("-source") {
		case "I/355/gaiadr3":
			fmt.Fprint(w, "#\nRA_ICRS\tDE_ICRS\tGmag\n-------\t-------\t----\n")
			for _, s := range f.stars {
				ra, dec := f.wcs.SkyOf(s[0], s[1])
				fmt.Fprintf(w, "%.7f\t%+.7f\t%.3f\n", ra, dec, 23.5-2.5*math.Log10(s[2]*1000))
			}
		case "B/vsx/vsx":
			fmt.Fprint(w, "Name\tType\tmax\tmin\tn_max\tPeriod\tRAJ2000\tDEJ2000\n----\t----\t---\t---\t---\t---\t---\t---\n")
		case "I/358/vclassre", "J/AJ/156/241/table4", "J/ApJS/249/18/table2", "II/366/catv2021":
			fmt.Fprint(w, "RAJ2000\tDEJ2000\n----\t----\n")
		default:
			http.Error(w, "unknown", http.StatusBadRequest)
		}
	})
	mux.HandleFunc("/skybot", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "# Flag: 0\n# Ticket: 1\n")
	})
	mux.HandleFunc("/alerce/", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"items":[]}`) })
	mux.HandleFunc("/fink", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `[]`) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &discoveries.Catalogs{VizierURL: srv.URL + "/vizier", SkyBoTURL: srv.URL + "/skybot", AlerceURL: srv.URL + "/alerce/", FinkURL: srv.URL + "/fink"}
}

type world struct {
	f     *field
	db    *gorm.DB
	sched *gorm.DB
	store *memStore
	stack app.Stack
}

func newWorld(t *testing.T, findOn time.Time) *world {
	t.Helper()
	wd := &world{f: newField(t, findOn), db: testdb.SQLite(t, "app"), sched: testdb.SQLite(t, "sched"), store: &memStore{m: map[string][]byte{}}}
	sqlDB, err := wd.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := wd.db.AutoMigrate(&app.Frame{}, &app.Stack{}, &app.StackFrame{}, &app.TargetReference{}, &app.DiscoveryRun{}, &app.DiscoveryCandidate{},
		&app.GoalMeasurement{}, &app.DiscoveryAlert{}, &app.DiscoveryAlertMessage{}, &app.DiscoveryAlertState{}, &app.DiscoveryFollowup{}); err != nil {
		t.Fatal(err)
	}
	var master bytes.Buffer
	if err := imagedata.WriteFITS(&master, wd.f.w, wd.f.h, 1, wd.f.render(-1, 0), nil); err != nil {
		t.Fatal(err)
	}
	key := "stacks/" + object + "/" + filter + "/master.fit"
	wd.store.m[key] = master.Bytes()
	wd.stack = app.Stack{Object: object, Filter: filter, Width: wd.f.w, Height: wd.f.h, Subs: 24, MasterKey: &key, UpdatedAt: time.Now()}
	if err := wd.db.Create(&wd.stack).Error; err != nil {
		t.Fatal(err)
	}
	cards := make([]frameheader.Card, 0, len(wcsKeys()))
	for k, v := range wcsKeys() {
		cards = append(cards, frameheader.Card{Name: k, Value: v})
	}
	raw, err := json.Marshal(cards)
	if err != nil {
		t.Fatal(err)
	}
	ws := string(raw)
	if err := wd.db.Create(&app.TargetReference{Object: object, FrameID: 1, ObjectKey: "x", WCS: &ws}).Error; err != nil {
		t.Fatal(err)
	}
	ra, dec := wd.f.wcs.SkyOf(900, 640)
	for _, s := range []string{
		`create table project ("Id" integer primary key, "profileId" text, name text, description text, state integer, priority integer, minimumtime integer, minimumaltitude real, "maximumAltitude" real, "isMosaic" integer, enablegrader integer, guid text)`,
		`create table target ("Id" integer primary key, name text, active integer, ra real, "dec" real, rotation real, projectid integer, guid text)`,
		`create table exposuretemplate ("Id" integer primary key, "profileId" text, name text, filtername text, gain integer, "offset" integer, bin integer, twilightlevel integer, moonavoidanceenabled integer, moonavoidanceseparation real, moonavoidancewidth integer, maximumhumidity real, defaultexposure real, moonrelaxscale real, moondownenabled integer, ditherevery integer, guid text)`,
		`create table exposureplan ("Id" integer primary key, "profileId" text, exposure real, desired integer, acquired integer, accepted integer, targetid integer, "exposureTemplateId" integer, enabled integer, guid text)`,
		`create table ruleweight ("Id" integer primary key, name text, weight real, projectid integer)`,
		`create table acquiredimage ("Id" integer primary key, "projectId" integer, "targetId" integer, acquireddate integer, filtername text, "gradingStatus" integer, metadata text)`,
		`insert into project values (1,'p','Synthetic',null,1,1,30,20,0,0,1,'pg1')`,
		fmt.Sprintf(`insert into target values (1,'%s',1,%f,%f,0,1,'g-synth')`, object, ra/15, dec),
		`insert into exposuretemplate values (3,'p','Luminance','Luminance',0,50,1,0,1,30,7,85,60,0,0,1,'t3')`,
		`insert into exposureplan values (41,'p',-1,100,24,24,1,3,1,'e41')`,
	} {
		if err := wd.sched.Exec(s).Error; err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	return wd
}

// image stores night n's subs as lights of the target tsTarget, stacked
// two hours earlier.
func (wd *world) image(t *testing.T, n int, tsTarget string) {
	t.Helper()
	processed := time.Now().Add(-2 * time.Hour)
	for i := range perNight {
		k := fmt.Sprintf("registered/%s/%s/%d_%d.xisf", object, filter, n, i)
		wd.store.m[k] = wd.f.xisf(t, n, i)
		start := wd.f.subTime(n, i)
		night := wd.f.nights[n]
		exp := float64(subSeconds)
		g := tsTarget
		fr := app.Frame{Key: fmt.Sprintf("%s/LIGHT/%d_%d.xisf", object, n, i), Type: "LIGHT", Object: object, Filter: filter, Exposure: &exp,
			DateObs: &start, Night: &night, LastModified: start, TSTarget: &g}
		if err := wd.db.Create(&fr).Error; err != nil {
			t.Fatal(err)
		}
		if err := wd.db.Create(&app.StackFrame{FrameID: fr.ID, StackID: &wd.stack.ID, Status: app.StackStatusAdded, Weight: 1, Exposure: exp,
			RegisteredKey: &k, ProcessedAt: processed}).Error; err != nil {
			t.Fatal(err)
		}
	}
}

type sender struct {
	messages []string
	photos   []morningreport.Photo
}

func (s *sender) Message(_ context.Context, html string) error {
	s.messages = append(s.messages, html)
	return nil
}

func (s *sender) Photos(_ context.Context, p []morningreport.Photo) error {
	s.photos = append(s.photos, p...)
	return nil
}

type commands struct{ sent map[string]schedcmd.Record }

func (c *commands) SubmitOnce(_ context.Context, id string, kind schedcmd.Kind, payload json.RawMessage, _ string) (schedcmd.Record, error) {
	if r, ok := c.sent[id]; ok {
		return r, nil
	}
	r := schedcmd.Record{ID: id, Kind: kind, Payload: payload, Status: schedcmd.StatusApplied}
	c.sent[id] = r
	return r, nil
}

func (c *commands) Status(_ context.Context, id string) (schedcmd.Record, error) {
	return c.sent[id], nil
}

// checkSent settles the follow-ups once and checks the project sent for
// the find's field.
func checkSent(ctx context.Context, t *testing.T, wd *world, follow *discoveryfollowup.Service, cmds *commands) (app.DiscoveryFollowup, schedcmd.ProjectCreatePayload) {
	t.Helper()
	if err := follow.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	var fu app.DiscoveryFollowup
	if err := wd.db.First(&fu).Error; err != nil {
		t.Fatal(err)
	}
	if fu.State != app.FollowupWaiting || fu.CreateCmd == "" || fu.Subs != 30 {
		t.Fatalf("follow-up %+v", fu)
	}
	var create schedcmd.ProjectCreatePayload
	if err := json.Unmarshal(cmds.sent[fu.CreateCmd].Payload, &create); err != nil || create.Targets[0].Name != object ||
		create.Targets[0].Window == nil || create.Targets[0].Window.Priority != schedcmd.EventPriorityHigh {
		t.Fatalf("project create %+v %v", create, err)
	}

	return fu, create
}

// skipNightComparisons skips a test that runs night comparisons on a
// synthetic field in short mode, and under the race detector, where three
// take over 20 minutes; the discoveries package's race tests run the
// comparison.
func skipNightComparisons(t *testing.T) {
	t.Helper()
	if testing.Short() || underRace {
		t.Skip("runs the night comparisons on a synthetic field")
	}
}

// TestNewFindAlertsOnceAndIsConfirmedByItsFollowup runs the night
// comparisons on SQLite over a synthetic field: a new source on the find's
// night is alerted once with its picture, a follow-up project for the field
// goes to the scheduler, and the follow-up night's comparison confirms it.
func TestNewFindAlertsOnceAndIsConfirmedByItsFollowup(t *testing.T) {
	t.Parallel()
	skipNightComparisons(t)
	ctx := context.Background()
	tonight := discoveryfollowup.Tonight(time.Now(), time.UTC)
	findOn := tonight.AddDate(0, 0, -2)
	wd := newWorld(t, findOn)
	for n := range findNight + 1 {
		wd.image(t, n, "g-synth")
	}
	runner := discoveries.New(wd.db, nil, wd.store, wd.f.catalogs(t), discoveries.Options{BackfillDays: 1, Threads: 2, RefSubs: 18})
	snd, cmds := &sender{}, &commands{sent: map[string]schedcmd.Record{}}
	follow := &discoveryfollowup.Service{DB: wd.db, Commands: cmds, Loc: time.UTC,
		Snapshot: func(ctx context.Context) (*planning.Snapshot, error) {
			return planning.Load(ctx, wd.sched, wd.db, planning.Inputs{})
		}}
	alerts := &discoveryalert.Service{DB: wd.db, Sender: snd, Cutouts: wd.store, WebURL: "https://astro.test", Loc: time.UTC,
		RetryAfter: time.Minute, MaxAttempts: 3, Followups: follow}
	if err := alerts.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	runner.Request(object, findOn, "")
	if err := runner.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	var run app.DiscoveryRun
	if err := wd.db.Where("night = ?", findOn).First(&run).Error; err != nil || run.State != app.DiscoveryDone {
		t.Fatalf("find night run %+v %v", run, err)
	}
	if err := alerts.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(snd.photos) != 1 || len(snd.messages) != 0 {
		t.Fatalf("want one photo alert, have %d photos and messages %v", len(snd.photos), snd.messages)
	}
	caption := snd.photos[0].Caption
	if !strings.Contains(caption, "possible new variable star in "+object) || !strings.Contains(caption, "?night="+findOn.Format(time.DateOnly)) ||
		len(snd.photos[0].JPEG) < 10000 {
		t.Errorf("caption %q, %d bytes", caption, len(snd.photos[0].JPEG))
	}
	fu, create := checkSent(ctx, t, wd, follow, cmds)

	runner.Request(object, findOn, "")
	if err := runner.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if err := alerts.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(snd.photos) != 1 {
		t.Errorf("a rerun of the find's night alerted again")
	}

	wd.image(t, followNite, fu.TargetGUID)
	runner.Request(object, wd.f.nights[followNite], "")
	if err := runner.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if err := alerts.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(snd.photos) != 1 || strings.Contains(strings.Join(snd.messages, "\n"), "new variable") {
		t.Errorf("the follow-up night alerted the same source as new: %v", snd.messages)
	}
	if err := wd.sched.Exec(`insert into project values (2,'p',?,null,1,2,30,20,0,0,1,?)`, create.Project.Name, fu.ProjectGUID).Error; err != nil {
		t.Fatal(err)
	}
	if err := follow.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if err := wd.db.First(&fu).Error; err != nil {
		t.Fatal(err)
	}
	if fu.State != app.FollowupConfirmed || fu.FollowNight == nil || !fu.FollowNight.Equal(wd.f.nights[followNite]) || fu.SeenSepArc == nil || *fu.SeenSepArc > 1 {
		t.Errorf("follow-up after its night: %+v", fu)
	}
	if fu.CloseCmd == "" || cmds.sent[fu.CloseCmd].Kind != schedcmd.KindProjectEdit {
		t.Errorf("follow-up project not closed: %q", fu.CloseCmd)
	}
}
