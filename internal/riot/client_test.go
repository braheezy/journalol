package riot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAccountByRiotIDAuthenticatesEscapesAndDecodes(t *testing.T) {
	t.Parallel()

	const apiKey = "RGAPI-test-key"
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Riot-Token"); got != apiKey {
			t.Errorf("X-Riot-Token = %q, want test key", got)
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q", got)
		}
		if got := r.RequestURI; got != "/riot/account/v1/accounts/by-riot-id/A%2FB%20Name/N%231" {
			t.Errorf("RequestURI = %q", got)
		}
		_, _ = io.WriteString(w, `{"puuid":"player-puuid","gameName":"A/B Name","tagLine":"N#1"}`)
	})

	client := testHandlerClient(t, handler, ClientOptions{APIKey: apiKey})
	account, err := client.AccountByRiotID(
		context.Background(),
		RouteAmericas,
		"A/B Name",
		"N#1",
	)
	if err != nil {
		t.Fatalf("AccountByRiotID: %v", err)
	}
	if account.PUUID != "player-puuid" || account.GameName != "A/B Name" || account.TagLine != "N#1" {
		t.Fatalf("account = %#v", account)
	}
}

func TestMatchIDsBuildsPageQuery(t *testing.T) {
	t.Parallel()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.RequestURI; got != "/lol/match/v5/matches/by-puuid/p%2Fu%3F/ids?count=2&start=4" {
			t.Errorf("RequestURI = %q", got)
		}
		_, _ = io.WriteString(w, `["NA1_2","NA1_1"]`)
	})

	client := testHandlerClient(t, handler, ClientOptions{APIKey: "key"})
	matchIDs, err := client.MatchIDs(context.Background(), RouteAmericas, "p/u?", 4, 2)
	if err != nil {
		t.Fatalf("MatchIDs: %v", err)
	}
	if len(matchIDs) != 2 || matchIDs[0] != "NA1_2" || matchIDs[1] != "NA1_1" {
		t.Fatalf("match IDs = %#v", matchIDs)
	}
}

func TestMatchIDsForQueueBuildsQueueQuery(t *testing.T) {
	t.Parallel()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.RequestURI; got != "/lol/match/v5/matches/by-puuid/puuid/ids?count=2&queue=420&start=4" {
			t.Errorf("RequestURI = %q", got)
		}
		_, _ = io.WriteString(w, `["NA1_2"]`)
	})

	client := testHandlerClient(t, handler, ClientOptions{APIKey: "key"})
	matchIDs, err := client.MatchIDsForQueue(
		context.Background(), RouteAmericas, "puuid", 4, 2, 420,
	)
	if err != nil {
		t.Fatalf("MatchIDsForQueue: %v", err)
	}
	if len(matchIDs) != 1 || matchIDs[0] != "NA1_2" {
		t.Fatalf("match IDs = %#v", matchIDs)
	}
}

func TestMatchIDsValidatesPagination(t *testing.T) {
	t.Parallel()

	client := testClient(t, "http://example.invalid", ClientOptions{APIKey: "key"})
	tests := []struct {
		name  string
		start int
		count int
	}{
		{name: "negative start", start: -1, count: 20},
		{name: "zero count", start: 0, count: 0},
		{name: "count too large", start: 0, count: 101},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := client.MatchIDs(
				context.Background(),
				RouteAmericas,
				"puuid",
				test.start,
				test.count,
			)
			if !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("error = %v, want ErrInvalidArgument", err)
			}
		})
	}
}

func TestMatchIDsRejectsUnboundedOrInvalidResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{name: "more IDs than requested", body: `["NA1_1","NA1_2"]`},
		{name: "empty ID", body: `[""]`},
		{name: "padded ID", body: `[" NA1_1"]`},
		{name: "control character", body: "[\"NA1_1\\n\"]"},
		{name: "oversized ID", body: `["` + strings.Repeat("x", 129) + `"]`},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, test.body)
			})
			client := testHandlerClient(t, handler, ClientOptions{
				APIKey:      "key",
				MaxAttempts: 1,
			})
			_, err := client.MatchIDs(
				context.Background(),
				RouteAmericas,
				"puuid",
				0,
				1,
			)
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.Kind != ErrorMalformedJSON {
				t.Fatalf("MatchIDs() error = %#v, want malformed response", err)
			}
		})
	}
}

