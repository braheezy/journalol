package replay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestRecordUsesBoundedReplayRange(t *testing.T) {
	var recordingReads atomic.Int32
	var startHookCalls atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/replay/recording":
			if request.Method == http.MethodPost {
				var got RecordingRequest
				if err := json.NewDecoder(request.Body).Decode(&got); err != nil {
					t.Fatalf("decode recording request: %v", err)
				}
				if !got.Recording || got.Path != "/tmp/death-01.webm" || got.StartTime != 30 || got.EndTime != 105 || got.Codec != "webm" {
					t.Fatalf("recording request = %#v", got)
				}
				if got.OnStarted != nil {
					t.Fatal("local recording hook was serialized to Replay API")
				}
				return jsonResponse(`{}`), nil
			}
			read := recordingReads.Add(1)
			if read == 1 {
				if startHookCalls.Load() != 0 {
					t.Fatal("recording start hook ran before active recording state")
				}
				return jsonResponse(`{"recording":true}`), nil
			}
			if startHookCalls.Load() != 1 {
				t.Fatal("recording completion was polled before start hook")
			}
			return jsonResponse(`{"recording":false}`), nil
		default:
			return &http.Response{StatusCode: http.StatusNotFound, Status: "404 Not Found", Body: io.NopCloser(bytes.NewBufferString("not found")), Header: make(http.Header)}, nil
		}
	})}

	replayClient := NewClientForEndpoint("https://replay.test", client)
	replayClient.pollInterval = time.Millisecond
	replayClient.startSettle = time.Millisecond
	if err := replayClient.Record(context.Background(), RecordingRequest{
		Path: "/tmp/death-01.webm", Codec: "webm", StartTime: 30, EndTime: 105,
		FramesPerSecond: 60, EnforceFrameRate: true,
		OnStarted: func() error {
			startHookCalls.Add(1)
			return nil
		},
	}); err != nil {
		t.Fatalf("Record(): %v", err)
	}
	if startHookCalls.Load() != 1 {
		t.Fatalf("recording start hook calls = %d, want 1", startHookCalls.Load())
	}
}

func TestRecordPropagatesPostReconstructionCameraFailure(t *testing.T) {
	t.Parallel()
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodPost {
			return jsonResponse(`{}`), nil
		}
		return jsonResponse(`{"recording":true}`), nil
	})}
	client := NewClientForEndpoint("https://replay.test", httpClient)
	client.pollInterval = time.Millisecond
	client.startSettle = time.Millisecond
	want := errors.New("camera input failed")
	err := client.Record(context.Background(), RecordingRequest{
		Path: "/tmp/journalol-hook-failure.webm", Codec: "webm", StartTime: 1, EndTime: 2,
		FramesPerSecond: 30, OnStarted: func() error { return want },
	})
	if err == nil || !errors.Is(err, want) {
		t.Fatalf("Record() error = %v, want camera hook failure", err)
	}
}

func TestWaitReadyRetriesUntilExpectedReplayIsLoaded(t *testing.T) {
	var gameReads atomic.Int32
	var playbackReads atomic.Int32
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/replay/game":
			if gameReads.Add(1) == 1 {
				return nil, errors.New("connection refused")
			}
			return jsonResponse(`{"processID":42}`), nil
		case "/replay/playback":
			if playbackReads.Add(1) == 1 {
				return jsonResponse(`{"length":0}`), nil
			}
			return jsonResponse(`{"paused":true,"time":5,"length":180}`), nil
		default:
			return nil, errors.New("unexpected replay API path")
		}
	})}
	client := NewClientForEndpoint("https://replay.test", httpClient)
	client.pollInterval = time.Millisecond
	game, playback, err := client.WaitReady(context.Background(), 42, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("WaitReady(): %v", err)
	}
	if game.ProcessID != 42 || playback.Length != 180 || !playback.Paused {
		t.Fatalf("WaitReady() = game %#v, playback %#v", game, playback)
	}
}

func TestPrepareRecordingWaitsForReplaySeek(t *testing.T) {
	var reads atomic.Int32
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodPost {
			var got struct {
				Paused bool    `json:"paused"`
				Time   float64 `json:"time"`
			}
			if err := json.NewDecoder(request.Body).Decode(&got); err != nil {
				t.Fatalf("decode playback request: %v", err)
			}
			if !got.Paused || got.Time != 150 {
				t.Fatalf("playback request = %#v", got)
			}
			return jsonResponse(`{}`), nil
		}
		if reads.Add(1) == 1 {
			return jsonResponse(`{"paused":true,"seeking":true,"time":120,"length":600}`), nil
		}
		return jsonResponse(`{"paused":true,"seeking":false,"time":150,"length":600}`), nil
	})}
	client := NewClientForEndpoint("https://replay.test", httpClient)
	client.pollInterval = time.Millisecond
	if err := client.PrepareRecording(context.Background(), 150, 100*time.Millisecond); err != nil {
		t.Fatalf("PrepareRecording(): %v", err)
	}
	if reads.Load() != 2 {
		t.Fatalf("playback reads = %d, want 2", reads.Load())
	}
}

func TestRecordRetriesTransientStatusTimeout(t *testing.T) {
	var reads atomic.Int32
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodPost {
			return jsonResponse(`{}`), nil
		}
		switch reads.Add(1) {
		case 1:
			return nil, errors.New("temporary timeout")
		case 2:
			return jsonResponse(`{"recording":true}`), nil
		default:
			return jsonResponse(`{"recording":false}`), nil
		}
	})}
	client := NewClientForEndpoint("https://replay.test", httpClient)
	client.pollInterval = time.Millisecond
	if err := client.Record(context.Background(), RecordingRequest{
		Path: "/tmp/journalol-retry.webm", Codec: "webm", StartTime: 1, EndTime: 2,
		FramesPerSecond: 60,
	}); err != nil {
		t.Fatalf("Record(): %v", err)
	}
	if reads.Load() != 3 {
		t.Fatalf("recording status reads = %d, want 3", reads.Load())
	}
}

func TestWaitReadyRejectsDifferentReplayProcess(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return jsonResponse(`{"processID":99}`), nil
	})}
	client := NewClientForEndpoint("https://replay.test", httpClient)
	client.pollInterval = time.Millisecond
	_, _, err := client.WaitReady(context.Background(), 42, 3*time.Millisecond)
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("process 99")) {
		t.Fatalf("WaitReady() error = %v, want mismatched PID", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func jsonResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Body:       io.NopCloser(bytes.NewBufferString(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
}
