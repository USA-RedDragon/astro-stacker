package discover_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	pgdriver "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestPickOnPostgres(t *testing.T) {
	t.Parallel()
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()
	c, err := postgres.Run(ctx, "postgres:17-alpine", postgres.WithDatabase("stacker"), postgres.WithUsername("stacker"), postgres.WithPassword("stacker"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(2*time.Minute)))
	testcontainers.CleanupContainer(t, c)
	if err != nil {
		t.Fatal(err)
	}
	dsn, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(pgdriver.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	s := pickFixtureOn(t, db)
	p, err := s.Pick(ctx, "M16", pickPlans())
	if err != nil {
		t.Fatal(err)
	}
	if p.Palette != "HOO + S-II" || len(p.Rules) != 1 || !p.Rules[0].Derived {
		t.Fatalf("pick %+v", p)
	}
	ha := filterOf(p, "H-α")
	if ha.Exposure == nil || *ha.Exposure != 300 || ha.Subs != 7 || ha.Hours.Hours != nil || ha.Hours.Unknown == "" || ha.Hours.Points != 4 {
		t.Errorf("H-α %+v", ha)
	}
	g, err := s.Pick(ctx, "M81", pickPlans())
	if err != nil {
		t.Fatal(err)
	}
	l := filterOf(g, "L")
	if l.Hours.Hours == nil || math.IsNaN(*l.Hours.Hours) || l.Hours.Points != 1 || l.Subs != 13 {
		t.Errorf("L %+v", l)
	}
	d, err := s.Pick(ctx, "B33", pickPlans())
	if err != nil {
		t.Fatal(err)
	}
	if l := filterOf(d, "L"); l.Exposure == nil || *l.Exposure != 120 {
		t.Errorf("dust L %+v", l)
	}
}
