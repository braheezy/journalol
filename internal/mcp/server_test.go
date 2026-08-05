package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"journalol/internal/store"
)

func TestServerListsToolsAndReturnsCoachingContext(t *testing.T) {
	dataStore, err := store.Open(filepath.Join(t.TempDir(), "journalol.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = dataStore.Close() })
	if err := dataStore.SeedDemo(context.Background()); err != nil {
		t.Fatalf("seed demo: %v", err)
	}

	input := strings.NewReader(strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"weekly_coaching_brief","arguments":{}}}`,
	}, "\n"))
	var output bytes.Buffer
	if err := NewServer(dataStore, time.UTC).Serve(context.Background(), input, &output); err != nil {
		t.Fatalf("Serve(): %v", err)
	}

	responses := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(responses) != 3 {
		t.Fatalf("response count = %d, want 3: %s", len(responses), output.String())
	}
	if !strings.Contains(responses[1], `"recent_matches"`) || !strings.Contains(responses[1], `"weekly_coaching_brief"`) {
		t.Fatalf("tools/list omitted coach tools: %s", responses[1])
	}
	if !strings.Contains(responses[2], `"recent_matches"`) || !strings.Contains(responses[2], `"coach_prompt"`) {
		t.Fatalf("weekly brief missing coaching context: %s", responses[2])
	}
}

func TestServerRejectsInvalidToolArgumentsWithoutWriting(t *testing.T) {
	dataStore, err := store.Open(filepath.Join(t.TempDir(), "journalol.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = dataStore.Close() })
	if err := dataStore.SeedDemo(context.Background()); err != nil {
		t.Fatalf("seed demo: %v", err)
	}

	input := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"recent_matches","arguments":{"limit":99}}}`)
	var output bytes.Buffer
	if err := NewServer(dataStore, time.UTC).Serve(context.Background(), input, &output); err != nil {
		t.Fatalf("Serve(): %v", err)
	}
	var response struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !response.Result.IsError {
		t.Fatalf("invalid argument did not return a tool error: %s", output.String())
	}
}
