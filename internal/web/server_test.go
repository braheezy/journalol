package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"journalol/internal/model"
	"journalol/internal/store"
)

func TestDemoPagesRender(t *testing.T) {
	handler, dataStore := newDemoHandler(t)
	player, err := dataStore.PrimaryPlayer(context.Background())
	if err != nil {
		t.Fatalf("load primary player: %v", err)
	}
	matches, err := dataStore.RecentMatches(context.Background(), player.ID, 1)
	if err != nil {
		t.Fatalf("load recent match: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("demo seed did not create a match")
	}

	tests := []struct {
		path   string
		marker string
	}{
		{path: "/", marker: "Today’s practice"},
		{path: "/matches", marker: "Match history"},
		{path: "/matches?queue=ranked", marker: "Ranked Solo/Duo"},
		{path: "/training", marker: "Training blocks"},
		{path: fmt.Sprintf("/matches/%d", matches[0].ID), marker: "One-minute reflection"},
		{path: "/static/app.css", marker: "--paper"},
	}

	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			response := performRequest(handler, http.MethodGet, test.path, nil, nil)
			if response.Code != http.StatusOK {
				t.Fatalf("GET %s status = %d, want %d; body: %s",
					test.path, response.Code, http.StatusOK, response.Body.String())
			}
			if !strings.Contains(response.Body.String(), test.marker) {
				t.Fatalf("GET %s did not contain %q", test.path, test.marker)
			}
		})
	}
}

func TestReviewSubmissionRoundTrip(t *testing.T) {
	handler, dataStore := newDemoHandler(t)
	player, err := dataStore.PrimaryPlayer(context.Background())
	if err != nil {
		t.Fatalf("load primary player: %v", err)
	}
	matches, err := dataStore.RecentMatches(context.Background(), player.ID, 100)
	if err != nil {
		t.Fatalf("load recent matches: %v", err)
	}

	var matchID int64
	for _, match := range matches {
		if !match.ReviewComplete {
			matchID = match.ID
			break
		}
	}
	if matchID == 0 {
		t.Fatal("demo seed did not create an unreviewed match")
	}

	detailPath := fmt.Sprintf("/matches/%d", matchID)
	getResponse := performRequest(handler, http.MethodGet, detailPath, nil, nil)
	csrfCookie := responseCookie(t, getResponse, csrfCookieName)

	categories, err := dataStore.MistakeCategories(context.Background())
	if err != nil {
		t.Fatalf("load mistake categories: %v", err)
	}
	if len(categories) == 0 {
		t.Fatal("demo seed did not create mistake categories")
	}
	matchDetail, err := dataStore.GetMatch(context.Background(), matchID)
	if err != nil {
		t.Fatalf("load match detail: %v", err)
	}

	form := url.Values{
		"_csrf":           {csrfCookie.Value},
		"grade":           {"4"},
		"biggest_mistake": {"Entered river without enough information."},
		"done_well":       {"Tracked the opposing support before the objective."},
		"next_game":       {"I will check the minimap before crossing river."},
		"category_ids":    {strconv.FormatInt(categories[0].ID, 10)},
	}
	for _, target := range matchDetail.ManualTargetCheckins {
		form.Set("manual_target_"+strconv.FormatInt(target.TargetID, 10), "yes")
	}
	postResponse := performRequest(
		handler,
		http.MethodPost,
		detailPath+"/review",
		form,
		csrfCookie,
	)
	if postResponse.Code != http.StatusSeeOther {
		t.Fatalf("save review status = %d, want %d; body: %s",
			postResponse.Code, http.StatusSeeOther, postResponse.Body.String())
	}
	if location := postResponse.Header().Get("Location"); location != detailPath+"?flash=review-saved" {
		t.Fatalf("save review location = %q", location)
	}

	detail, err := dataStore.GetMatch(context.Background(), matchID)
	if err != nil {
		t.Fatalf("reload reviewed match: %v", err)
	}
	if detail.Review == nil || !detail.Review.Complete || detail.Review.Grade != "4" {
		t.Fatalf("saved review = %#v", detail.Review)
	}
	if len(detail.SelectedCategoryIDs) != 1 || detail.SelectedCategoryIDs[0] != categories[0].ID {
		t.Fatalf("saved categories = %v", detail.SelectedCategoryIDs)
	}
	for _, target := range detail.ManualTargetCheckins {
		if target.Value == nil || !*target.Value {
			t.Fatalf("saved manual target = %#v", target)
		}
	}
}

