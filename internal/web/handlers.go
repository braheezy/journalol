package web

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"journalol/internal/model"
	"journalol/internal/store"
)

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	player, err := s.store.PrimaryPlayer(r.Context())
	if err != nil {
		s.renderStoreError(w, r, err)
		return
	}
	activeBlock, err := s.store.ActiveTrainingBlock(r.Context(), player.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.renderStoreError(w, r, err)
		return
	}
	matches, err := s.store.ListMatches(r.Context(), model.MatchFilter{
		PlayerID: player.ID,
		QueueIDs: model.TrainingQueueIDs(),
		Limit:    6,
	})
	if err != nil {
		s.renderStoreError(w, r, err)
		return
	}
	stats, err := s.store.DashboardStats(r.Context(), player.ID)
	if err != nil {
		s.renderStoreError(w, r, err)
		return
	}

	var syncStatus *syncStatusView
	if !player.IsDemo {
		latestSync, latestErr := s.store.LatestSyncRun(r.Context(), player.ID)
		if latestErr != nil && !errors.Is(latestErr, store.ErrNotFound) {
			s.renderStoreError(w, r, latestErr)
			return
		}
		syncStatus = newSyncStatusView(latestSync, s.location, s.syncer != nil)
	}

	s.localizeMatches(matches)
	data := pageData{
		Title:       "Dashboard",
		CSRFToken:   csrfToken(r),
		Player:      player,
		ActiveBlock: activeBlock,
		Summary:     newSummaryView(stats),
		Matches:     matches,
		SyncStatus:  syncStatus,
		Flash:       flashMessage(r),
	}
	s.render(w, http.StatusOK, "dashboard", data)
}

func (s *Server) syncRiotData(w http.ResponseWriter, r *http.Request) {
	player, err := s.store.PrimaryPlayer(r.Context())
	if err != nil {
		s.renderStoreError(w, r, err)
		return
	}
	if player.IsDemo || s.syncer == nil {
		s.renderError(w, r, http.StatusNotFound, "Sync unavailable", "Riot sync is not configured for this profile.")
		return
	}

	run, err := s.syncer.Sync(r.Context(), store.SyncTriggerManual)
	if err != nil {
		s.logger.ErrorContext(r.Context(), "manual Riot sync failed", "error", err)
		s.renderError(
			w,
			r,
			http.StatusBadGateway,
			"Riot sync could not finish",
			"Your saved games were not removed. Check that your Riot API key is current, then try again.",
		)
		return
	}

	flash := "sync-complete"
	if run != nil && run.State == store.SyncStatePartial {
		flash = "sync-partial"
	}
	http.Redirect(w, r, "/?flash="+flash, http.StatusSeeOther)
}

func (s *Server) matches(w http.ResponseWriter, r *http.Request) {
	player, err := s.store.PrimaryPlayer(r.Context())
	if err != nil {
		s.renderStoreError(w, r, err)
		return
	}

	filter, err := matchFilterFromRequest(r, player.ID)
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, "Invalid match filter", err.Error())
		return
	}
	matches, err := s.store.ListMatches(r.Context(), filter)
	if err != nil {
		s.renderStoreError(w, r, err)
		return
	}
	s.localizeMatches(matches)

	data := pageData{
		Title:     "Matches",
		CSRFToken: csrfToken(r),
		Player:    player,
		Matches:   matches,
		Filters: matchFilterView{
			Champion: r.URL.Query().Get("champion"),
			Role:     r.URL.Query().Get("role"),
			Queue:    queueScopeFromRequest(r),
			Result:   r.URL.Query().Get("result"),
			Notes:    r.URL.Query().Get("q"),
		},
		Flash: flashMessage(r),
	}
	s.render(w, http.StatusOK, "matches", data)
}

func (s *Server) matchDetail(w http.ResponseWriter, r *http.Request) {
	matchID, err := positivePathID(r)
	if err != nil {
		s.renderError(w, r, http.StatusNotFound, "Match not found", "That match does not exist.")
		return
	}
	player, err := s.store.PrimaryPlayer(r.Context())
	if err != nil {
		s.renderStoreError(w, r, err)
		return
	}
	detail, err := s.store.GetMatch(r.Context(), matchID)
	if err != nil {
		s.renderStoreError(w, r, err)
		return
	}
	categories, err := s.store.MistakeCategories(r.Context())
	if err != nil {
		s.renderStoreError(w, r, err)
		return
	}

	selected := make(map[int64]struct{}, len(detail.SelectedCategoryIDs))
	for _, categoryID := range detail.SelectedCategoryIDs {
		selected[categoryID] = struct{}{}
	}
	for index := range categories {
		_, categories[index].Selected = selected[categories[index].ID]
	}

	detail.StartedAt = detail.StartedAt.In(s.location)
	detail.EndedAt = detail.EndedAt.In(s.location)
	match := detail.Match
	data := pageData{
		Title:         "Review " + match.Champion,
		CSRFToken:     csrfToken(r),
		Player:        player,
		Match:         &match,
		Review:        newReviewView(detail.Review),
		ManualTargets: newManualTargetViews(detail.ManualTargetCheckins, nil),
		Categories:    categories,
		Flash:         flashMessage(r),
	}
	s.render(w, http.StatusOK, "match_detail", data)
}

