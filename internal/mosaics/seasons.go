package mosaics

import "math"

type Item struct {
	Panel     int     `json:"panel"`
	Filter    string  `json:"filter"`
	DoneHours float64 `json:"doneHours"`
	GoalHours float64 `json:"goalHours"`
}

type Strategy string

const (
	StrategyWeakest Strategy = "weakest"
	StrategyEven    Strategy = "even"
	StrategyOff     Strategy = "off"
)

type SeasonRow struct {
	Index           int     `json:"index"`
	WeakestProgress float64 `json:"weakestProgress"`
	AverageProgress float64 `json:"averageProgress"`
	Done            bool    `json:"done"`
}

const (
	increment = 0.25
	epsilon   = 1e-9
)

func Progress(it Item) float64 {
	if it.GoalHours <= 0 {
		return 1
	}
	return math.Max(0, math.Min(1, it.DoneHours/it.GoalHours))
}

func finished(it Item) bool {
	return it.GoalHours <= 0 || it.DoneHours >= it.GoalHours-epsilon
}

func Simulate(items []Item, s Strategy, hoursPerSeason float64, maxSeasons int) (rows []SeasonRow, finishSeason int) {
	state := append([]Item(nil), items...)
	cursor := 0
	for season := 1; season <= maxSeasons; season++ {
		budget := hoursPerSeason
		for budget > epsilon && !allFinished(state) {
			amount := math.Min(increment, budget)
			switch s {
			case StrategyEven:
				cursor = nextUnfinished(state, cursor)
				budget -= give(&state[cursor], amount)
				cursor++
			case StrategyOff:
				budget -= spreadByDone(state, amount)
			case StrategyWeakest:
				budget -= give(&state[weakest(state)], amount)
			default:
				budget -= give(&state[weakest(state)], amount)
			}
		}
		row := summarize(state, season)
		rows = append(rows, row)
		if row.Done {
			return rows, season
		}
	}
	return rows, 0
}

func give(it *Item, amount float64) float64 {
	room := it.GoalHours - it.DoneHours
	if amount >= room-epsilon {
		it.DoneHours = it.GoalHours
		return math.Max(room, 0)
	}
	it.DoneHours += amount
	return amount
}

func allFinished(items []Item) bool {
	for _, it := range items {
		if !finished(it) {
			return false
		}
	}
	return true
}

func nextUnfinished(items []Item, from int) int {
	for k := range items {
		i := (from + k) % len(items)
		if !finished(items[i]) {
			return i
		}
	}
	return 0
}

func weakest(items []Item) int {
	best := -1
	for i, it := range items {
		if finished(it) {
			continue
		}
		if best < 0 || Progress(it) < Progress(items[best]) {
			best = i
		}
	}
	return best
}

func spreadByDone(items []Item, amount float64) float64 {
	total := 0.0
	for _, it := range items {
		if !finished(it) {
			total += offWeight(it)
		}
	}
	spent := 0.0
	for i := range items {
		if !finished(items[i]) {
			spent += give(&items[i], amount*offWeight(items[i])/total)
		}
	}
	return spent
}

func summarize(items []Item, index int) SeasonRow {
	row := SeasonRow{Index: index, WeakestProgress: 1, Done: true}
	if len(items) == 0 {
		row.AverageProgress = 1
		return row
	}
	sum := 0.0
	for _, it := range items {
		p := Progress(it)
		sum += p
		row.WeakestProgress = math.Min(row.WeakestProgress, p)
		if !finished(it) {
			row.Done = false
		}
	}
	row.AverageProgress = sum / float64(len(items))
	return row
}

func offWeight(it Item) float64 {
	return math.Max(it.DoneHours, 0) + 0.5
}
