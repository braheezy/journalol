// Package riot is Journalol's server-side boundary for Riot APIs. It owns
// authentication, regional routing, bounded retries, response limits, DTOs,
// and sanitized failure reporting.
package riot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	defaultHTTPTimeout     = 10 * time.Second
	defaultMaxResponseSize = int64(32 << 20)
	defaultMaxAttempts     = 3
	defaultRetryBaseDelay  = 200 * time.Millisecond
	defaultMaxRetryWait    = 5 * time.Second
	discardBodyLimit       = int64(64 << 10)
	defaultUserAgent       = "Journalol/1.0"
)

// ClientOptions configures a Riot API client. BaseURLs exists for deterministic
// fake-server tests; production callers should leave it unset.
type ClientOptions struct {
	APIKey           string
	HTTPClient       *http.Client
	Timeout          time.Duration
	BaseURLs         map[RegionalRoute]string
	MaxResponseBytes int64
	MaxAttempts      int
	RetryBaseDelay   time.Duration
	MaxRetryWait     time.Duration
	UserAgent        string
}

// Client performs authenticated Riot API requests.
type Client struct {
	apiKey           string
	httpClient       *http.Client
	baseURLs         map[RegionalRoute]string
	maxResponseBytes int64
	maxAttempts      int
	retryBaseDelay   time.Duration
	maxRetryWait     time.Duration
	userAgent        string
}

// NewClient constructs a Riot API client without making a network request.
func NewClient(options ClientOptions) (*Client, error) {
	apiKey := strings.TrimSpace(options.APIKey)
	if apiKey == "" {
		return nil, fmt.Errorf("%w: API key is required", ErrInvalidArgument)
	}
	if options.Timeout < 0 {
		return nil, fmt.Errorf("%w: timeout cannot be negative", ErrInvalidArgument)
	}
	if options.MaxResponseBytes < 0 {
		return nil, fmt.Errorf("%w: response limit cannot be negative", ErrInvalidArgument)
	}
	if options.MaxAttempts < 0 {
		return nil, fmt.Errorf("%w: maximum attempts cannot be negative", ErrInvalidArgument)
	}
	if options.RetryBaseDelay < 0 || options.MaxRetryWait < 0 {
		return nil, fmt.Errorf("%w: retry durations cannot be negative", ErrInvalidArgument)
	}

	httpClient := options.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultHTTPTimeout}
	} else {
		copy := *httpClient
		httpClient = &copy
	}
	if options.Timeout > 0 {
		httpClient.Timeout = options.Timeout
	}
	// A redirect must not forward X-Riot-Token to an unexpected endpoint.
	httpClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}

	baseURLs := make(map[RegionalRoute]string, len(regionalBaseURLs))
	for route, baseURL := range regionalBaseURLs {
		baseURLs[route] = baseURL
	}
	for route, baseURL := range options.BaseURLs {
		if _, ok := regionalBaseURLs[route]; !ok {
			return nil, fmt.Errorf("%w: unsupported base URL route", ErrInvalidArgument)
		}
		normalized, err := normalizeBaseURL(baseURL)
		if err != nil {
			return nil, err
		}
		baseURLs[route] = normalized
	}

	maxResponseBytes := options.MaxResponseBytes
	if maxResponseBytes == 0 {
		maxResponseBytes = defaultMaxResponseSize
	}
	maxAttempts := options.MaxAttempts
	if maxAttempts == 0 {
		maxAttempts = defaultMaxAttempts
	}
	retryBaseDelay := options.RetryBaseDelay
	if retryBaseDelay == 0 {
		retryBaseDelay = defaultRetryBaseDelay
	}
	maxRetryWait := options.MaxRetryWait
	if maxRetryWait == 0 {
		maxRetryWait = defaultMaxRetryWait
	}
	userAgent := strings.TrimSpace(options.UserAgent)
	if userAgent == "" {
		userAgent = defaultUserAgent
	}

	return &Client{
		apiKey:           apiKey,
		httpClient:       httpClient,
		baseURLs:         baseURLs,
		maxResponseBytes: maxResponseBytes,
		maxAttempts:      maxAttempts,
		retryBaseDelay:   retryBaseDelay,
		maxRetryWait:     maxRetryWait,
		userAgent:        userAgent,
	}, nil
}

// AccountByRiotID resolves a game name and tag line through ACCOUNT-V1.
func (c *Client) AccountByRiotID(
	ctx context.Context,
	route RegionalRoute,
	gameName string,
	tagLine string,
) (Account, error) {
	if strings.TrimSpace(gameName) == "" {
		return Account{}, fmt.Errorf("%w: game name is required", ErrInvalidArgument)
	}
	if strings.TrimSpace(tagLine) == "" {
		return Account{}, fmt.Errorf("%w: tag line is required", ErrInvalidArgument)
	}

	path := "/riot/account/v1/accounts/by-riot-id/" +
		url.PathEscape(gameName) + "/" + url.PathEscape(tagLine)
	raw, err := c.get(ctx, "account lookup", route, path, nil)
	if err != nil {
		return Account{}, err
	}

	var account Account
	if err := json.Unmarshal(raw, &account); err != nil {
		return Account{}, malformedJSONError("account lookup")
	}
	return account, nil
}