func (s *Server) saveReview(w http.ResponseWriter, r *http.Request) {
	matchID, err := positivePathID(r)
	if err != nil {
		s.renderError(w, r, http.StatusNotFound, "Match not found", "That match does not exist.")
		return
	}
	player, err := s.store.PrimaryPlayer(r.Context())
	if err != nil {
		s.renderStoreError(w, r, err)
		return
	}
	detail, err := s.store.GetMatch(r.Context(), matchID)
	if err != nil {
		s.renderStoreError(w, r, err)
		return
	}
	if detail.IsRemake {
		s.renderError(
			w,
			r,
			http.StatusBadRequest,
			"Remakes do not need a review",
			"This match is excluded from training progress, so there is nothing to grade.",
		)
		return
	}

	grade := strings.TrimSpace(r.PostForm.Get("grade"))
	gradeNumber, err := strconv.Atoi(grade)
	if err != nil || gradeNumber < 1 || gradeNumber > 5 {
		s.renderReviewFormError(w, r, matchID, "Choose a focus grade from 1 to 5.")
		return
	}

	biggestMistake := strings.TrimSpace(r.PostForm.Get("biggest_mistake"))
	doneWell := strings.TrimSpace(r.PostForm.Get("done_well"))
	nextGame := strings.TrimSpace(r.PostForm.Get("next_game"))
	if biggestMistake == "" && doneWell == "" && nextGame == "" {
		s.renderReviewFormError(w, r, matchID, "Write at least one short reflection before saving.")
		return
	}
	if tooLong(biggestMistake, 500) || tooLong(doneWell, 500) || tooLong(nextGame, 500) {
		s.renderReviewFormError(w, r, matchID, "Keep each reflection to 500 characters or fewer.")
		return
	}

	categoryIDs, err := parsePositiveIDs(r.PostForm["category_ids"])
	if err != nil {
		s.renderReviewFormError(w, r, matchID, "One of the selected mistake categories was invalid.")
		return
	}
	if len(categoryIDs) > 0 {
		categories, err := s.store.MistakeCategories(r.Context())
		if err != nil {
			s.renderStoreError(w, r, err)
			return
		}
		allowed := make(map[int64]struct{}, len(categories))
		for _, category := range categories {
			allowed[category.ID] = struct{}{}
		}
		for _, categoryID := range categoryIDs {
			if _, ok := allowed[categoryID]; !ok {
				s.renderReviewFormError(w, r, matchID, "One of the selected mistake categories is unavailable.")
				return
			}
		}
	}
	manualTargetCheckins, err := manualTargetCheckinsFromForm(r, detail.ManualTargetCheckins)
	if err != nil {
		s.renderReviewFormError(w, r, matchID, err.Error())
		return
	}

	_, err = s.store.UpsertReview(r.Context(), model.UpsertReviewParams{
		MatchID:              matchID,
		PlayerID:             player.ID,
		GradeScale:           model.GradeNumeric,
		Grade:                grade,
		BiggestMistake:       biggestMistake,
		DoneWell:             doneWell,
		NextGame:             nextGame,
		Complete:             true,
		CategoryIDs:          categoryIDs,
		ManualTargetCheckins: manualTargetCheckins,
	})
	if err != nil {
		s.renderStoreError(w, r, err)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/matches/%d?flash=review-saved", matchID), http.StatusSeeOther)
}

func (s *Server) training(w http.ResponseWriter, r *http.Request) {
	player, err := s.store.PrimaryPlayer(r.Context())
	if err != nil {
		s.renderStoreError(w, r, err)
		return
	}
	activeBlock, err := s.store.ActiveTrainingBlock(r.Context(), player.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.renderStoreError(w, r, err)
		return
	}
	blocks, err := s.store.ListTrainingBlocks(r.Context(), player.ID)
	if err != nil {
		s.renderStoreError(w, r, err)
		return
	}

	data := pageData{
		Title:       "Training",
		CSRFToken:   csrfToken(r),
		Player:      player,
		ActiveBlock: activeBlock,
		Blocks:      blocks,
		Flash:       flashMessage(r),
	}
	s.render(w, http.StatusOK, "training", data)
}

func (s *Server) createTrainingBlock(w http.ResponseWriter, r *http.Request) {
	player, err := s.store.PrimaryPlayer(r.Context())
	if err != nil {
		s.renderStoreError(w, r, err)
		return
	}

	params, activate, err := s.trainingBlockParams(r, player.ID)
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, "Check the training block", err.Error())
		return
	}
	if activate {
		replaceActive := r.PostForm.Get("replace_active") == "true"
		if _, err := s.store.CreateAndActivateTrainingBlockAt(
			r.Context(),
			params,
			replaceActive,
			time.Now(),
			s.location,
		); err != nil {
			s.renderStoreError(w, r, err)
			return
		}
	} else {
		if _, err := s.store.CreateTrainingBlock(r.Context(), params); err != nil {
			s.renderStoreError(w, r, err)
			return
		}
	}

	http.Redirect(w, r, "/training?flash=block-created", http.StatusSeeOther)
}

