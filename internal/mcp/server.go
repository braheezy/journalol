// Package mcp exposes Journalol's private coaching context through the Model
// Context Protocol. It is intentionally read-only: a coach can inspect facts
// and reflections but cannot create plans or change journal data on its own.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"journalol/internal/model"
	"journalol/internal/store"
)

const (
	serverName    = "journalol"
	serverVersion = "0.1.0"
)

// Server is a local stdio MCP server backed by the same Journalol store as the
// web app. It has no network listener and no write tools.
type Server struct {
	store    *store.Store
	location *time.Location
}

// NewServer creates a read-only coaching context server.
func NewServer(dataStore *store.Store, location *time.Location) *Server {
	if location == nil {
		location = time.UTC
	}
	return &Server{store: dataStore, location: location}
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Serve processes newline-delimited JSON-RPC requests until the client closes
// stdin. MCP's standard stdio transport reserves stdout exclusively for these
// responses.
func (s *Server) Serve(ctx context.Context, input io.Reader, output io.Writer) error {
	if s == nil || s.store == nil {
		return errors.New("MCP server requires a store")
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	encoder := json.NewEncoder(output)
	for scanner.Scan() {
		var request request
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			if err := encoder.Encode(response{
				JSONRPC: "2.0",
				ID:      json.RawMessage("null"),
				Error:   &rpcError{Code: -32700, Message: "parse error"},
			}); err != nil {
				return fmt.Errorf("write parse error: %w", err)
			}
			continue
		}
		if request.JSONRPC != "2.0" || strings.TrimSpace(request.Method) == "" {
			if err := encoder.Encode(response{
				JSONRPC: "2.0",
				ID:      responseID(request.ID),
				Error:   &rpcError{Code: -32600, Message: "invalid request"},
			}); err != nil {
				return fmt.Errorf("write invalid request: %w", err)
			}
			continue
		}

		// Notifications deliberately have no id and must not receive a reply.
		if len(request.ID) == 0 || string(request.ID) == "null" {
			continue
		}
		result, rpcErr := s.handle(ctx, request)
		message := response{JSONRPC: "2.0", ID: request.ID, Result: result, Error: rpcErr}
		if err := encoder.Encode(message); err != nil {
			return fmt.Errorf("write response: %w", err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read request: %w", err)
	}
	return nil
}

func responseID(id json.RawMessage) json.RawMessage {
	if len(id) == 0 {
		return json.RawMessage("null")
	}
	return id
}

func (s *Server) handle(ctx context.Context, request request) (any, *rpcError) {
	switch request.Method {
	case "initialize":
		return initializeResult(request.Params), nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": toolDefinitions()}, nil
	case "tools/call":
		return s.callTool(ctx, request.Params)
	default:
		return nil, &rpcError{Code: -32601, Message: "method not found"}
	}
}

func initializeResult(params json.RawMessage) map[string]any {
	var input struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(params, &input)
	protocolVersion := "2025-03-26"
	if input.ProtocolVersion == "2024-11-05" || input.ProtocolVersion == "2025-03-26" {
		protocolVersion = input.ProtocolVersion
	}
	return map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities": map[string]any{
			"tools": map[string]any{"listChanged": false},
		},
		"serverInfo":   map[string]string{"name": serverName, "version": serverVersion},
		"instructions": "Journalol is a private League of Legends training journal. Use its imported match data and player reflections to coach deliberately. Treat conclusions as hypotheses, distinguish facts from inferences, and do not give live in-game instructions.",
	}
}

type toolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func toolDefinitions() []toolDefinition {
	noArguments := map[string]any{"type": "object", "additionalProperties": false}
	return []toolDefinition{
		{
			Name:        "journalol_overview",
			Description: "Get the player's aggregate training history, current focus, and journal scope. Start here for a general coaching check-in.",
			InputSchema: noArguments,
		},
		{
			Name:        "recent_matches",
			Description: "List recent Normal Draft and ranked Summoner's Rift matches. Use match_review_context for reflections and detailed data for a specific match.",
			InputSchema: map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"limit":       map[string]any{"type": "integer", "minimum": 1, "maximum": 20, "description": "Number of matches; defaults to 10."},
					"queue_scope": map[string]any{"type": "string", "enum": []string{"all", "ranked", "normal_draft", "solo_duo", "flex"}, "description": "Defaults to all included queues."},
				},
			},
		},
		{
			Name:        "match_review_context",
			Description: "Get one match's factual performance, assigned training focus, completed review, annotation tags, and manual check-ins. Use before offering a detailed post-game coaching review.",
			InputSchema: map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"match_id": map[string]any{"type": "integer", "minimum": 1},
				},
				"required": []string{"match_id"},
			},
		},
		{
			Name:        "active_training_block",
			Description: "Get the current training block and its automatic and manual targets. Returns no active block when the player has no current focus.",
			InputSchema: noArguments,
		},
		{
			Name:        "weekly_coaching_brief",
			Description: "Get a compact coaching packet: aggregate progress, active focus, common self-tagged mistakes, and the latest ten included matches. Useful for weekly retrospectives and regimen adjustments.",
			InputSchema: noArguments,
		},
	}
}

