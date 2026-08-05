// Package replay speaks only to League's local Replay API. It never contacts
// Riot's public API and deliberately permits the game client's self-signed
// certificate only for the fixed loopback address.
package replay

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"time"
)

const defaultEndpoint = "https://127.0.0.1:2999"

// RecordingRequest defines one bounded replay render. The Replay API writes a
// WebM clip directly to Path; it does not upload video anywhere.
type RecordingRequest struct {
	Path             string  `json:"path"`
	Codec            string  `json:"codec"`
	StartTime        float64 `json:"startTime"`
	EndTime          float64 `json:"endTime"`
	FramesPerSecond  int     `json:"framesPerSecond"`
	EnforceFrameRate bool    `json:"enforceFrameRate"`
	Recording        bool    `json:"recording"`
	// OnStarted is local-only. Replay recording performs a second simulation
	// reconstruction, so native spectator follow must be reapplied after the
	// API reports that recording has actually started.
	OnStarted func() error `json:"-"`
}

// GameState identifies the local game process serving the Replay API.
type GameState struct {
	ProcessID int `json:"processID"`
}

// PlaybackState reports whether a replay is fully loaded and its usable time
// range. League calls the total duration "length" in this API.
type PlaybackState struct {
	Paused  bool    `json:"paused"`
	Seeking bool    `json:"seeking"`
	Time    float64 `json:"time"`
	Speed   float64 `json:"speed"`
	Length  float64 `json:"length"`
}

// Client is small on purpose: it uses only the documented local Replay API
// endpoints and does not download or parse .rofl files.
type Client struct {
	endpoint     string
	httpClient   *http.Client
	pollInterval time.Duration
	stallTimeout time.Duration
	startSettle  time.Duration
}

// NewClient creates a client for the local Replay API. The client's
// self-signed TLS certificate is accepted only because the endpoint is fixed
// to the loopback game process.
func NewClient() *Client {
	return NewClientForEndpoint(defaultEndpoint, &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, // #nosec G402 -- fixed local League endpoint
	})
}

// NewClientForEndpoint is exported for tests. Production callers should use
// NewClient so a caller cannot accidentally send local replay paths elsewhere.
func NewClientForEndpoint(endpoint string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{
		endpoint:     strings.TrimRight(endpoint, "/"),
		httpClient:   httpClient,
		pollInterval: 500 * time.Millisecond,
		stallTimeout: 45 * time.Second,
		startSettle:  250 * time.Millisecond,
	}
}

// Check verifies that a replay is currently open and the Replay API has been
// enabled. The endpoint is unavailable when the normal client is open but no
// replay game process is running.
func (c *Client) Check(ctx context.Context) error {
	game, err := c.Game(ctx)
	if err != nil {
		return fmt.Errorf("Replay API is unavailable: %w", err)
	}
	if game.ProcessID == 0 {
		return errors.New("Replay API did not report an open replay")
	}
	return nil
}

// Game returns the process currently serving the local Replay API.
func (c *Client) Game(ctx context.Context) (GameState, error) {
	var game GameState
	if err := c.get(ctx, "/replay/game", &game); err != nil {
		return GameState{}, err
	}
	return game, nil
}

// Playback returns the current replay clock and total duration.
func (c *Client) Playback(ctx context.Context) (PlaybackState, error) {
	var playback PlaybackState
	if err := c.get(ctx, "/replay/playback", &playback); err != nil {
		return PlaybackState{}, err
	}
	return playback, nil
}

