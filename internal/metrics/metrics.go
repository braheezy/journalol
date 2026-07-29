// Package metrics contains the named, deterministic formulas used throughout
// the app. Keeping these definitions here prevents pages and future integrations
// from inventing subtly different versions of the same metric.
package metrics

import (
	"fmt"
	"math"
)

type Direction string

const (
	LowerIsBetter  Direction = "lower"
	HigherIsBetter Direction = "higher"
)

type Definition struct {
	Key          string
	Label        string
	Unit         string
	Direction    Direction
	Version      int
	MinimumGames int
}

var definitions = map[string]Definition{
	"deaths": {
		Key:          "deaths",
		Label:        "Deaths",
		Unit:         "per game",
		Direction:    LowerIsBetter,
		Version:      1,
		MinimumGames: 1,
	},
	"kda": {
		Key:          "kda",
		Label:        "KDA",
		Unit:         "ratio",
		Direction:    HigherIsBetter,
		Version:      1,
		MinimumGames: 1,
	},
	"vision_per_minute": {
		Key:          "vision_per_minute",
		Label:        "Vision score",
		Unit:         "per minute",
		Direction:    HigherIsBetter,
		Version:      1,
		MinimumGames: 1,
	},
	"control_wards": {
		Key:          "control_wards",
		Label:        "Control wards purchased",
		Unit:         "per game",
		Direction:    HigherIsBetter,
		Version:      1,
		MinimumGames: 1,
	},
}

func DefinitionFor(key string) (Definition, bool) {
	definition, ok := definitions[key]
	return definition, ok
}

func KDA(kills, deaths, assists int) float64 {
	denominator := deaths
	if denominator == 0 {
		denominator = 1
	}
	return float64(kills+assists) / float64(denominator)
}

func VisionPerMinute(visionScore float64, durationSeconds int) (float64, bool) {
	if durationSeconds <= 0 {
		return 0, false
	}
	return visionScore / (float64(durationSeconds) / 60), true
}

func Meets(comparator string, actual, threshold float64) (bool, error) {
	switch comparator {
	case "<":
		return actual < threshold, nil
	case "<=":
		return actual <= threshold, nil
	case ">":
		return actual > threshold, nil
	case ">=":
		return actual >= threshold, nil
	case "=":
		return math.Abs(actual-threshold) < 1e-9, nil
	default:
		return false, fmt.Errorf("unsupported comparator %q", comparator)
	}
}
