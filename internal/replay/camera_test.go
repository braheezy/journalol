package replay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

func TestPreparePlayerCameraUsesNativeFollowWithoutAPIAttach(t *testing.T) {
	t.Parallel()
	steps := make([]string, 0, 4)
	client := NewClientForEndpoint("https://replay.test", &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/replay/render" {
			return nil, errors.New("unexpected Replay API path")
		}
		if request.Method == http.MethodGet {
			steps = append(steps, "verify")
			return jsonResponse(`{"cameraMode":"top","cameraAttached":false}`), nil
		}
		var state RenderState
		if err := json.NewDecoder(request.Body).Decode(&state); err != nil {
			t.Fatalf("decode render request: %v", err)
		}
		if state.CameraAttached {
			t.Fatal("PreparePlayerCamera() should not force Replay API cameraAttached")
		}
		steps = append(steps, "detach")
		if state.CameraMode != "top" {
			t.Fatalf("camera mode = %q, want top", state.CameraMode)
		}
		return jsonResponse(`{}`), nil
	})})
	client.pollInterval = time.Millisecond

	err := client.PreparePlayerCamera(context.Background(), func() error {
		steps = append(steps, "select")
		return nil
	})
	if err != nil {
		t.Fatalf("PreparePlayerCamera(): %v", err)
	}
	want := []string{"detach", "select", "verify"}
	if !reflect.DeepEqual(steps, want) {
		t.Fatalf("camera steps = %#v, want %#v", steps, want)
	}
}

func TestPreparePlayerCameraWaitsForTopDownState(t *testing.T) {
	t.Parallel()
	var reads atomic.Int32
	client := NewClientForEndpoint("https://replay.test", &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodPost {
			return jsonResponse(`{}`), nil
		}
		if reads.Add(1) == 1 {
			return jsonResponse(`{"cameraMode":"focus","cameraAttached":false}`), nil
		}
		return jsonResponse(`{"cameraMode":"top","cameraAttached":false}`), nil
	})})
	client.pollInterval = time.Millisecond

	if err := client.PreparePlayerCamera(context.Background(), func() error { return nil }); err != nil {
		t.Fatalf("PreparePlayerCamera(): %v", err)
	}
	if reads.Load() != 2 {
		t.Fatalf("render reads = %d, want 2", reads.Load())
	}
}

func TestPreparePlayerCameraReportsSelectionFailure(t *testing.T) {
	t.Parallel()
	client := NewClientForEndpoint("https://replay.test", &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost {
			return &http.Response{StatusCode: http.StatusNotFound, Status: "404 Not Found", Body: io.NopCloser(bytes.NewBufferString("not found")), Header: make(http.Header)}, nil
		}
		return jsonResponse(`{}`), nil
	})})
	want := errors.New("input unavailable")
	err := client.PreparePlayerCamera(context.Background(), func() error { return want })
	if err == nil || !errors.Is(err, want) {
		t.Fatalf("PreparePlayerCamera() error = %v, want selection failure", err)
	}
}
