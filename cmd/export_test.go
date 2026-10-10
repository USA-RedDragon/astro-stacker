package cmd

import (
	"github.com/USA-RedDragon/astro-stacker/internal/config"
	"github.com/USA-RedDragon/astro-stacker/internal/goalmeasure"
	"github.com/USA-RedDragon/astro-stacker/internal/goals"
)

func GoalOptions(cfg *config.Config, boosts goals.SeasonBoostSource, busy func() bool) goalmeasure.Options {
	return goalOptions(cfg, boosts, busy)
}