func TestReviewValidationRerendersSubmittedDraft(t *testing.T) {
	handler, dataStore := newDemoHandler(t)
	player, err := dataStore.PrimaryPlayer(context.Background())
	if err != nil {
		t.Fatalf("load primary player: %v", err)
	}
	matches, err := dataStore.RecentMatches(context.Background(), player.ID, 100)
	if err != nil {
		t.Fatalf("load recent matches: %v", err)
	}

	var matchID int64
	for _, match := range matches {
		if !match.ReviewComplete {
			matchID = match.ID
			break
		}
	}
	if matchID == 0 {
		t.Fatal("demo seed did not create an unreviewed match")
	}

	path := fmt.Sprintf("/matches/%d", matchID)
	getResponse := performRequest(handler, http.MethodGet, path, nil, nil)
	csrfCookie := responseCookie(t, getResponse, csrfCookieName)
	detail, err := dataStore.GetMatch(context.Background(), matchID)
	if err != nil {
		t.Fatalf("load match detail: %v", err)
	}

	form := url.Values{
		"_csrf":           {csrfCookie.Value},
		"grade":           {"9"},
		"biggest_mistake": {"Keep this submitted note."},
	}
	for _, target := range detail.ManualTargetCheckins {
		form.Set("manual_target_"+strconv.FormatInt(target.TargetID, 10), "no")
	}

	response := performRequest(handler, http.MethodPost, path+"/review", form, csrfCookie)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid review status = %d, want %d", response.Code, http.StatusBadRequest)
	}
	body := response.Body.String()
	if !strings.Contains(body, "Keep this submitted note.") ||
		!strings.Contains(body, "Choose a focus grade from 1 to 5.") {
		t.Fatalf("invalid review did not preserve the draft or show the error: %s", body)
	}
}

func TestRemakeReviewIsRejected(t *testing.T) {
	handler, dataStore := newDemoHandler(t)
	player, err := dataStore.PrimaryPlayer(context.Background())
	if err != nil {
		t.Fatalf("load primary player: %v", err)
	}
	matches, err := dataStore.RecentMatches(context.Background(), player.ID, 100)
	if err != nil {
		t.Fatalf("load recent matches: %v", err)
	}

	var remakeID int64
	for _, match := range matches {
		if match.IsRemake {
			remakeID = match.ID
			break
		}
	}
	if remakeID == 0 {
		t.Fatal("demo seed did not create a remake")
	}

	path := fmt.Sprintf("/matches/%d", remakeID)
	getResponse := performRequest(handler, http.MethodGet, path, nil, nil)
	if !strings.Contains(getResponse.Body.String(), "No review needed") {
		t.Fatalf("remake detail did not explain review exclusion: %s", getResponse.Body.String())
	}
	csrfCookie := responseCookie(t, getResponse, csrfCookieName)
	form := url.Values{
		"_csrf":           {csrfCookie.Value},
		"grade":           {"4"},
		"biggest_mistake": {"This should not be saved."},
	}
	response := performRequest(handler, http.MethodPost, path+"/review", form, csrfCookie)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("remake review status = %d, want %d", response.Code, http.StatusBadRequest)
	}
	if !strings.Contains(response.Body.String(), "Remakes do not need a review") {
		t.Fatalf("remake rejection did not explain why: %s", response.Body.String())
	}
}

func TestCreateAndActivateTrainingBlock(t *testing.T) {
	handler, dataStore := newDemoHandler(t)
	getResponse := performRequest(handler, http.MethodGet, "/training", nil, nil)
	csrfCookie := responseCookie(t, getResponse, csrfCookieName)

	form := url.Values{
		"_csrf":          {csrfCookie.Value},
		"name":           {"Track the jungler"},
		"description":    {"Name the likely jungle quadrant before entering river."},
		"reminder":       {"Check camps, lanes, then move."},
		"start_date":     {"2026-07-26"},
		"activate":       {"true"},
		"replace_active": {"true"},
	}
	postResponse := performRequest(
		handler,
		http.MethodPost,
		"/training",
		form,
		csrfCookie,
	)
	if postResponse.Code != http.StatusSeeOther {
		t.Fatalf("create training block status = %d, want %d; body: %s",
			postResponse.Code, http.StatusSeeOther, postResponse.Body.String())
	}

	player, err := dataStore.PrimaryPlayer(context.Background())
	if err != nil {
		t.Fatalf("load primary player: %v", err)
	}
	active, err := dataStore.ActiveTrainingBlock(context.Background(), player.ID)
	if err != nil {
		t.Fatalf("load active training block: %v", err)
	}
	if active == nil || active.Name != "Track the jungler" {
		t.Fatalf("active block = %#v", active)
	}
	if len(active.Targets) != 1 || active.Targets[0].Threshold != nil {
		t.Fatalf("manual target = %#v", active.Targets)
	}
}

