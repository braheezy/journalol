// Package model contains the small set of domain values shared by Journalol's
// application, persistence, and presentation layers. The types deliberately do
// not contain database or HTTP concerns.
package model

import (
	"fmt"
	"strings"
	"time"
)

const (
	TrainingBlockPlanned   = "planned"
	TrainingBlockActive    = "active"
	TrainingBlockCompleted = "completed"
	TrainingBlockAbandoned = "abandoned"

	TargetAutomatic = "automatic"
	TargetManual    = "manual"

	GradeNumeric = "numeric"
	GradeLetter  = "letter"
)

// PlayerProfile is the one locally configured League player.
type PlayerProfile struct {
	ID               int64
	GameName         string
	TagLine          string
	PlatformRoute    string
	RegionalRoute    string
	PUUID            string
	ProfileIconID    *int64
	SummonerLevel    *int64
	IsPrimary        bool
	IsDemo           bool
	PollIntervalMins int
	HistoryLimit     int
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// RiotID returns the player's current display identity.
func (p PlayerProfile) RiotID() string {
	if p.TagLine == "" {
		return p.GameName
	}
	return p.GameName + "#" + p.TagLine
}

// DisplayName is kept separate so a future account label can differ from the
// Riot ID without changing presentation callers.
func (p PlayerProfile) DisplayName() string {
	return p.RiotID()
}

// TrainingTarget is a locked goal definition once its block is activated.
type TrainingTarget struct {
	ID           int64
	BlockID      int64
	Type         string
	Label        string
	MetricKey    string
	ManualPrompt string
	Comparator   string
	Threshold    *float64
	Unit         string
	Aggregation  string
	WindowGames  int
	DisplayOrder int
}

// TrainingBlock is a bounded period of deliberate practice.
type TrainingBlock struct {
	ID            int64
	PlayerID      int64
	Name          string
	Description   string
	StartDate     string
	EndDate       *string
	Status        string
	Reminder      string
	Notes         string
	Retrospective string
	Targets       []TrainingTarget
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// CreateTrainingBlockParams is accepted by the persistence/application
// boundary. New blocks always begin in the planned state.
type CreateTrainingBlockParams struct {
	PlayerID    int64
	Name        string
	Description string
	StartDate   string
	EndDate     *string
	Reminder    string
	Notes       string
	Targets     []TrainingTargetInput
}

// TrainingTargetInput contains only user-editable target fields.
type TrainingTargetInput struct {
	Type         string
	Label        string
	MetricKey    string
	ManualPrompt string
	Comparator   string
	Threshold    *float64
	Unit         string
	Aggregation  string
	WindowGames  int
}

// Match is the primary player's useful match summary. It intentionally flattens
// the participant row because Journalol is single-player in its first release.
type Match struct {
	ID              int64
	PlayerID        int64
	RiotMatchID     string
	QueueID         int
	QueueType       string
	GameMode        string
	Patch           string
	StartedAt       time.Time
	EndedAt         time.Time
	DurationSeconds int
	IsRemake        bool
	Surrendered     bool
	Completeness    string

	ChampionID       int
	Champion         string
	Role             string
	Win              bool
	Kills            int
	Deaths           int
	Assists          int
	CS               int
	Gold             int
	ChampionDamage   int
	VisionScore      int
	WardsPlaced      int
	WardsKilled      int
	ControlWards     int
	OpponentChampion string

	TrainingBlockID *int64
	BlockName       string
	ReviewComplete  bool
}

// KDA returns the conventional per-match KDA while keeping the raw K/D/A
// available for display.
func (m Match) KDA() float64 {
	deaths := m.Deaths
	if deaths < 1 {
		deaths = 1
	}
	return float64(m.Kills+m.Assists) / float64(deaths)
}

// StartedAtLabel provides a compact localizable-enough demo label. Callers that
// know the configured timezone can format StartedAt themselves.
func (m Match) StartedAtLabel() string {
	return m.StartedAt.Format("Jan 2, 3:04 PM")
}

// DurationLabel returns a familiar m:ss match duration.
func (m Match) DurationLabel() string {
	if m.DurationSeconds < 0 {
		return "—"
	}
	return fmt.Sprintf("%d:%02d", m.DurationSeconds/60, m.DurationSeconds%60)
}

// MatchFilter defines parameterized list filters. Limit is clamped by the
// store, and a zero limit selects its default.
type MatchFilter struct {
	PlayerID        int64
	Champion        string
	Role            string
	QueueType       string
	Result          *bool
	From            *time.Time
	To              *time.Time
	TrainingBlockID *int64
	Reviewed        *bool
	NotesQuery      string
	Limit           int
	Offset          int
}

// MatchDetail contains the review and display-oriented imported structures that
// are not needed on the match list.
type MatchDetail struct {
	Match
	Items                []int
	Runes                []int
	SummonerSpells       []int
	SkillOrder           []int
	AssignedBlock        *TrainingBlock
	ManualTargetCheckins []ManualTargetCheckin
	Review               *Review
	SelectedCategoryIDs  []int64
}

// Review is the fast post-game reflection attached to one match.
type Review struct {
	ID              int64
	MatchID         int64
	PlayerID        int64
	GradeScale      string
	Grade           string
	GradeNormalized *float64
	BiggestMistake  string
	DoneWell        string
	NextGame        string
	Complete        bool
	Annotations     []ReviewAnnotation
	CreatedAt       time.Time
	UpdatedAt       time.Time
	CompletedAt     *time.Time
}

// ReviewAnnotation tags either the whole match or a known event timestamp.
type ReviewAnnotation struct {
	ID                    int64
	ReviewID              int64
	CategoryID            int64
	CategorySlug          string
	CategoryLabel         string
	EventTimestampSeconds *int
	DeathSequence         *int
	Note                  string
}

// ReviewAnnotationInput is used when replacing a review's annotations.
type ReviewAnnotationInput struct {
	CategoryID            int64
	EventTimestampSeconds *int
	DeathSequence         *int
	Note                  string
}

// UpsertReviewParams saves either a draft or a completed review. CategoryIDs is
// a convenient shorthand for whole-match tags; Annotations supports event tags.
type UpsertReviewParams struct {
	MatchID              int64
	PlayerID             int64
	GradeScale           string
	Grade                string
	BiggestMistake       string
	DoneWell             string
	NextGame             string
	Complete             bool
	CategoryIDs          []int64
	Annotations          []ReviewAnnotationInput
	ManualTargetCheckins []ManualTargetCheckinInput
}

// ManualTargetCheckin is one manual target as presented for a particular
// match. A nil Value means the target has not been answered yet; a pointer to
// false is an explicit "no" response.
type ManualTargetCheckin struct {
	TargetID int64
	MatchID  int64
	BlockID  int64
	Label    string
	Prompt   string
	Value    *bool
	Note     string
}

// ManualTargetCheckinInput records a yes/no response for a manual target. The
// presence of an input is distinct from its Value, so false remains meaningful.
type ManualTargetCheckinInput struct {
	TargetID int64
	Value    bool
	Note     string
}

// MistakeCategory is a stable tag used across reviews.
type MistakeCategory struct {
	ID       int64
	Slug     string
	Label    string
	IsActive bool
	IsCustom bool
	Selected bool
}

// CategoryCount is a dashboard aggregate.
type CategoryCount struct {
	Category MistakeCategory
	Count    int
}

// ChampionPerformance is a compact dashboard split.
type ChampionPerformance struct {
	Champion      string
	Games         int
	Wins          int
	AverageDeaths float64
}

// DashboardStats contains deterministic aggregate inputs. ProgressText is
// generated from adjacent, equal-size death windows rather than from an AI.
type DashboardStats struct {
	Games                 int
	Wins                  int
	WinRate               float64
	AverageDeaths         float64
	KDA                   float64
	VisionPerMinute       float64
	ControlWardsPerGame   float64
	PendingReviews        int
	LatestDeathsAverage   *float64
	PreviousDeathsAverage *float64
	ProgressWindowGames   int
	ProgressText          string
	CommonMistakes        []CategoryCount
	ByChampion            []ChampionPerformance
}

// TargetResult is the versioned result of applying one target to one match.
type TargetResult struct {
	ID               int64
	TargetID         int64
	MatchID          int64
	ActualValue      *float64
	State            string
	EvaluatorVersion int
	IsCurrent        bool
	EvaluatedAt      time.Time
}

// NormalizeText trims form values without changing intentional internal
// whitespace. It is useful to callers constructing model inputs.
func NormalizeText(value string) string {
	return strings.TrimSpace(value)
}
