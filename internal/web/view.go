package web

import (
	"fmt"
	"strings"
	"time"

	"journalol/internal/model"
	"journalol/internal/store"
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
	SyncStatus    *syncStatusView
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
	Queue    string
	Result   string
	Notes    string
}

type syncStatusView struct {
	HasRun          bool
	CanSync         bool
	StateClass      string
	StateLabel      string
	CompletedLabel  string
	Message         string
	DiscoveredCount int
	ImportedCount   int
	SkippedCount    int
	FailedCount     int
}

func newSyncStatusView(run *store.SyncRun, location *time.Location, canSync bool) *syncStatusView {
	status := &syncStatusView{
		CanSync:    canSync,
		StateClass: "idle",
		StateLabel: "Not synced yet",
		Message:    "Bring in recent matches from Riot when you are ready.",
	}
	if run == nil {
		return status
	}

	status.HasRun = true
	status.DiscoveredCount = run.DiscoveredCount
	status.ImportedCount = run.ImportedCount
	status.SkippedCount = run.SkippedCount
	status.FailedCount = run.FailedCount
	if run.CompletedAt != nil {
		if location == nil {
			location = time.UTC
		}
		status.CompletedLabel = run.CompletedAt.In(location).Format("Jan 2, 3:04 PM")
	}

	switch run.State {
	case store.SyncStateSucceeded:
		status.StateClass = "success"
		status.StateLabel = "Up to date"
		status.Message = "The latest Riot match history was checked successfully."
	case store.SyncStatePartial:
		status.StateClass = "partial"
		status.StateLabel = "Partially updated"
		status.Message = "Some matches could not be updated. It is safe to try the sync again."
	case store.SyncStateFailed:
		status.StateClass = "failed"
		status.StateLabel = "Needs attention"
		status.Message = "Riot data could not be refreshed. Check that your API key is current, then try again."
	case store.SyncStateRunning:
		status.StateClass = "running"
		status.StateLabel = "Syncing"
		status.Message = "Recent Riot matches are being checked now."
	default:
		status.StateClass = "idle"
		status.StateLabel = "Last sync recorded"
		status.Message = "The most recent sync has an unknown state. It is safe to try again."
	}
	return status
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