func (s *Server) callTool(ctx context.Context, params json.RawMessage) (any, *rpcError) {
	var call struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(params, &call); err != nil || strings.TrimSpace(call.Name) == "" {
		return nil, &rpcError{Code: -32602, Message: "tools/call requires a tool name"}
	}

	var (
		value any
		err   error
	)
	switch call.Name {
	case "journalol_overview":
		if err = requireNoArguments(call.Arguments); err == nil {
			value, err = s.overview(ctx)
		}
	case "recent_matches":
		value, err = s.recentMatches(ctx, call.Arguments)
	case "match_review_context":
		value, err = s.matchReviewContext(ctx, call.Arguments)
	case "active_training_block":
		if err = requireNoArguments(call.Arguments); err == nil {
			value, err = s.activeTrainingBlock(ctx)
		}
	case "weekly_coaching_brief":
		if err = requireNoArguments(call.Arguments); err == nil {
			value, err = s.weeklyBrief(ctx)
		}
	default:
		return toolError(fmt.Sprintf("unknown tool %q", call.Name)), nil
	}
	if err != nil {
		return toolError(err.Error()), nil
	}
	return toolResult(value), nil
}

func requireNoArguments(arguments map[string]any) error {
	if len(arguments) != 0 {
		return errors.New("this tool does not accept arguments")
	}
	return nil
}

func toolResult(value any) map[string]any {
	encoded, _ := json.Marshal(value)
	return map[string]any{
		"content":           []map[string]string{{"type": "text", "text": string(encoded)}},
		"structuredContent": value,
	}
}

func toolError(message string) map[string]any {
	return map[string]any{
		"content": []map[string]string{{"type": "text", "text": message}},
		"isError": true,
	}
}

type overview struct {
	Player              playerView         `json:"player"`
	IncludedQueues      []string           `json:"included_queues"`
	CareerSummary       summaryView        `json:"career_summary"`
	ActiveTrainingBlock *trainingBlockView `json:"active_training_block"`
	CoachingBoundaries  []string           `json:"coaching_boundaries"`
}

type playerView struct {
	RiotID   string `json:"riot_id"`
	Platform string `json:"platform"`
}

type summaryView struct {
	Games                 int                   `json:"games"`
	Wins                  int                   `json:"wins"`
	WinRate               float64               `json:"win_rate_percent"`
	KDA                   float64               `json:"kda"`
	AverageDeaths         float64               `json:"average_deaths"`
	VisionPerMinute       float64               `json:"vision_per_minute"`
	ControlWardsPerGame   float64               `json:"control_wards_per_game"`
	PendingReviews        int                   `json:"pending_reviews"`
	DeathProgress         string                `json:"death_progress"`
	CommonSelfTaggedAreas []categoryCountView   `json:"common_self_tagged_areas"`
	ChampionPerformance   []championPerformance `json:"champion_performance"`
}

type categoryCountView struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

type championPerformance struct {
	Champion      string  `json:"champion"`
	Games         int     `json:"games"`
	Wins          int     `json:"wins"`
	AverageDeaths float64 `json:"average_deaths"`
}