func (s *Server) activateTrainingBlock(w http.ResponseWriter, r *http.Request) {
	blockID, err := positivePathID(r)
	if err != nil {
		s.renderError(w, r, http.StatusNotFound, "Training block not found", "That training block does not exist.")
		return
	}
	replaceActive := r.PostForm.Get("replace_active") == "true"
	if _, err := s.store.ActivateTrainingBlockAt(
		r.Context(),
		blockID,
		replaceActive,
		time.Now(),
		s.location,
	); err != nil {
		s.renderStoreError(w, r, err)
		return
	}
	http.Redirect(w, r, "/training?flash=block-activated", http.StatusSeeOther)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Ping(r.Context()); err != nil {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return
	}
	s.health(w, r)
}

func (s *Server) trainingBlockParams(r *http.Request, playerID int64) (model.CreateTrainingBlockParams, bool, error) {
	name := strings.TrimSpace(r.PostForm.Get("name"))
	description := strings.TrimSpace(r.PostForm.Get("description"))
	reminder := strings.TrimSpace(r.PostForm.Get("reminder"))
	if name == "" {
		return model.CreateTrainingBlockParams{}, false, errors.New("give the focus a short name")
	}
	if tooLong(name, 100) || tooLong(description, 800) || tooLong(reminder, 160) {
		return model.CreateTrainingBlockParams{}, false, errors.New("one of the focus fields is too long")
	}

	startDate := strings.TrimSpace(r.PostForm.Get("start_date"))
	if startDate == "" {
		startDate = time.Now().In(s.location).Format(time.DateOnly)
	}
	if _, err := time.Parse(time.DateOnly, startDate); err != nil {
		return model.CreateTrainingBlockParams{}, false, errors.New("start date must be a valid date")
	}

	var endDate *string
	if rawEndDate := strings.TrimSpace(r.PostForm.Get("end_date")); rawEndDate != "" {
		parsed, err := time.Parse(time.DateOnly, rawEndDate)
		if err != nil {
			return model.CreateTrainingBlockParams{}, false, errors.New("end date must be a valid date")
		}
		if rawEndDate < startDate {
			return model.CreateTrainingBlockParams{}, false, errors.New("end date cannot be before the start date")
		}
		normalized := parsed.Format(time.DateOnly)
		endDate = &normalized
	}

	target, err := trainingTargetFromForm(r)
	if err != nil {
		return model.CreateTrainingBlockParams{}, false, err
	}
	return model.CreateTrainingBlockParams{
		PlayerID:    playerID,
		Name:        name,
		Description: description,
		StartDate:   startDate,
		EndDate:     endDate,
		Reminder:    reminder,
		Targets:     []model.TrainingTargetInput{target},
	}, r.PostForm.Get("activate") == "true", nil
}