// PrepareRecording seeks while paused and waits for League to finish replay
// reconstruction before the encoder starts. Asking recording to perform a
// cold seek itself can wedge the macOS game client at a replay checkpoint.
func (c *Client) PrepareRecording(ctx context.Context, target float64, timeout time.Duration) error {
	if target < 0 || timeout <= 0 {
		return errors.New("invalid replay recording seek")
	}
	request := struct {
		Paused bool    `json:"paused"`
		Time   float64 `json:"time"`
	}{Paused: true, Time: target}
	if err := c.post(ctx, "/replay/playback", request, nil); err != nil {
		return fmt.Errorf("seek replay for recording: %w", err)
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()
	var lastErr error
	for {
		playback, err := c.Playback(ctx)
		switch {
		case err != nil:
			lastErr = err
		case playback.Seeking:
			lastErr = errors.New("League is still reconstructing the replay checkpoint")
		case math.Abs(playback.Time-target) > 0.75:
			lastErr = fmt.Errorf("League replay clock is %.2fs, expected %.2fs", playback.Time, target)
		default:
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("timed out seeking League replay before recording: %w", lastErr)
		case <-ticker.C:
		}
	}
}

// WaitReady waits for the game process Journalol launched to expose a loaded
// replay. Matching the expected PID prevents a stale replay on port 2999 from
// being captured by mistake.
func (c *Client) WaitReady(ctx context.Context, expectedPID int, timeout time.Duration) (GameState, PlaybackState, error) {
	if expectedPID < 0 {
		return GameState{}, PlaybackState{}, errors.New("expected replay process ID cannot be negative")
	}
	if timeout <= 0 {
		return GameState{}, PlaybackState{}, errors.New("replay startup timeout must be positive")
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()
	var lastErr error
	for {
		game, err := c.Game(ctx)
		switch {
		case err != nil:
			lastErr = err
		case game.ProcessID <= 0:
			lastErr = errors.New("Replay API did not report a game process")
		case expectedPID > 0 && game.ProcessID != expectedPID:
			lastErr = fmt.Errorf("Replay API belongs to process %d, expected launched process %d", game.ProcessID, expectedPID)
		default:
			playback, playbackErr := c.Playback(ctx)
			if playbackErr != nil {
				lastErr = playbackErr
			} else if playback.Length <= 0 {
				lastErr = errors.New("Replay API is up but playback is not loaded yet")
			} else {
				return game, playback, nil
			}
		}
		select {
		case <-ctx.Done():
			return GameState{}, PlaybackState{}, ctx.Err()
		case <-deadline.C:
			if lastErr == nil {
				lastErr = errors.New("Replay API did not become ready")
			}
			return GameState{}, PlaybackState{}, fmt.Errorf("timed out waiting for League replay startup: %w", lastErr)
		case <-ticker.C:
		}
	}
}

// Record asks the Replay API to render one bounded clip and waits until the
// API reports completion. Rendering can take longer than the clip duration.
func (c *Client) Record(ctx context.Context, request RecordingRequest) error {
	request.Path = strings.TrimSpace(request.Path)
	request.Codec = strings.ToLower(strings.TrimSpace(request.Codec))
	if request.Path == "" || request.Codec == "" || request.StartTime < 0 || request.EndTime <= request.StartTime {
		return errors.New("invalid replay recording request")
	}
	if request.FramesPerSecond < 1 || request.FramesPerSecond > 120 {
		return errors.New("frames per second must be between 1 and 120")
	}
	request.Recording = true
	if err := c.post(ctx, "/replay/recording", request, nil); err != nil {
		return fmt.Errorf("start replay recording: %w", err)
	}
	seenRecording := false
	if request.OnStarted != nil {
		if err := c.runRecordingStartHook(ctx, request.OnStarted); err != nil {
			return err
		}
		seenRecording = true
	}

	// The Replay API controls the rendered range through startTime/endTime; no
	// wall-clock sleep or screen capture is involved.
	deadline := time.NewTimer(maxDuration(5*time.Minute, time.Duration(request.EndTime-request.StartTime)*time.Second+2*time.Minute))
	defer deadline.Stop()
	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()
	idlePolls := 0
	var lastPollErr error
	lastProgress := time.Now()
	lastSize := int64(-1)
	for {
		var state struct {
			Recording bool `json:"recording"`
		}
		if err := c.get(ctx, "/replay/recording", &state); err != nil {
			// The game process can stop servicing HTTPS briefly while it encodes
			// frames. Keep polling within the bounded render deadline instead of
			// terminating a healthy recording on a transient client timeout.
			lastPollErr = err
		} else if state.Recording {
			seenRecording = true
			idlePolls = 0
		} else {
			idlePolls++
			if seenRecording || nonemptyFile(request.Path) || idlePolls >= 3 {
				return nil
			}
		}
		currentSize := generatedSize(request.Path)
		if currentSize != lastSize {
			lastSize = currentSize
			lastProgress = time.Now()
		} else if currentSize > 0 && time.Since(lastProgress) >= c.stallTimeout {
			return fmt.Errorf("League replay encoder stopped making progress for %s", c.stallTimeout.Round(time.Second))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			if lastPollErr != nil {
				return fmt.Errorf("timed out waiting for replay recording: %w", lastPollErr)
			}
			return errors.New("timed out waiting for replay recording")
		case <-ticker.C:
		}
	}
}

func (c *Client) runRecordingStartHook(ctx context.Context, hook func() error) error {
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(minDuration(c.pollInterval, 100*time.Millisecond))
	defer ticker.Stop()
	var lastErr error
	for {
		var state struct {
			Recording bool `json:"recording"`
		}
		if err := c.get(ctx, "/replay/recording", &state); err != nil {
			lastErr = err
		} else if state.Recording {
			settle := time.NewTimer(c.startSettle)
			select {
			case <-ctx.Done():
				settle.Stop()
				return ctx.Err()
			case <-settle.C:
			}
			if err := hook(); err != nil {
				return fmt.Errorf("restore player camera after recording start: %w", err)
			}
			return nil
		} else {
			lastErr = errors.New("League has not reported an active recording")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("timed out waiting to restore the player camera after recording start: %w", lastErr)
		case <-ticker.C:
		}
	}
}

func nonemptyFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Size() > 0
}

func generatedSize(path string) int64 {
	var size int64
	for _, candidate := range []string{path, path + ".tmp"} {
		if info, err := os.Stat(candidate); err == nil {
			size += info.Size()
		}
	}
	return size
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func (c *Client) get(ctx context.Context, path string, destination any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+path, nil)
	if err != nil {
		return err
	}
	return c.do(request, destination)
}

func (c *Client) post(ctx context.Context, path string, value, destination any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	return c.do(request, destination)
}

func (c *Client) do(request *http.Request, destination any) error {
	response, err := c.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("%s returned %s: %s", request.URL.Path, response.Status, sanitizeBody(body))
	}
	if destination != nil && len(body) > 0 {
		if err := json.Unmarshal(body, destination); err != nil {
			return fmt.Errorf("decode %s response: %w", request.URL.Path, err)
		}
	}
	return nil
}

func sanitizeBody(body []byte) string {
	text := strings.TrimSpace(string(body))
	if len(text) > 240 {
		return text[:240] + "…"
	}
	return text
}