type trainingBlockView struct {
	ID          int64                `json:"id"`
	Name        string               `json:"name"`
	Description string               `json:"description,omitempty"`
	StartDate   string               `json:"start_date"`
	EndDate     *string              `json:"end_date,omitempty"`
	Status      string               `json:"status"`
	Reminder    string               `json:"reminder,omitempty"`
	Notes       string               `json:"notes,omitempty"`
	Targets     []trainingTargetView `json:"targets"`
}

type trainingTargetView struct {
	Label       string   `json:"label"`
	Type        string   `json:"type"`
	Metric      string   `json:"metric,omitempty"`
	Prompt      string   `json:"prompt,omitempty"`
	Comparator  string   `json:"comparator"`
	Threshold   *float64 `json:"threshold,omitempty"`
	Unit        string   `json:"unit,omitempty"`
	Aggregation string   `json:"aggregation"`
	WindowGames int      `json:"window_games"`
}

func (s *Server) overview(ctx context.Context) (*overview, error) {
	player, err := s.store.PrimaryPlayer(ctx)
	if err != nil {
		return nil, fmt.Errorf("load player: %w", err)
	}
	stats, err := s.store.DashboardStats(ctx, player.ID)
	if err != nil {
		return nil, fmt.Errorf("load summary: %w", err)
	}
	active, err := s.store.ActiveTrainingBlock(ctx, player.ID)
	if err != nil {
		return nil, fmt.Errorf("load active training block: %w", err)
	}
	return &overview{
		Player:              playerView{RiotID: player.RiotID(), Platform: player.PlatformRoute},
		IncludedQueues:      []string{"Normal Draft", "Ranked Solo/Duo", "Ranked Flex"},
		CareerSummary:       newSummaryView(stats),
		ActiveTrainingBlock: newTrainingBlockView(active),
		CoachingBoundaries: []string{
			"Use these facts and player-authored reflections as evidence; label inferences clearly.",
			"This data is for post-game and training planning, not live in-game instruction.",
			"Journalol MCP is read-only. Propose changes in chat; the player makes journal changes in Journalol.",
		},
	}, nil
}

func newSummaryView(stats *model.DashboardStats) summaryView {
	view := summaryView{}
	if stats == nil {
		return view
	}
	view = summaryView{
		Games: stats.Games, Wins: stats.Wins, WinRate: stats.WinRate, KDA: stats.KDA,
		AverageDeaths: stats.AverageDeaths, VisionPerMinute: stats.VisionPerMinute,
		ControlWardsPerGame: stats.ControlWardsPerGame, PendingReviews: stats.PendingReviews,
		DeathProgress:         stats.ProgressText,
		CommonSelfTaggedAreas: make([]categoryCountView, 0, len(stats.CommonMistakes)),
		ChampionPerformance:   make([]championPerformance, 0, len(stats.ByChampion)),
	}
	for _, item := range stats.CommonMistakes {
		view.CommonSelfTaggedAreas = append(view.CommonSelfTaggedAreas, categoryCountView{Label: item.Category.Label, Count: item.Count})
	}
	for _, item := range stats.ByChampion {
		view.ChampionPerformance = append(view.ChampionPerformance, championPerformance{Champion: item.Champion, Games: item.Games, Wins: item.Wins, AverageDeaths: item.AverageDeaths})
	}
	return view
}

func newTrainingBlockView(block *model.TrainingBlock) *trainingBlockView {
	if block == nil {
		return nil
	}
	view := &trainingBlockView{
		ID: block.ID, Name: block.Name, Description: block.Description, StartDate: block.StartDate,
		EndDate: block.EndDate, Status: block.Status, Reminder: block.Reminder, Notes: block.Notes,
		Targets: make([]trainingTargetView, 0, len(block.Targets)),
	}
	for _, target := range block.Targets {
		view.Targets = append(view.Targets, trainingTargetView{
			Label: target.Label, Type: target.Type, Metric: target.MetricKey, Prompt: target.ManualPrompt,
			Comparator: target.Comparator, Threshold: target.Threshold, Unit: target.Unit,
			Aggregation: target.Aggregation, WindowGames: target.WindowGames,
		})
	}
	return view
}

