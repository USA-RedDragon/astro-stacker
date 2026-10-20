package surveys

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"time"
)

// Priority orders the stars waiting for room in the ATLAS queue: lower
// goes first, and stars of one priority go in the order they asked.
// ATLAS runs one of our tasks at a time, oldest first, so the order they
// are queued in is the order their answers come back in.
type Priority int

type priorityKey struct{}

// WithPriority marks the ATLAS requests made under ctx with p.
func WithPriority(ctx context.Context, p Priority) context.Context {
	return context.WithValue(ctx, priorityKey{}, p)
}

func priorityOf(ctx context.Context) Priority {
	p, _ := ctx.Value(priorityKey{}).(Priority)
	return p
}

type atlasWant struct {
	field   Field
	t       Target
	refresh bool
	prio    Priority
	seq     int
}

// addWant puts a star in line for room in the ATLAS queue, keeping its
// place when it is already in line; callers hold atlasMu.
func (f *Fetcher) addWant(key string, w atlasWant) {
	if f.wants == nil {
		f.wants = map[string]atlasWant{}
	}
	if old, ok := f.wants[key]; ok {
		w.seq = old.seq
		w.prio = min(w.prio, old.prio)
	} else {
		f.wantSeq++
		w.seq = f.wantSeq
	}
	f.wants[key] = w
}

func (f *Fetcher) wantOrder() []string {
	keys := make([]string, 0, len(f.wants))
	for k := range f.wants {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := f.wants[keys[i]], f.wants[keys[j]]
		if a.prio != b.prio {
			return a.prio < b.prio
		}
		return a.seq < b.seq
	})
	return keys
}

func (f *Fetcher) wantsBefore(key string) int {
	for i, k := range f.wantOrder() {
		if k == key {
			return i
		}
	}
	return 0
}

// atlasServe queues the stars in line, best priority first, while our
// tasks at ATLAS leave room, keeping room for the stars of better priority
// this run is still to ask. It returns the answer for the star at mine
// when that star was queued, and how many of our tasks ATLAS held before;
// callers hold atlasMu.
func (f *Fetcher) atlasServe(ctx context.Context, mine string) (fe Fetch, got bool, queued int, err error) {
	var qp struct {
		Positions map[string]int `json:"queuepositions"`
	}
	if err := f.atlasRequest(ctx, http.MethodGet, f.atlasBase()+"/queuepositions.json", nil, &qp); err != nil {
		return Fetch{}, false, 0, err
	}
	queued = len(qp.Positions)
	free := f.maxQueued() - queued
	for _, k := range f.wantOrder() {
		if free <= 0 {
			break
		}
		w := f.wants[k]
		if f.ATLASAhead != nil && free <= f.ATLASAhead(w.prio) {
			break
		}
		out, throttled := f.atlasPost(ctx, w.field, w.t, w.refresh)
		if k == mine {
			fe, got = out, true
		}
		if throttled {
			break
		}
		delete(f.wants, k)
		free--
	}
	return fe, got, queued, nil
}

// ATLASServe gives the room our tasks at ATLAS left to the stars in line
// for it, as asking for one more star would.
func (f *Fetcher) ATLASServe(ctx context.Context) error {
	f.atlasMu.Lock()
	defer f.atlasMu.Unlock()
	if len(f.wants) == 0 || f.ATLASToken == "" {
		return nil
	}
	_, _, _, err := f.atlasServe(ctx, "")
	return err
}

// ATLASRuntimesKey is where, under the cache prefix, the run times of our
// latest finished ATLAS tasks are kept.
const ATLASRuntimesKey = "atlas-runtimes.json"

const keptRuntimes = 50

// ATLASRuntimes are the run times ATLAS reported for our latest finished
// tasks, newest last. ATLAS runs one of our tasks at a time, so they set
// the pace our line drains at.
type ATLASRuntimes struct {
	Updated time.Time `json:"updated"`
	Seconds []float64 `json:"seconds"`
}

// Mean is the mean run time, zero with none kept.
func (r ATLASRuntimes) Mean() time.Duration {
	if len(r.Seconds) == 0 {
		return 0
	}
	sum := 0.0
	for _, s := range r.Seconds {
		sum += s
	}
	return time.Duration(sum / float64(len(r.Seconds)) * float64(time.Second))
}

// noteRuntime keeps the run time of a finished task from its start and
// finish times as ATLAS gave them; callers hold atlasMu.
func (f *Fetcher) noteRuntime(ctx context.Context, st atlasTaskStatus) {
	if st.Start == nil || st.Finish == nil {
		return
	}
	a, err1 := time.Parse(time.RFC3339, *st.Start)
	b, err2 := time.Parse(time.RFC3339, *st.Finish)
	if err1 != nil || err2 != nil || !b.After(a) {
		return
	}
	key := f.prefix() + ATLASRuntimesKey
	var rt ATLASRuntimes
	raw, err := f.Store.Get(ctx, key)
	switch {
	case err == nil:
		if json.Unmarshal(raw, &rt) != nil {
			rt = ATLASRuntimes{}
		}
	case !isNotFound(err):
		return
	}
	rt.Seconds = append(rt.Seconds, b.Sub(a).Seconds())
	if len(rt.Seconds) > keptRuntimes {
		rt.Seconds = rt.Seconds[len(rt.Seconds)-keptRuntimes:]
	}
	rt.Updated = f.now().UTC()
	if out, err := json.Marshal(rt); err == nil {
		_ = f.Store.Put(ctx, key, out, "application/json")
	}
}