// MatchIDs returns a page of MATCH-V5 IDs for a PUUID.
func (c *Client) MatchIDs(
	ctx context.Context,
	route RegionalRoute,
	puuid string,
	start int,
	count int,
) ([]string, error) {
	return c.matchIDs(ctx, route, puuid, start, count, 0)
}

// MatchIDsForQueue returns a page of MATCH-V5 IDs restricted to one Riot
// queue. MATCH-V5 accepts one queue value per request, so callers that need a
// combined archive should merge the returned pages locally.
func (c *Client) MatchIDsForQueue(
	ctx context.Context,
	route RegionalRoute,
	puuid string,
	start int,
	count int,
	queueID int,
) ([]string, error) {
	if queueID < 1 {
		return nil, fmt.Errorf("%w: queue ID must be positive", ErrInvalidArgument)
	}
	return c.matchIDs(ctx, route, puuid, start, count, queueID)
}

func (c *Client) matchIDs(
	ctx context.Context,
	route RegionalRoute,
	puuid string,
	start int,
	count int,
	queueID int,
) ([]string, error) {
	if strings.TrimSpace(puuid) == "" {
		return nil, fmt.Errorf("%w: PUUID is required", ErrInvalidArgument)
	}
	if start < 0 {
		return nil, fmt.Errorf("%w: match start cannot be negative", ErrInvalidArgument)
	}
	if count < 1 || count > 100 {
		return nil, fmt.Errorf("%w: match count must be between 1 and 100", ErrInvalidArgument)
	}

	path := "/lol/match/v5/matches/by-puuid/" + url.PathEscape(puuid) + "/ids"
	query := url.Values{
		"start": {strconv.Itoa(start)},
		"count": {strconv.Itoa(count)},
	}
	if queueID > 0 {
		query.Set("queue", strconv.Itoa(queueID))
	}
	raw, err := c.get(ctx, "match discovery", route, path, query)
	if err != nil {
		return nil, err
	}

	var matchIDs []string
	if err := json.Unmarshal(raw, &matchIDs); err != nil {
		return nil, malformedJSONError("match discovery")
	}
	if len(matchIDs) > count {
		return nil, malformedJSONError("match discovery")
	}
	for _, matchID := range matchIDs {
		if strings.TrimSpace(matchID) == "" ||
			strings.TrimSpace(matchID) != matchID ||
			len(matchID) > 128 ||
			strings.IndexFunc(matchID, unicode.IsControl) >= 0 {
			return nil, malformedJSONError("match discovery")
		}
	}
	return matchIDs, nil
}

// MatchDetail fetches a MATCH-V5 detail document and retains its exact raw
// bytes for durable import.
func (c *Client) MatchDetail(
	ctx context.Context,
	route RegionalRoute,
	matchID string,
) (MatchPayload, error) {
	if strings.TrimSpace(matchID) == "" {
		return MatchPayload{}, fmt.Errorf("%w: match ID is required", ErrInvalidArgument)
	}

	path := "/lol/match/v5/matches/" + url.PathEscape(matchID)
	raw, err := c.get(ctx, "match detail", route, path, nil)
	if err != nil {
		return MatchPayload{}, err
	}

	var match Match
	if err := json.Unmarshal(raw, &match); err != nil {
		return MatchPayload{}, malformedJSONError("match detail")
	}
	return MatchPayload{Raw: raw, Match: match}, nil
}

// Timeline fetches a MATCH-V5 timeline document and retains its exact raw
// bytes so timeline failure can be handled independently from match detail.
func (c *Client) Timeline(
	ctx context.Context,
	route RegionalRoute,
	matchID string,
) (TimelinePayload, error) {
	if strings.TrimSpace(matchID) == "" {
		return TimelinePayload{}, fmt.Errorf("%w: match ID is required", ErrInvalidArgument)
	}

	path := "/lol/match/v5/matches/" + url.PathEscape(matchID) + "/timeline"
	raw, err := c.get(ctx, "match timeline", route, path, nil)
	if err != nil {
		return TimelinePayload{}, err
	}

	var timeline Timeline
	if err := json.Unmarshal(raw, &timeline); err != nil {
		return TimelinePayload{}, malformedJSONError("match timeline")
	}
	return TimelinePayload{Raw: raw, Timeline: timeline}, nil
}