func TestMatchDetailReturnsRawAndDecodedDTO(t *testing.T) {
	t.Parallel()

	const body = `{"metadata":{"dataVersion":"2","matchId":"NA1_123"},"info":{"gameVersion":"16.14.1","queueId":420,"participants":[{"puuid":"player","participantId":7,"teamId":100,"championId":99,"championName":"Lux","teamPosition":"UTILITY","kills":2,"deaths":3,"assists":14,"visionWardsBoughtInGame":4,"win":true}]}}`
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.EscapedPath(); got != "/lol/match/v5/matches/NA1_123" {
			t.Errorf("path = %q", got)
		}
		_, _ = io.WriteString(w, body)
	})

	client := testHandlerClient(t, handler, ClientOptions{APIKey: "key"})
	payload, err := client.MatchDetail(context.Background(), RouteAmericas, "NA1_123")
	if err != nil {
		t.Fatalf("MatchDetail: %v", err)
	}
	if string(payload.Raw) != body {
		t.Fatalf("raw response changed:\n got %q\nwant %q", payload.Raw, body)
	}
	if payload.Match.Metadata.MatchID != "NA1_123" || payload.Match.Info.QueueID != 420 {
		t.Fatalf("match = %#v", payload.Match)
	}
	participant := payload.Match.Info.Participants[0]
	if participant.PUUID != "player" || participant.TeamPosition != "UTILITY" ||
		participant.VisionWardsBoughtInGame != 4 {
		t.Fatalf("participant = %#v", participant)
	}
}

func TestTimelineReturnsRawAndDecodedDTO(t *testing.T) {
	t.Parallel()

	const body = `{"metadata":{"matchId":"NA1_123"},"info":{"frameInterval":60000,"participants":[{"participantId":7,"puuid":"player"}],"frames":[{"timestamp":60000,"events":[{"timestamp":45000,"type":"SKILL_LEVEL_UP","participantId":7,"skillSlot":2,"levelUpType":"NORMAL"},{"timestamp":50000,"type":"ELITE_MONSTER_KILL","killerId":7,"killerTeamId":100,"assistingParticipantIds":[2,3],"monsterType":"DRAGON"}]}]}}`
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.EscapedPath(); got != "/lol/match/v5/matches/NA1_123/timeline" {
			t.Errorf("path = %q", got)
		}
		_, _ = io.WriteString(w, body)
	})

	client := testHandlerClient(t, handler, ClientOptions{APIKey: "key"})
	payload, err := client.Timeline(context.Background(), RouteAmericas, "NA1_123")
	if err != nil {
		t.Fatalf("Timeline: %v", err)
	}
	if string(payload.Raw) != body {
		t.Fatalf("raw response changed")
	}
	if payload.Timeline.Metadata.MatchID != "NA1_123" ||
		payload.Timeline.Info.Frames[0].Events[0].SkillSlot != 2 ||
		payload.Timeline.Info.Frames[0].Events[1].MonsterType != "DRAGON" ||
		payload.Timeline.Info.Frames[0].Events[1].KillerTeamID != 100 {
		t.Fatalf("timeline = %#v", payload.Timeline)
	}
}

func TestRateLimitRetriesAfterHeader(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "untrusted upstream response", http.StatusTooManyRequests)
			return
		}
		_, _ = io.WriteString(w, `["NA1_1"]`)
	})

	client := testHandlerClient(t, handler, ClientOptions{
		APIKey:         "key",
		MaxAttempts:    2,
		RetryBaseDelay: time.Millisecond,
		MaxRetryWait:   10 * time.Millisecond,
	})
	matchIDs, err := client.MatchIDs(context.Background(), RouteAmericas, "puuid", 0, 1)
	if err != nil {
		t.Fatalf("MatchIDs: %v", err)
	}
	if attempts.Load() != 2 || len(matchIDs) != 1 {
		t.Fatalf("attempts = %d, match IDs = %#v", attempts.Load(), matchIDs)
	}
}