type matchView struct {
	ID                int64   `json:"id"`
	PlayedAt          string  `json:"played_at"`
	Queue             string  `json:"queue"`
	Champion          string  `json:"champion"`
	Role              string  `json:"role"`
	OpponentChampion  string  `json:"opponent_champion,omitempty"`
	Result            string  `json:"result"`
	DurationSeconds   int     `json:"duration_seconds"`
	Kills             int     `json:"kills"`
	Deaths            int     `json:"deaths"`
	Assists           int     `json:"assists"`
	KDA               float64 `json:"kda"`
	CS                int     `json:"cs"`
	Gold              int     `json:"gold"`
	ChampionDamage    int     `json:"champion_damage"`
	VisionScore       int     `json:"vision_score"`
	ControlWards      int     `json:"control_wards"`
	IsRemake          bool    `json:"is_remake"`
	Surrendered       bool    `json:"surrendered"`
	ReviewComplete    bool    `json:"review_complete"`
	TrainingBlockName string  `json:"training_block_name,omitempty"`
}

func (s *Server) recentMatches(ctx context.Context, arguments map[string]any) (any, error) {
	limit, scope, err := matchListArguments(arguments)
	if err != nil {
		return nil, err
	}
	filter := model.MatchFilter{Limit: limit, QueueIDs: queueIDsForScope(scope)}
	matches, err := s.store.ListMatches(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("load recent matches: %w", err)
	}
	view := make([]matchView, 0, len(matches))
	for _, match := range matches {
		view = append(view, s.newMatchView(match))
	}
	return map[string]any{
		"queue_scope": scope,
		"matches":     view,
		"note":        "These are imported match summaries. They do not establish causes by themselves; use the player's review before making detailed conclusions.",
	}, nil
}

func matchListArguments(arguments map[string]any) (int, string, error) {
	limit := 10
	scope := "all"
	for key, value := range arguments {
		switch key {
		case "limit":
			number, ok := value.(float64)
			if !ok || number != float64(int(number)) || number < 1 || number > 20 {
				return 0, "", errors.New("limit must be an integer between 1 and 20")
			}
			limit = int(number)
		case "queue_scope":
			text, ok := value.(string)
			if !ok {
				return 0, "", errors.New("queue_scope must be a string")
			}
			scope = strings.ToLower(strings.TrimSpace(text))
		default:
			return 0, "", fmt.Errorf("unsupported argument %q", key)
		}
	}
	switch scope {
	case "all", "ranked", "normal_draft", "solo_duo", "flex":
		return limit, scope, nil
	default:
		return 0, "", errors.New("queue_scope must be all, ranked, normal_draft, solo_duo, or flex")
	}
}

func queueIDsForScope(scope string) []int {
	switch scope {
	case "ranked":
		return []int{model.QueueRankedSolo, model.QueueRankedFlex}
	case "normal_draft":
		return []int{model.QueueNormalDraft}
	case "solo_duo":
		return []int{model.QueueRankedSolo}
	case "flex":
		return []int{model.QueueRankedFlex}
	default:
		return model.TrainingQueueIDs()
	}
}

func (s *Server) newMatchView(match model.Match) matchView {
	result := "loss"
	if match.Win {
		result = "win"
	}
	return matchView{
		ID: match.ID, PlayedAt: match.StartedAt.In(s.location).Format(time.RFC3339),
		Queue: match.QueueType, Champion: match.Champion, Role: match.Role,
		OpponentChampion: match.OpponentChampion, Result: result,
		DurationSeconds: match.DurationSeconds, Kills: match.Kills, Deaths: match.Deaths,
		Assists: match.Assists, KDA: match.KDA(), CS: match.CS, Gold: match.Gold,
		ChampionDamage: match.ChampionDamage, VisionScore: match.VisionScore,
		ControlWards: match.ControlWards, IsRemake: match.IsRemake,
		Surrendered: match.Surrendered, ReviewComplete: match.ReviewComplete,
		TrainingBlockName: match.BlockName,
	}
}

type matchReviewContext struct {
	Match             matchView           `json:"match"`
	TrainingBlock     *trainingBlockView  `json:"training_block"`
	Review            *reviewView         `json:"review"`
	ManualCheckins    []manualCheckinView `json:"manual_target_checkins"`
	DetailLimitations []string            `json:"detail_limitations"`
}

