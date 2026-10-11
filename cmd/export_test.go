package cmd

import (
	"context"

	"github.com/USA-RedDragon/astro-stacker/internal/config"
	"github.com/USA-RedDragon/astro-stacker/internal/events"
	"github.com/USA-RedDragon/astro-stacker/internal/goalmeasure"
	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"gorm.io/gorm"
)

func GoalOptions(cfg *config.Config, boosts goals.SeasonBoostSource, busy func() bool) goalmeasure.Options {
	return goalOptions(cfg, boosts, busy)
}

func AfterScan(db *gorm.DB, broker *events.Broker, publish ...func(context.Context)) func(context.Context) {
	return afterScan(db, broker, publish...)
}