func TestLongRateLimitIsReturnedForDurableScheduling(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("Retry-After", "120")
		http.Error(w, "do not expose this body", http.StatusTooManyRequests)
	})

	client := testHandlerClient(t, handler, ClientOptions{
		APIKey:         "key",
		MaxAttempts:    3,
		RetryBaseDelay: time.Millisecond,
		MaxRetryWait:   5 * time.Millisecond,
	})
	_, err := client.MatchIDs(context.Background(), RouteAmericas, "puuid", 0, 1)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want *APIError", err)
	}
	if apiErr.Kind != ErrorRateLimited || apiErr.StatusCode != http.StatusTooManyRequests ||
		apiErr.RetryAfter != 2*time.Minute || !apiErr.Retryable {
		t.Fatalf("API error = %#v", apiErr)
	}
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d, want 1", attempts.Load())
	}
}

func TestServerErrorIsRetried(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			http.Error(w, "temporary", http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, `{"puuid":"p","gameName":"g","tagLine":"t"}`)
	})

	client := testHandlerClient(t, handler, ClientOptions{
		APIKey:         "key",
		MaxAttempts:    2,
		RetryBaseDelay: time.Millisecond,
		MaxRetryWait:   10 * time.Millisecond,
	})
	if _, err := client.AccountByRiotID(context.Background(), RouteAmericas, "g", "t"); err != nil {
		t.Fatalf("AccountByRiotID: %v", err)
	}
	if attempts.Load() != 2 {
		t.Fatalf("attempts = %d, want 2", attempts.Load())
	}
}

func TestAPIErrorDoesNotExposeKeyOrResponseBody(t *testing.T) {
	t.Parallel()

	const (
		apiKey      = "RGAPI-key-must-stay-secret"
		responseKey = "body-must-stay-secret"
	)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, responseKey+" "+apiKey, http.StatusForbidden)
	})

	client := testHandlerClient(t, handler, ClientOptions{APIKey: apiKey, MaxAttempts: 1})
	_, err := client.AccountByRiotID(context.Background(), RouteAmericas, "game", "tag")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want *APIError", err)
	}
	if apiErr.Kind != ErrorForbidden || apiErr.StatusCode != http.StatusForbidden ||
		apiErr.Retryable {
		t.Fatalf("API error = %#v", apiErr)
	}
	errorText := err.Error()
	if strings.Contains(errorText, apiKey) || strings.Contains(errorText, responseKey) {
		t.Fatalf("sanitized error leaked secret: %q", errorText)
	}
}

func TestSuccessfulResponseSizeIsBounded(t *testing.T) {
	t.Parallel()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `["too large"]`)
	})

	client := testHandlerClient(t, handler, ClientOptions{
		APIKey:           "key",
		MaxAttempts:      1,
		MaxResponseBytes: 4,
	})
	_, err := client.MatchIDs(context.Background(), RouteAmericas, "puuid", 0, 1)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Kind != ErrorResponseTooLarge {
		t.Fatalf("error = %#v, want response-too-large APIError", err)
	}
}

func TestMalformedJSONIsSanitized(t *testing.T) {
	t.Parallel()

	const body = `{"secret":"do not print",`
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, body)
	})

	client := testHandlerClient(t, handler, ClientOptions{APIKey: "key", MaxAttempts: 1})
	_, err := client.MatchDetail(context.Background(), RouteAmericas, "NA1_1")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Kind != ErrorMalformedJSON {
		t.Fatalf("error = %#v, want malformed-JSON APIError", err)
	}
	if strings.Contains(err.Error(), "do not print") {
		t.Fatalf("error leaked response body: %q", err)
	}
}

