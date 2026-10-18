package surveys

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// ErrOffline is the error of every network request and store write an
// offline Fetcher makes.
var ErrOffline = errors.New("surveys: offline, reading the caches only")

type offlineStore struct{ Store }

func (offlineStore) Put(context.Context, string, []byte, string) error { return ErrOffline }

type offlineTransport struct{}

func (offlineTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, ErrOffline }

// Offline is a Fetcher that reads what the vet already keeps under prefix
// in store and never pulls, queues or writes anything.
func Offline(store Store, prefix string, now func() time.Time) *Fetcher {
	return &Fetcher{Store: offlineStore{store}, Prefix: prefix, HTTP: &http.Client{Transport: offlineTransport{}}, Now: now}
}