func trainingTargetFromForm(r *http.Request) (model.TrainingTargetInput, error) {
	label := strings.TrimSpace(r.PostForm.Get("target_label"))
	if label == "" {
		return model.TrainingTargetInput{
			Type:         model.TargetManual,
			Label:        "Focus adherence",
			ManualPrompt: "Did you follow the focus in the moments that mattered?",
			Comparator:   "at_least",
			Unit:         "review",
			Aggregation:  "per_game",
			WindowGames:  1,
		}, nil
	}

	keys := map[string]struct {
		key  string
		unit string
	}{
		"Deaths per game":         {key: "deaths", unit: "per game"},
		"Vision score per minute": {key: "vision_per_minute", unit: "per minute"},
		"Control wards per game":  {key: "control_wards", unit: "per game"},
	}
	metric, ok := keys[label]
	if !ok {
		return model.TrainingTargetInput{}, errors.New("choose a supported target metric")
	}
	comparator := r.PostForm.Get("target_comparator")
	if comparator != "<=" && comparator != ">=" {
		return model.TrainingTargetInput{}, errors.New("choose a supported target comparison")
	}
	threshold, err := strconv.ParseFloat(r.PostForm.Get("target_threshold"), 64)
	if err != nil || math.IsNaN(threshold) || math.IsInf(threshold, 0) || threshold < 0 {
		return model.TrainingTargetInput{}, errors.New("target value must be a non-negative number")
	}

	return model.TrainingTargetInput{
		Type:        model.TargetAutomatic,
		Label:       label,
		MetricKey:   metric.key,
		Comparator:  comparator,
		Threshold:   &threshold,
		Unit:        metric.unit,
		Aggregation: "per_game",
		WindowGames: 1,
	}, nil
}

func matchFilterFromRequest(r *http.Request, playerID int64) (model.MatchFilter, error) {
	filter := model.MatchFilter{
		PlayerID:   playerID,
		Champion:   strings.TrimSpace(r.URL.Query().Get("champion")),
		Role:       strings.TrimSpace(r.URL.Query().Get("role")),
		NotesQuery: strings.TrimSpace(r.URL.Query().Get("q")),
		Limit:      100,
	}
	if tooLong(filter.Champion, 100) || tooLong(filter.Role, 32) || tooLong(filter.NotesQuery, 200) {
		return model.MatchFilter{}, errors.New("one of the filters is too long")
	}
	switch queueScopeFromRequest(r) {
	case "", "both":
		filter.QueueIDs = model.TrainingQueueIDs()
	case "ranked":
		filter.QueueIDs = []int{model.QueueRankedSolo, model.QueueRankedFlex}
	case "draft":
		filter.QueueIDs = []int{model.QueueNormalDraft}
	case "solo":
		filter.QueueIDs = []int{model.QueueRankedSolo}
	case "flex":
		filter.QueueIDs = []int{model.QueueRankedFlex}
	default:
		return model.MatchFilter{}, errors.New("queue must be both, ranked, draft, solo, or flex")
	}
	switch r.URL.Query().Get("result") {
	case "":
	case "win":
		result := true
		filter.Result = &result
	case "loss":
		result := false
		filter.Result = &result
	default:
		return model.MatchFilter{}, errors.New("result must be win or loss")
	}
	return filter, nil
}

func queueScopeFromRequest(r *http.Request) string {
	return strings.ToLower(strings.TrimSpace(r.URL.Query().Get("queue")))
}

func positivePathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		return 0, errors.New("invalid ID")
	}
	return id, nil
}

