package skyevents

import (
	"testing"
	"time"
)

func rareEvent(kind string, members ...Member) Event {
	return Event{Kind: kind, Members: members, End: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
		Rarity: &Rarity{Status: StatusReady, Rare: true}}
}

func TestEventsWithAddedBodiesAreNotAutoPlanned(t *testing.T) {
	t.Parallel()
	comet := Member{Name: "161P/Hartley-IRAS", Kind: KindComet}
	dso := Member{Name: "M 57", Kind: KindDSO}
	planets := rareEvent(EventBodyBody, Member{Name: "Venus", Kind: KindPlanet}, Member{Name: "Mars", Kind: KindPlanet})
	withComet := rareEvent(EventBodyDSO, comet, dso)
	if !planets.AutoPlannable() {
		t.Fatal("a rare planet event is not auto-plannable")
	}
	if withComet.AutoPlannable() {
		t.Fatal("a comet event was auto-plannable")
	}
	now := time.Date(2026, 10, 12, 18, 0, 0, 0, time.UTC)
	st := autoState(withComet, nil, Planning{Available: true}, now)
	if st == nil || st.State != AutoSkipped {
		t.Fatalf("comet event auto state %+v", st)
	}
}

func TestTwoBodyMosaicsWithAnAddedBodyAreDropped(t *testing.T) {
	t.Parallel()
	comet := Member{Name: "161P/Hartley-IRAS", Kind: KindComet}
	moon := Member{Name: "Moon", Kind: KindMoon}
	kept := rareEvent(EventMosaic2, Member{Name: "Venus", Kind: KindPlanet}, Member{Name: "Mars", Kind: KindPlanet}, Member{Name: "M 44", Kind: KindDSO})
	dropped := rareEvent(EventMosaic2, moon, comet)
	out := WorthAMosaic([]Event{kept, dropped})
	if len(out) != 1 || out[0].Members[2].Name != "M 44" {
		t.Fatalf("got %+v", out)
	}
}
