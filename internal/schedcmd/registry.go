package schedcmd

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"gorm.io/gorm"
)

type Spec interface {
	Kind() Kind
	Category() Category
	Destination() Destination
	Validate(payload json.RawMessage) error
	Describe(payload json.RawMessage) (Description, error)
	Inverse(payload json.RawMessage) (Kind, json.RawMessage, error)
}

type AppApplier interface {
	ApplyApp(ctx context.Context, db *gorm.DB, payload json.RawMessage) error
}

type Registry struct {
	mu    sync.RWMutex
	specs map[Kind]Spec
}

func NewRegistry(specs ...Spec) *Registry {
	r := &Registry{specs: map[Kind]Spec{}}
	for _, s := range specs {
		r.Register(s)
	}
	return r
}

func (r *Registry) Register(s Spec) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.specs[s.Kind()]; dup {
		panic(fmt.Sprintf("schedcmd: kind %q registered twice", s.Kind()))
	}
	if s.Destination() == DestinationApp {
		if _, ok := s.(AppApplier); !ok {
			panic(fmt.Sprintf("schedcmd: app kind %q has no ApplyApp", s.Kind()))
		}
	}
	r.specs[s.Kind()] = s
}

func (r *Registry) Get(k Kind) (Spec, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.specs[k]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownKind, k)
	}
	return s, nil
}

func (r *Registry) Kinds() []Kind {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Kind, 0, len(r.specs))
	for k := range r.specs {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func Default() *Registry { return NewRegistry(Builtin()...) }