func parsePositiveIDs(values []string) ([]int64, error) {
	seen := make(map[int64]struct{}, len(values))
	ids := make([]int64, 0, len(values))
	for _, value := range values {
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id < 1 {
			return nil, errors.New("invalid ID")
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, nil
}

func manualTargetCheckinsFromForm(
	r *http.Request,
	targets []model.ManualTargetCheckin,
) ([]model.ManualTargetCheckinInput, error) {
	checkins := make([]model.ManualTargetCheckinInput, 0, len(targets))
	for _, target := range targets {
		field := "manual_target_" + strconv.FormatInt(target.TargetID, 10)
		var value bool
		switch r.PostForm.Get(field) {
		case "yes":
			value = true
		case "no":
			value = false
		default:
			return nil, fmt.Errorf("Answer the focus check-in for “%s”.", target.Label)
		}
		checkins = append(checkins, model.ManualTargetCheckinInput{
			TargetID: target.TargetID,
			Value:    value,
		})
	}
	return checkins, nil
}

func (s *Server) renderReviewFormError(
	w http.ResponseWriter,
	r *http.Request,
	matchID int64,
	message string,
) {
	player, err := s.store.PrimaryPlayer(r.Context())
	if err != nil {
		s.renderStoreError(w, r, err)
		return
	}
	detail, err := s.store.GetMatch(r.Context(), matchID)
	if err != nil {
		s.renderStoreError(w, r, err)
		return
	}
	categories, err := s.store.MistakeCategories(r.Context())
	if err != nil {
		s.renderStoreError(w, r, err)
		return
	}

	selected := make(map[string]struct{}, len(r.PostForm["category_ids"]))
	for _, categoryID := range r.PostForm["category_ids"] {
		selected[categoryID] = struct{}{}
	}
	for index := range categories {
		_, categories[index].Selected = selected[strconv.FormatInt(categories[index].ID, 10)]
	}

	submittedTargets := make(map[int64]string, len(detail.ManualTargetCheckins))
	for _, target := range detail.ManualTargetCheckins {
		field := "manual_target_" + strconv.FormatInt(target.TargetID, 10)
		answer := r.PostForm.Get(field)
		if answer == "yes" || answer == "no" {
			submittedTargets[target.TargetID] = answer
		}
	}

	detail.StartedAt = detail.StartedAt.In(s.location)
	detail.EndedAt = detail.EndedAt.In(s.location)
	match := detail.Match
	s.render(w, http.StatusBadRequest, "match_detail", pageData{
		Title:     "Review " + match.Champion,
		CSRFToken: csrfToken(r),
		Player:    player,
		Match:     &match,
		Review: &reviewView{
			Grade:          strings.TrimSpace(r.PostForm.Get("grade")),
			BiggestMistake: r.PostForm.Get("biggest_mistake"),
			DoneWell:       r.PostForm.Get("done_well"),
			NextGame:       r.PostForm.Get("next_game"),
		},
		ManualTargets: newManualTargetViews(detail.ManualTargetCheckins, submittedTargets),
		Categories:    categories,
		FormError:     message,
	})
}

func (s *Server) localizeMatches(matches []model.Match) {
	for index := range matches {
		matches[index].StartedAt = matches[index].StartedAt.In(s.location)
		matches[index].EndedAt = matches[index].EndedAt.In(s.location)
	}
}

func tooLong(value string, maximum int) bool {
	return len([]rune(value)) > maximum
}

func flashMessage(r *http.Request) string {
	switch r.URL.Query().Get("flash") {
	case "review-saved":
		return "Review saved. Carry the next-game action forward."
	case "block-created":
		return "Training block created."
	case "block-activated":
		return "Training focus activated."
	case "sync-complete":
		return "Riot data is up to date."
	case "sync-partial":
		return "Riot sync finished. Some match details will be retried."
	default:
		return ""
	}
}

func (s *Server) renderStoreError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.renderError(w, r, http.StatusNotFound, "Not found", "That Journalol record does not exist.")
	case errors.Is(err, store.ErrActiveTrainingBlock):
		s.renderError(w, r, http.StatusConflict, "A focus is already active", "Confirm that you want to replace the current focus, then try again.")
	case errors.Is(err, store.ErrTrainingBlockNeedsTarget):
		s.renderError(w, r, http.StatusBadRequest, "The focus needs a target", "Add either a measurable target or a reflection check-in.")
	case errors.Is(err, store.ErrInvalidInput) &&
		strings.Contains(err.Error(), "before its start date"):
		s.renderError(w, r, http.StatusBadRequest, "That focus starts later", "Save it as planned, then activate it on or after its start date.")
	case errors.Is(err, store.ErrInvalidInput) &&
		strings.Contains(err.Error(), "expired training block"):
		s.renderError(w, r, http.StatusBadRequest, "That focus has already ended", "Adjust the dates in a new training block before making it active.")
	case errors.Is(err, store.ErrInvalidInput):
		s.renderError(w, r, http.StatusBadRequest, "Check the submitted details", "One or more values were invalid.")
	default:
		s.logger.ErrorContext(r.Context(), "request failed", "error", err)
		s.renderError(w, r, http.StatusInternalServerError, "Journalol hit a snag", "Your saved data was not changed. Try again.")
	}
}

func (s *Server) renderError(w http.ResponseWriter, r *http.Request, status int, title, message string) {
	player, err := s.store.PrimaryPlayer(r.Context())
	if err != nil {
		player = nil
	}
	s.render(w, status, "error", pageData{
		Title:     title,
		CSRFToken: csrfToken(r),
		Player:    player,
		Error:     message,
	})
}

func (s *Server) render(w http.ResponseWriter, status int, name string, data pageData) {
	var buffer bytes.Buffer
	if err := s.templates.ExecuteTemplate(&buffer, name, data); err != nil {
		s.logger.Error("render template", "template", name, "error", err)
		http.Error(w, "could not render page", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buffer.WriteTo(w)
}
