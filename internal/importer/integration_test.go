package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"testing"

	"journalol/internal/riot"
	"journalol/internal/store"
)

func TestRiotHTTPClientThroughImporterAndSQLite(t *testing.T) {
	fixture := completeFakeAPI(t)
	httpClient := &http.Client{Transport: importerRoundTripFunc(
		func(r *http.Request) (*http.Response, error) {
			if got := r.Header.Get("X-Riot-Token"); got != "RGAPI-e2e-test" {
				t.Errorf("X-Riot-Token = %q", got)
			}
			var body any
			switch r.URL.EscapedPath() {
			case "/riot/account/v1/accounts/by-riot-id/Coach%20Cat/NA1":
				body = fixture.account
			case "/lol/match/v5/matches/by-puuid/primary-puuid/ids":
				if r.URL.Query().Get("start") != "0" || r.URL.Query().Get("count") != "20" {
					t.Errorf("match ID query = %q", r.URL.RawQuery)
				}
				switch r.URL.Query().Get("queue") {
				case "400", "440":
					body = []string{}
				case "420":
					body = []string{"NA1_e2e"}
				default:
					t.Errorf("match discovery queue = %q", r.URL.Query().Get("queue"))
					body = []string{}
				}
			case "/lol/match/v5/matches/NA1_e2e":
				detail := fixture.match
				detail.Metadata.MatchID = "NA1_e2e"
				body = detail
			case "/lol/match/v5/matches/NA1_e2e/timeline":
				timeline := fixture.timeline
				timeline.Metadata.MatchID = "NA1_e2e"
				body = timeline
			default:
				return nil, fmt.Errorf("unexpected Riot test path %q", r.URL.EscapedPath())
			}
			raw, err := json.Marshal(body)
			if err != nil {
				return nil, err
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewReader(raw)),
				Request:    r,
			}, nil
		},
	)}

	client, err := riot.NewClient(riot.ClientOptions{
		APIKey:      "RGAPI-e2e-test",
		MaxAttempts: 1,
		HTTPClient:  httpClient,
		BaseURLs: map[riot.RegionalRoute]string{
			riot.RouteAmericas: "https://riot.test",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	dataStore, err := store.Open(filepath.Join(t.TempDir(), "journalol.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dataStore.Close()
	service, err := NewService(dataStore, client, Settings{
		GameName:      "Coach Cat",
		TagLine:       "NA1",
		PlatformRoute: "NA1",
		RegionalRoute: "AMERICAS",
		HistoryLimit:  20,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	run, err := service.Sync(context.Background(), store.SyncTriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != store.SyncStateSucceeded || run.ImportedCount != 1 {
		t.Fatalf("sync run = %#v", run)
	}
	player, err := dataStore.PrimaryPlayer(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	matches, err := dataStore.RecentMatches(context.Background(), player.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].RiotMatchID != "NA1_e2e" ||
		matches[0].Completeness != store.MatchCompletenessComplete {
		t.Fatalf("stored matches = %#v", matches)
	}
}

type importerRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn importerRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}
