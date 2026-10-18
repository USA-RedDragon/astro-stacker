package vetting

import (
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/discoverysource"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

func TestPipelineChangeRequeuesEachKind(t *testing.T) {
	t.Parallel()
	night := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	g := 15.0
	mover := discoverysource.Summary{Source: app.DiscoverySource{Kind: app.DiscoveryKindMover}, Class: discoveries.ClassUnknownMover,
		Members: []app.DiscoveryCandidate{{ID: 4, Class: discoveries.ClassUnknownMover, Night: night}}}
	nova := discoverysource.Summary{Source: app.DiscoverySource{Kind: app.DiscoveryKindChange}, Class: discoveries.ClassNova,
		Members: []app.DiscoveryCandidate{{ID: 5, Class: discoveries.ClassNova, Night: night}}}
	star := discoverysource.Summary{Source: app.DiscoverySource{Kind: app.DiscoveryKindChange}, Class: discoveries.ClassNewVariable,
		Best: app.DiscoveryCandidate{GaiaG: &g, Sign: -1}, Members: []app.DiscoveryCandidate{{ID: 6, Class: discoveries.ClassNewVariable, Night: night}}}
	plain := func(s discoverysource.Summary) string {
		x := s
		x.Source.Kind, x.Class, x.Best = "", "", app.DiscoveryCandidate{GaiaG: &g}
		return hashOf(x)
	}
	if hashOf(mover) == plain(mover) || hashOf(nova) == plain(nova) {
		t.Error("a mover or nova kept its old input hash, so it would not be vetted again")
	}
	if pipelineOf(star) != starPipeline || pipelineOf(nova) != transientPipeline || pipelineOf(mover) != moverPipeline {
		t.Error("a source took another kind's pipeline, so changing one kind's checks would vet the wrong sources again")
	}
	if !isTransient(nova) || isTransient(star) || isTransient(mover) {
		t.Error("transient pipeline picked the wrong sources")
	}
}