func (c *Client) get(
	ctx context.Context,
	operation string,
	route RegionalRoute,
	path string,
	query url.Values,
) ([]byte, error) {
	requestURL, err := c.requestURL(route, path, query)
	if err != nil {
		return nil, err
	}

	for attempt := 1; attempt <= c.maxAttempts; attempt++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
		if err != nil {
			return nil, fmt.Errorf("%w: could not construct request", ErrInvalidArgument)
		}
		request.Header.Set("Accept", "application/json")
		request.Header.Set("User-Agent", c.userAgent)
		request.Header.Set("X-Riot-Token", c.apiKey)

		response, requestErr := c.httpClient.Do(request)
		if requestErr != nil {
			apiErr := networkError(operation, ctx, requestErr)
			if !apiErr.Retryable || attempt == c.maxAttempts {
				return nil, apiErr
			}
			if err := waitForRetry(ctx, c.retryDelay(attempt)); err != nil {
				return nil, canceledError(operation, err)
			}
			continue
		}

		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, discardBodyLimit))
			_ = response.Body.Close()

			retryAfter, hasRetryAfter := parseRetryAfter(response.Header.Get("Retry-After"), time.Now())
			apiErr := &APIError{
				Operation:  operation,
				Kind:       errorKindForStatus(response.StatusCode),
				StatusCode: response.StatusCode,
				RetryAfter: retryAfter,
				Retryable: response.StatusCode == http.StatusTooManyRequests ||
					response.StatusCode >= http.StatusInternalServerError,
			}
			if !apiErr.Retryable || attempt == c.maxAttempts {
				return nil, apiErr
			}

			delay := c.retryDelay(attempt)
			if response.StatusCode == http.StatusTooManyRequests && hasRetryAfter {
				delay = retryAfter
			}
			// A long server-directed pause belongs in the durable import
			// scheduler, not inside one HTTP call.
			if delay > c.maxRetryWait {
				return nil, apiErr
			}
			if err := waitForRetry(ctx, delay); err != nil {
				return nil, canceledError(operation, err)
			}
			continue
		}

		raw, readErr := readBounded(response.Body, c.maxResponseBytes)
		_ = response.Body.Close()
		if readErr != nil {
			if errors.Is(readErr, errResponseTooLarge) {
				return nil, &APIError{
					Operation:  operation,
					Kind:       ErrorResponseTooLarge,
					StatusCode: response.StatusCode,
				}
			}
			apiErr := &APIError{
				Operation:  operation,
				Kind:       ErrorNetwork,
				StatusCode: response.StatusCode,
				Retryable:  true,
			}
			if attempt == c.maxAttempts {
				return nil, apiErr
			}
			if err := waitForRetry(ctx, c.retryDelay(attempt)); err != nil {
				return nil, canceledError(operation, err)
			}
			continue
		}
		return raw, nil
	}

	panic("unreachable")
}

func (c *Client) requestURL(
	route RegionalRoute,
	path string,
	query url.Values,
) (string, error) {
	baseURL, ok := c.baseURLs[route]
	if !ok {
		return "", fmt.Errorf("%w: unsupported regional route", ErrInvalidArgument)
	}
	requestURL := baseURL + path
	if len(query) > 0 {
		requestURL += "?" + query.Encode()
	}
	return requestURL, nil
}

func (c *Client) retryDelay(attempt int) time.Duration {
	delay := c.retryBaseDelay
	for step := 1; step < attempt; step++ {
		if delay >= c.maxRetryWait/2 {
			return c.maxRetryWait
		}
		delay *= 2
	}
	if delay > c.maxRetryWait {
		return c.maxRetryWait
	}
	return delay
}

func normalizeBaseURL(value string) (string, error) {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("%w: base URL is invalid", ErrInvalidArgument)
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return "", fmt.Errorf("%w: base URL scheme is invalid", ErrInvalidArgument)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("%w: base URL cannot contain credentials, query, or fragment", ErrInvalidArgument)
	}
	return value, nil
}

var errResponseTooLarge = errors.New("response too large")

func readBounded(body io.Reader, limit int64) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, errResponseTooLarge
	}
	return raw, nil
}

func parseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds < 0 {
			return 0, false
		}
		return time.Duration(seconds) * time.Second, true
	}
	retryAt, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}
	delay := retryAt.Sub(now)
	if delay < 0 {
		delay = 0
	}
	return delay, true
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func networkError(operation string, ctx context.Context, requestErr error) *APIError {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return canceledError(operation, ctxErr)
	}
	if errors.Is(requestErr, context.Canceled) {
		return canceledError(operation, requestErr)
	}
	apiErr := &APIError{
		Operation: operation,
		Kind:      ErrorNetwork,
		Retryable: true,
	}
	if errors.Is(requestErr, context.DeadlineExceeded) {
		apiErr.cause = context.DeadlineExceeded
	}
	return apiErr
}

func canceledError(operation string, cause error) *APIError {
	return &APIError{
		Operation: operation,
		Kind:      ErrorCanceled,
		Retryable: false,
		cause:     cause,
	}
}

func malformedJSONError(operation string) *APIError {
	return &APIError{
		Operation: operation,
		Kind:      ErrorMalformedJSON,
		Retryable: false,
	}
}
