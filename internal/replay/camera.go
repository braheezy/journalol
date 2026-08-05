package replay

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// RenderState is the small portion of League's replay render state needed by
// Journalol. The Replay API can attach the camera to the currently selected
// game object, but it does not expose a participant ID with which to select
// that object.
type RenderState struct {
	CameraMode     string `json:"cameraMode"`
	CameraAttached bool   `json:"cameraAttached"`
}

// Render returns the current replay camera state.
func (c *Client) Render(ctx context.Context) (RenderState, error) {
	var render RenderState
	if err := c.get(ctx, "/replay/render", &render); err != nil {
		return RenderState{}, err
	}
	return render, nil
}

// PreparePlayerCamera leaves directed-camera mode, then asks League to select
// and follow the primary player's champion through native spectator input.
// selectPlayer is deliberately a callback because participant selection is
// game input rather than an HTTP Replay API operation. Replay API
// cameraAttached is not used: it controls an object-attached production camera,
// not the native top-down spectator follow state.
func (c *Client) PreparePlayerCamera(ctx context.Context, selectPlayer func() error) error {
	if selectPlayer == nil {
		return errors.New("player camera requires a champion selection action")
	}
	if err := c.post(ctx, "/replay/render", RenderState{
		CameraMode:     "top",
		CameraAttached: false,
	}, nil); err != nil {
		return fmt.Errorf("disable directed replay camera: %w", err)
	}
	if err := selectPlayer(); err != nil {
		return fmt.Errorf("select primary player in replay: %w", err)
	}

	// Give the game input queue time to apply the native spectator follow action.
	// The wait remains cancellable so Ctrl-C still performs normal cleanup.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(200 * time.Millisecond):
	}

	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()
	var lastState RenderState
	var lastErr error
	for {
		state, err := c.Render(ctx)
		if err != nil {
			lastErr = err
		} else {
			lastState = state
			if strings.EqualFold(state.CameraMode, "top") {
				return nil
			}
			lastErr = fmt.Errorf("League reported camera mode %q", state.CameraMode)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("replay camera did not return to top-down mode (last mode: %q): %w",
				lastState.CameraMode, lastErr)
		case <-ticker.C:
		}
	}
}
