package web

import (
	"fmt"
	"strings"

	"journalol/internal/model"
)

type pageData struct {
	Title         string
	CSRFToken     string
	Player        *model.PlayerProfile
	ActiveBlock   *model.TrainingBlock
	Summary       *summaryView
	Matches       []model.Match
	Match         *model.Match
	Review        *reviewView
	ManualTargets []manualTargetView
	Categories    []model.MistakeCategory
	Blocks        []model.TrainingBlock
	Filters       matchFilterView
	Flash         string
	Error         string
	FormError     string
}

type summaryView struct {
	Games               int
	Wins                int
	WinRate             string
	AverageDeaths       string
	KDA                 string
	VisionPerMinute     string
	ControlWardsPerGame string
	PendingReviews      int
	ProgressText        string
}

type reviewView struct {
	Grade          string
	BiggestMistake string
	DoneWell       string
	NextGame       string
}

type manualTargetView struct {
	ID     int64
	Label  string
	Prompt string
	Answer string
}

type matchFilterView struct {
	Champion string
	Role     string
	Result   string
	Notes    string
}

func newSummaryView(stats *model.DashboardStats) *summaryView {
	if stats == nil {
		return nil
	}
	return &summaryView{
		Games:               stats.Games,
		Wins:                stats.Wins,
		WinRate:             formatDecimal(stats.WinRate, 0) + "%",
		AverageDeaths:       formatDecimal(stats.AverageDeaths, 1),
		KDA:                 formatDecimal(stats.KDA, 2),
		VisionPerMinute:     formatDecimal(stats.VisionPerMinute, 2),
		ControlWardsPerGame: formatDecimal(stats.ControlWardsPerGame, 1),
		PendingReviews:      stats.PendingReviews,
		ProgressText:        stats.ProgressText,
	}
}

func newReviewView(review *model.Review) *reviewView {
	if review == nil {
		return nil
	}
	grade := review.Grade
	if review.GradeScale == model.GradeLetter && review.GradeNormalized != nil {
		grade = fmt.Sprintf("%.0f", *review.GradeNormalized)
	}
	return &reviewView{
		Grade:          grade,
		BiggestMistake: review.BiggestMistake,
		DoneWell:       review.DoneWell,
		NextGame:       review.NextGame,
	}
}

func newManualTargetViews(
	checkins []model.ManualTargetCheckin,
	submitted map[int64]string,
) []manualTargetView {
	targets := make([]manualTargetView, 0, len(checkins))
	for _, checkin := range checkins {
		answer := ""
		if checkin.Value != nil {
			if *checkin.Value {
				answer = "yes"
			} else {
				answer = "no"
			}
		}
		if submittedAnswer, ok := submitted[checkin.TargetID]; ok {
			answer = submittedAnswer
		}
		targets = append(targets, manualTargetView{
			ID:     checkin.TargetID,
			Label:  checkin.Label,
			Prompt: checkin.Prompt,
			Answer: answer,
		})
	}
	return targets
}

func formatDecimal(value float64, places int) string {
	formatted := fmt.Sprintf("%.*f", places, value)
	if strings.Contains(formatted, ".") {
		formatted = strings.TrimRight(strings.TrimRight(formatted, "0"), ".")
	}
	if formatted == "" || formatted == "-" {
		return "0"
	}
	return formatted
}