type reviewView struct {
	Complete       bool                   `json:"complete"`
	Grade          string                 `json:"grade,omitempty"`
	BiggestMistake string                 `json:"biggest_mistake,omitempty"`
	DoneWell       string                 `json:"done_well,omitempty"`
	NextGame       string                 `json:"next_game,omitempty"`
	Annotations    []reviewAnnotationView `json:"annotations"`
}

type reviewAnnotationView struct {
	Category      string `json:"category"`
	Note          string `json:"note,omitempty"`
	EventSecond   *int   `json:"event_second,omitempty"`
	DeathSequence *int   `json:"death_sequence,omitempty"`
}

type manualCheckinView struct {
	Label  string `json:"label"`
	Prompt string `json:"prompt"`
	Answer string `json:"answer"`
	Note   string `json:"note,omitempty"`
}

func (s *Server) matchReviewContext(ctx context.Context, arguments map[string]any) (any, error) {
	if len(arguments) != 1 {
		return nil, errors.New("match_review_context requires only match_id")
	}
	rawID, ok := arguments["match_id"]
	if !ok {
		return nil, errors.New("match_id is required")
	}
	number, ok := rawID.(float64)
	if !ok || number != float64(int64(number)) || number < 1 {
		return nil, errors.New("match_id must be a positive integer")
	}
	detail, err := s.store.GetMatch(ctx, int64(number))
	if errors.Is(err, store.ErrNotFound) {
		return nil, errors.New("match not found")
	}
	if err != nil {
		return nil, fmt.Errorf("load match: %w", err)
	}
	result := &matchReviewContext{
		Match:          s.newMatchView(detail.Match),
		TrainingBlock:  newTrainingBlockView(detail.AssignedBlock),
		ManualCheckins: make([]manualCheckinView, 0, len(detail.ManualTargetCheckins)),
		DetailLimitations: []string{
			"This record has match-level Riot data and player-authored annotations; it does not contain a full replay or team voice context.",
			"Do not infer a single causal mistake from a loss. Tie advice to the player reflection when available.",
		},
	}
	if detail.Review != nil {
		result.Review = newReviewView(detail.Review)
	}
	for _, checkin := range detail.ManualTargetCheckins {
		answer := "not answered"
		if checkin.Value != nil {
			if *checkin.Value {
				answer = "yes"
			} else {
				answer = "no"
			}
		}
		result.ManualCheckins = append(result.ManualCheckins, manualCheckinView{
			Label: checkin.Label, Prompt: checkin.Prompt, Answer: answer, Note: checkin.Note,
		})
	}
	return result, nil
}

func newReviewView(review *model.Review) *reviewView {
	if review == nil {
		return nil
	}
	view := &reviewView{
		Complete: review.Complete, Grade: review.Grade, BiggestMistake: review.BiggestMistake,
		DoneWell: review.DoneWell, NextGame: review.NextGame,
		Annotations: make([]reviewAnnotationView, 0, len(review.Annotations)),
	}
	for _, annotation := range review.Annotations {
		view.Annotations = append(view.Annotations, reviewAnnotationView{
			Category: annotation.CategoryLabel, Note: annotation.Note,
			EventSecond: annotation.EventTimestampSeconds, DeathSequence: annotation.DeathSequence,
		})
	}
	return view
}

func (s *Server) activeTrainingBlock(ctx context.Context) (any, error) {
	block, err := s.store.ActiveTrainingBlock(ctx, 0)
	if err != nil {
		return nil, fmt.Errorf("load active training block: %w", err)
	}
	return map[string]any{"active_training_block": newTrainingBlockView(block)}, nil
}

func (s *Server) weeklyBrief(ctx context.Context) (any, error) {
	overview, err := s.overview(ctx)
	if err != nil {
		return nil, err
	}
	recent, err := s.recentMatches(ctx, map[string]any{"limit": float64(10), "queue_scope": "all"})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"summary":               overview.CareerSummary,
		"active_training_block": overview.ActiveTrainingBlock,
		"recent_matches":        recent.(map[string]any)["matches"],
		"coach_prompt":          "Review this as a bounded training cycle. Identify one demonstrated strength, one or two hypotheses worth testing, and a small next-week practice plan. Do not overstate causality from a small sample.",
	}, nil
}