func TestFutureTrainingBlockCannotActivateOrLeaveResidue(t *testing.T) {
	handler, dataStore := newDemoHandler(t)
	getResponse := performRequest(handler, http.MethodGet, "/training", nil, nil)
	csrfCookie := responseCookie(t, getResponse, csrfCookieName)

	form := url.Values{
		"_csrf":          {csrfCookie.Value},
		"name":           {"A future focus"},
		"start_date":     {"2099-01-01"},
		"activate":       {"true"},
		"replace_active": {"true"},
	}
	response := performRequest(handler, http.MethodPost, "/training", form, csrfCookie)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("future activation status = %d, want %d; body: %s",
			response.Code, http.StatusBadRequest, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "That focus starts later") {
		t.Fatalf("future activation did not explain the date: %s", response.Body.String())
	}

	player, err := dataStore.PrimaryPlayer(context.Background())
	if err != nil {
		t.Fatalf("load primary player: %v", err)
	}
	blocks, err := dataStore.ListTrainingBlocks(context.Background(), player.ID)
	if err != nil {
		t.Fatalf("list training blocks: %v", err)
	}
	for _, block := range blocks {
		if block.Name == "A future focus" {
			t.Fatalf("failed atomic activation left block %#v", block)
		}
	}
}

func TestManualRiotSync(t *testing.T) {
	syncService := &stubSyncer{run: &store.SyncRun{}}
	handler, _ := newRealHandler(t, syncService)

	getResponse := performRequest(handler, http.MethodGet, "/", nil, nil)
	if getResponse.Code != http.StatusOK {
		t.Fatalf("dashboard status = %d, want %d; body: %s",
			getResponse.Code, http.StatusOK, getResponse.Body.String())
	}
	if !strings.Contains(getResponse.Body.String(), "Sync now") {
		t.Fatalf("configured dashboard did not show manual sync: %s", getResponse.Body.String())
	}
	csrfCookie := responseCookie(t, getResponse, csrfCookieName)

	response := performRequest(
		handler,
		http.MethodPost,
		"/sync",
		url.Values{"_csrf": {csrfCookie.Value}},
		csrfCookie,
	)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("sync status = %d, want %d; body: %s",
			response.Code, http.StatusSeeOther, response.Body.String())
	}
	if location := response.Header().Get("Location"); location != "/?flash=sync-complete" {
		t.Fatalf("sync redirect location = %q", location)
	}
	if len(syncService.triggers) != 1 || syncService.triggers[0] != "manual" {
		t.Fatalf("sync triggers = %v, want [manual]", syncService.triggers)
	}
}