func TestRetryWaitIsCancelable(t *testing.T) {
	t.Parallel()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "limited", http.StatusTooManyRequests)
	})

	client := testHandlerClient(t, handler, ClientOptions{
		APIKey:         "key",
		MaxAttempts:    2,
		RetryBaseDelay: time.Millisecond,
		MaxRetryWait:   2 * time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(10*time.Millisecond, cancel)

	started := time.Now()
	_, err := client.MatchIDs(ctx, RouteAmericas, "puuid", 0, 1)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context cancellation", err)
	}
	if time.Since(started) > 500*time.Millisecond {
		t.Fatalf("canceled retry took too long: %s", time.Since(started))
	}
}

func TestNetworkErrorIsRetried(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if attempts.Add(1) == 1 {
			return nil, errors.New("temporary network failure")
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`["NA1_1"]`)),
			Request:    request,
		}, nil
	})
	client, err := NewClient(ClientOptions{
		APIKey:         "key",
		HTTPClient:     &http.Client{Transport: transport},
		BaseURLs:       map[RegionalRoute]string{RouteAmericas: "http://riot.test"},
		MaxAttempts:    2,
		RetryBaseDelay: time.Millisecond,
		MaxRetryWait:   10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	matchIDs, err := client.MatchIDs(context.Background(), RouteAmericas, "puuid", 0, 1)
	if err != nil {
		t.Fatalf("MatchIDs: %v", err)
	}
	if attempts.Load() != 2 || len(matchIDs) != 1 {
		t.Fatalf("attempts = %d, matches = %#v", attempts.Load(), matchIDs)
	}
}

func TestClientTimeoutIsInjectableAndSanitized(t *testing.T) {
	t.Parallel()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		_, _ = io.WriteString(w, `[]`)
	})

	client := testHandlerClient(t, handler, ClientOptions{
		APIKey:      "key",
		Timeout:     5 * time.Millisecond,
		MaxAttempts: 1,
	})
	_, err := client.MatchIDs(context.Background(), RouteAmericas, "private-puuid", 0, 1)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Kind != ErrorNetwork || !apiErr.Retryable {
		t.Fatalf("error = %#v, want retryable network APIError", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline matching", err)
	}
	if strings.Contains(err.Error(), "private-puuid") {
		t.Fatalf("error exposed request path: %q", err)
	}
}

func TestNewClientRejectsUnsafeBaseURL(t *testing.T) {
	t.Parallel()

	_, err := NewClient(ClientOptions{
		APIKey:   "key",
		BaseURLs: map[RegionalRoute]string{RouteAmericas: "https://user:pass@example.com"},
	})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("error = %v, want ErrInvalidArgument", err)
	}
}

func testClient(t *testing.T, baseURL string, options ClientOptions) *Client {
	t.Helper()
	options.BaseURLs = map[RegionalRoute]string{RouteAmericas: baseURL}
	client, err := NewClient(options)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

func testHandlerClient(t *testing.T, handler http.Handler, options ClientOptions) *Client {
	t.Helper()
	options.HTTPClient = &http.Client{Transport: handlerRoundTripper{handler: handler}}
	return testClient(t, "http://riot.test", options)
}

type handlerRoundTripper struct {
	handler http.Handler
}

func (transport handlerRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	type result struct {
		response *http.Response
	}
	resultCh := make(chan result, 1)
	go func() {
		serverRequest := request.Clone(request.Context())
		serverRequest.RequestURI = request.URL.RequestURI()
		recorder := httptest.NewRecorder()
		transport.handler.ServeHTTP(recorder, serverRequest)
		response := recorder.Result()
		response.Request = request
		resultCh <- result{response: response}
	}()

	select {
	case <-request.Context().Done():
		return nil, request.Context().Err()
	case got := <-resultCh:
		return got.response, nil
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := fn(request)
	if err != nil {
		return nil, fmt.Errorf("round trip: %w", err)
	}
	return response, nil
}
