package mosaics_test

import (
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
)

func TestProgress(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		it   mosaics.Item
		want float64
	}{
		{"none", mosaics.Item{GoalHours: 10}, 0},
		{"half", mosaics.Item{DoneHours: 5, GoalHours: 10}, 0.5},
		{"done", mosaics.Item{DoneHours: 10, GoalHours: 10}, 1},
		{"over", mosaics.Item{DoneHours: 15, GoalHours: 10}, 1},
		{"negative", mosaics.Item{DoneHours: -1, GoalHours: 10}, 0},
		{"no goal", mosaics.Item{DoneHours: 3}, 1},
		{"negative goal", mosaics.Item{GoalHours: -2}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := mosaics.Progress(tc.it); !near(got, tc.want, 1e-12) {
				t.Fatalf("progress %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSimulate(t *testing.T) {
	t.Parallel()
	behind := []mosaics.Item{
		{Panel: 1, Filter: "Ha", DoneHours: 0, GoalHours: 10},
		{Panel: 2, Filter: "Ha", DoneHours: 5, GoalHours: 10},
	}
	cases := []struct {
		name       string
		items      []mosaics.Item
		s          mosaics.Strategy
		hours      float64
		max        int
		finish     int
		rows       int
		weakest1   float64
		average1   float64
		weakestTol float64
	}{
		{"weakest evens out first", behind, mosaics.StrategyWeakest, 5, 10, 3, 3, 0.5, 0.5, 1e-9},
		{"even splits", behind, mosaics.StrategyEven, 5, 10, 3, 3, 0.25, 0.5, 1e-9},
		{"off feeds the leader", behind, mosaics.StrategyOff, 5, 10, 3, 3, 0.05, 0.5, 0.05},
		{"never within the limit", behind, mosaics.StrategyWeakest, 1, 4, 0, 4, 0.1, 0.3, 1e-9},
		{"zero budget", behind, mosaics.StrategyEven, 0, 3, 0, 3, 0, 0.25, 1e-9},
		{"already done", []mosaics.Item{{DoneHours: 4, GoalHours: 4}, {GoalHours: 0}}, mosaics.StrategyWeakest, 5, 3, 1, 1, 1, 1, 1e-9},
		{"empty", nil, mosaics.StrategyOff, 5, 3, 1, 1, 1, 1, 1e-9},
		{"no seasons", behind, mosaics.StrategyWeakest, 5, 0, 0, 0, 0, 0, 0},
		{"unknown strategy acts as weakest", behind, mosaics.Strategy("bogus"), 5, 10, 3, 3, 0.5, 0.5, 1e-9},
		{"odd increments", []mosaics.Item{{GoalHours: 1.1}, {GoalHours: 0.3}}, mosaics.StrategyEven, 1.4, 5, 1, 1, 1, 1, 1e-9},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			before := append([]mosaics.Item(nil), tc.items...)
			rows, finish := mosaics.Simulate(tc.items, tc.s, tc.hours, tc.max)
			if finish != tc.finish || len(rows) != tc.rows {
				t.Fatalf("finish %d with %d rows, want %d with %d: %+v", finish, len(rows), tc.finish, tc.rows, rows)
			}
			for i := range before {
				if before[i] != tc.items[i] {
					t.Fatalf("input item %d changed", i)
				}
			}
			for i, r := range rows {
				if r.Index != i+1 {
					t.Fatalf("row %d has index %d", i, r.Index)
				}
				if r.Done != (finish == r.Index) {
					t.Fatalf("row %d done %v with finish %d", r.Index, r.Done, finish)
				}
				if r.WeakestProgress > r.AverageProgress+1e-12 {
					t.Fatalf("row %d weakest %v above average %v", r.Index, r.WeakestProgress, r.AverageProgress)
				}
				if i > 0 && r.AverageProgress < rows[i-1].AverageProgress-1e-12 {
					t.Fatalf("progress went backwards: %+v", rows)
				}
			}
			if len(rows) == 0 {
				return
			}
			if !near(rows[0].WeakestProgress, tc.weakest1, tc.weakestTol) {
				t.Fatalf("weakest after season 1 %v, want %v", rows[0].WeakestProgress, tc.weakest1)
			}
			if !near(rows[0].AverageProgress, tc.average1, 1e-9) {
				t.Fatalf("average after season 1 %v, want %v", rows[0].AverageProgress, tc.average1)
			}
		})
	}
}

func TestSimulateStrategiesDiffer(t *testing.T) {
	t.Parallel()
	items := []mosaics.Item{
		{Panel: 1, DoneHours: 12, GoalHours: 20},
		{Panel: 2, DoneHours: 2, GoalHours: 20},
		{Panel: 3, DoneHours: 0, GoalHours: 20},
		{Panel: 4, DoneHours: 9, GoalHours: 20},
	}
	weakest := map[mosaics.Strategy]float64{}
	finish := map[mosaics.Strategy]int{}
	for _, s := range []mosaics.Strategy{mosaics.StrategyWeakest, mosaics.StrategyEven, mosaics.StrategyOff} {
		rows, f := mosaics.Simulate(items, s, 10, 20)
		weakest[s], finish[s] = rows[0].WeakestProgress, f
	}
	if weakest[mosaics.StrategyWeakest] <= weakest[mosaics.StrategyEven] || weakest[mosaics.StrategyEven] <= weakest[mosaics.StrategyOff] {
		t.Fatalf("weakest progress after one season %v", weakest)
	}
	for s, f := range finish {
		if f != 6 {
			t.Fatalf("%s finished in season %d, want 6 since no time is wasted", s, f)
		}
	}
}