func TestManualRiotSyncFailureIsSanitized(t *testing.T) {
	const sensitiveDetail = "RGAPI-never-render-this upstream response"
	syncService := &stubSyncer{err: errors.New(sensitiveDetail)}
	handler, _ := newRealHandler(t, syncService)

	getResponse := performRequest(handler, http.MethodGet, "/", nil, nil)
	csrfCookie := responseCookie(t, getResponse, csrfCookieName)
	response := performRequest(
		handler,
		http.MethodPost,
		"/sync",
		url.Values{"_csrf": {csrfCookie.Value}},
		csrfCookie,
	)

	if response.Code != http.StatusBadGateway {
		t.Fatalf("failed sync status = %d, want %d; body: %s",
			response.Code, http.StatusBadGateway, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, "Riot sync could not finish") {
		t.Fatalf("failed sync did not render useful generic error: %s", body)
	}
	if strings.Contains(body, sensitiveDetail) {
		t.Fatalf("failed sync leaked upstream detail: %s", body)
	}
}

func TestManualPartialRiotSyncUsesHonestFlash(t *testing.T) {
	syncService := &stubSyncer{run: &store.SyncRun{State: store.SyncStatePartial}}
	handler, _ := newRealHandler(t, syncService)
	getResponse := performRequest(handler, http.MethodGet, "/", nil, nil)
	csrfCookie := responseCookie(t, getResponse, csrfCookieName)

	response := performRequest(
		handler,
		http.MethodPost,
		"/sync",
		url.Values{"_csrf": {csrfCookie.Value}},
		csrfCookie,
	)
	if response.Code != http.StatusSeeOther ||
		response.Header().Get("Location") != "/?flash=sync-partial" {
		t.Fatalf("partial sync response = %d %q, want 303 partial flash",
			response.Code, response.Header().Get("Location"))
	}
}

func TestSyncControlsStayHiddenWithoutRealIntegration(t *testing.T) {
	demoHandler, _ := newDemoHandler(t)
	demoResponse := performRequest(demoHandler, http.MethodGet, "/", nil, nil)
	if strings.Contains(demoResponse.Body.String(), "Match sync") ||
		strings.Contains(demoResponse.Body.String(), "Sync now") {
		t.Fatalf("demo dashboard unexpectedly showed Riot sync controls: %s", demoResponse.Body.String())
	}

	realHandler, _ := newRealHandler(t, nil)
	realResponse := performRequest(realHandler, http.MethodGet, "/", nil, nil)
	if strings.Contains(realResponse.Body.String(), "Sync now") {
		t.Fatalf("dashboard without a sync service showed manual control: %s", realResponse.Body.String())
	}
	if !strings.Contains(realResponse.Body.String(), "Match sync") {
		t.Fatalf("real dashboard did not show sync status: %s", realResponse.Body.String())
	}
}

func TestDashboardShowsLatestSyncWithoutStoredErrorDetail(t *testing.T) {
	handler, dataStore := newRealHandler(t, &stubSyncer{run: &store.SyncRun{}})
	ctx := context.Background()
	player, err := dataStore.PrimaryPlayer(ctx)
	if err != nil {
		t.Fatalf("load primary player: %v", err)
	}
	run, err := dataStore.StartSyncRun(ctx, store.SyncRunStart{
		PlayerID:  player.ID,
		Trigger:   store.SyncTriggerPoll,
		StartedAt: time.Date(2026, time.July, 28, 15, 59, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("start sync run: %v", err)
	}
	const storedDetail = "internal-provider-detail-must-not-render"
	if err := dataStore.FinishSyncRun(ctx, run.ID, store.SyncRunFinish{
		State:           store.SyncStateFailed,
		DiscoveredCount: 6,
		ImportedCount:   2,
		SkippedCount:    3,
		FailedCount:     1,
		ErrorCode:       "upstream_failure",
		ErrorMessage:    storedDetail,
		CompletedAt:     time.Date(2026, time.July, 28, 16, 5, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("finish sync run: %v", err)
	}

	response := performRequest(handler, http.MethodGet, "/", nil, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("dashboard status = %d, want %d; body: %s",
			response.Code, http.StatusOK, response.Body.String())
	}
	body := response.Body.String()
	for _, marker := range []string{
		"Needs attention",
		"Jul 28, 4:05 PM",
		"<dd>6</dd>",
		"<dd>2</dd>",
		"<dd>3</dd>",
		"<dd>1</dd>",
	} {
		if !strings.Contains(body, marker) {
			t.Fatalf("dashboard did not contain sync marker %q: %s", marker, body)
		}
	}
	if strings.Contains(body, storedDetail) {
		t.Fatalf("dashboard leaked stored sync error detail: %s", body)
	}
}

func newDemoHandler(t *testing.T) (http.Handler, *store.Store) {
	t.Helper()

	dataStore, err := store.Open(filepath.Join(t.TempDir(), "journalol.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := dataStore.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	if err := dataStore.SeedDemo(context.Background()); err != nil {
		t.Fatalf("seed demo data: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server, err := NewServer(
		dataStore,
		time.UTC,
		map[string]struct{}{"localhost": {}},
		logger,
	)
	if err != nil {
		t.Fatalf("create server: %v", err)
	}
	return server.Handler(), dataStore
}

func newRealHandler(t *testing.T, syncService Syncer) (http.Handler, *store.Store) {
	t.Helper()

	dataStore, err := store.Open(filepath.Join(t.TempDir(), "journalol.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := dataStore.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	if _, err := dataStore.SavePrimaryPlayer(context.Background(), model.PlayerProfile{
		GameName:      "Practice Player",
		TagLine:       "NA1",
		PlatformRoute: "NA1",
		RegionalRoute: "AMERICAS",
		PUUID:         "test-real-puuid",
	}); err != nil {
		t.Fatalf("save real player: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	options := []Option{}
	if syncService != nil {
		options = append(options, WithSyncer(syncService))
	}
	server, err := NewServer(
		dataStore,
		time.UTC,
		map[string]struct{}{"localhost": {}},
		logger,
		options...,
	)
	if err != nil {
		t.Fatalf("create server: %v", err)
	}
	return server.Handler(), dataStore
}

type stubSyncer struct {
	run      *store.SyncRun
	err      error
	triggers []string
}

func (s *stubSyncer) Sync(_ context.Context, trigger string) (*store.SyncRun, error) {
	s.triggers = append(s.triggers, trigger)
	return s.run, s.err
}

func performRequest(
	handler http.Handler,
	method string,
	path string,
	form url.Values,
	cookie *http.Cookie,
) *httptest.ResponseRecorder {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	request := httptest.NewRequest(method, path, body)
	request.Host = "localhost"
	if form != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func responseCookie(t *testing.T, response *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	t.Fatalf("response did not set %s cookie", name)
	return nil
}
