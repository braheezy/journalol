package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAddr   = "127.0.0.1:8080"
	defaultDBPath = "./journalol.db"
)

// RiotConfig contains the server-side settings needed to resolve one Riot ID
// and import its League match history. APIKey is intentionally runtime-only:
// it is never passed to the web package or persisted.
type RiotConfig struct {
	APIKey        string
	GameName      string
	TagLine       string
	PlatformRoute string
	RegionalRoute string
	HistoryLimit  int
	PollInterval  time.Duration
	SyncOnStart   bool
}

// Config contains the small set of runtime settings needed by the local app.
type Config struct {
	Addr            string
	DBPath          string
	Location        *time.Location
	Demo            bool
	Riot            *RiotConfig
	AllowedHosts    map[string]struct{}
	ShutdownTimeout time.Duration
}

// Load reads configuration from environment variables.
func Load() (Config, error) {
	locationName := envOrDefault("JOURNALOL_TIMEZONE", "UTC")
	location, err := time.LoadLocation(locationName)
	if err != nil {
		return Config{}, fmt.Errorf("load JOURNALOL_TIMEZONE %q: %w", locationName, err)
	}

	demo, err := envBool("JOURNALOL_DEMO", true)
	if err != nil {
		return Config{}, err
	}
	riot, err := loadRiotConfig()
	if err != nil {
		return Config{}, err
	}

	allowedHosts := map[string]struct{}{
		"localhost": {},
		"127.0.0.1": {},
		"::1":       {},
	}
	for _, host := range strings.Split(os.Getenv("JOURNALOL_ALLOWED_HOSTS"), ",") {
		host = strings.TrimSpace(strings.ToLower(host))
		if host != "" {
			allowedHosts[host] = struct{}{}
		}
	}

	return Config{
		Addr:            envOrDefault("JOURNALOL_ADDR", defaultAddr),
		DBPath:          envOrDefault("JOURNALOL_DB_PATH", defaultDBPath),
		Location:        location,
		Demo:            demo,
		Riot:            riot,
		AllowedHosts:    allowedHosts,
		ShutdownTimeout: 10 * time.Second,
	}, nil
}

func loadRiotConfig() (*RiotConfig, error) {
	apiKey := strings.TrimSpace(os.Getenv("RIOT_API_KEY"))
	apiKeyFile := strings.TrimSpace(os.Getenv("RIOT_API_KEY_FILE"))
	gameName := strings.TrimSpace(os.Getenv("JOURNALOL_RIOT_GAME_NAME"))
	tagLine := strings.TrimSpace(os.Getenv("JOURNALOL_RIOT_TAG_LINE"))
	platform := strings.ToUpper(strings.TrimSpace(os.Getenv("JOURNALOL_RIOT_PLATFORM_ROUTE")))
	regional := strings.ToUpper(strings.TrimSpace(os.Getenv("JOURNALOL_RIOT_REGIONAL_ROUTE")))

	if apiKey == "" && apiKeyFile != "" {
		body, err := os.ReadFile(apiKeyFile)
		if err != nil {
			return nil, fmt.Errorf("read RIOT_API_KEY_FILE: %w", err)
		}
		if len(body) > 4096 {
			return nil, fmt.Errorf("RIOT_API_KEY_FILE is unexpectedly large")
		}
		apiKey = strings.TrimSpace(string(body))
	}

	if apiKey == "" && apiKeyFile == "" && gameName == "" && tagLine == "" &&
		platform == "" && regional == "" {
		return nil, nil
	}

	missing := make([]string, 0, 4)
	if apiKey == "" {
		missing = append(missing, "RIOT_API_KEY (or RIOT_API_KEY_FILE)")
	}
	if gameName == "" {
		missing = append(missing, "JOURNALOL_RIOT_GAME_NAME")
	}
	if tagLine == "" {
		missing = append(missing, "JOURNALOL_RIOT_TAG_LINE")
	}
	if platform == "" {
		missing = append(missing, "JOURNALOL_RIOT_PLATFORM_ROUTE")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("incomplete Riot configuration; missing %s", strings.Join(missing, ", "))
	}

	derivedRegional, ok := regionalRouteForPlatform(platform)
	if !ok {
		return nil, fmt.Errorf(
			"JOURNALOL_RIOT_PLATFORM_ROUTE %q is unsupported; use a League platform such as NA1, EUW1, KR, or OC1",
			platform,
		)
	}
	if regional == "" {
		regional = derivedRegional
	}
	if !validRegionalRoute(regional) {
		return nil, fmt.Errorf(
			"JOURNALOL_RIOT_REGIONAL_ROUTE %q is unsupported; use AMERICAS, ASIA, EUROPE, or SEA",
			regional,
		)
	}

	historyLimit, err := envIntRange("JOURNALOL_RIOT_HISTORY_LIMIT", 20, 1, 100)
	if err != nil {
		return nil, err
	}
	pollInterval, err := envDuration("JOURNALOL_RIOT_POLL_INTERVAL", 5*time.Minute)
	if err != nil {
		return nil, err
	}
	if pollInterval != 0 && pollInterval < time.Minute {
		return nil, fmt.Errorf("JOURNALOL_RIOT_POLL_INTERVAL must be 0 or at least 1m")
	}
	syncOnStart, err := envBool("JOURNALOL_RIOT_SYNC_ON_START", true)
	if err != nil {
		return nil, err
	}

	return &RiotConfig{
		APIKey:        apiKey,
		GameName:      gameName,
		TagLine:       tagLine,
		PlatformRoute: platform,
		RegionalRoute: regional,
		HistoryLimit:  historyLimit,
		PollInterval:  pollInterval,
		SyncOnStart:   syncOnStart,
	}, nil
}

func regionalRouteForPlatform(platform string) (string, bool) {
	switch strings.ToUpper(strings.TrimSpace(platform)) {
	case "NA1", "BR1", "LA1", "LA2":
		return "AMERICAS", true
	case "KR", "JP1":
		return "ASIA", true
	case "EUW1", "EUN1", "TR1", "RU":
		return "EUROPE", true
	case "OC1", "PH2", "SG2", "TH2", "TW2", "VN2":
		return "SEA", true
	default:
		return "", false
	}
}

func validRegionalRoute(route string) bool {
	switch route {
	case "AMERICAS", "ASIA", "EUROPE", "SEA":
		return true
	default:
		return false
	}
}

func envOrDefault(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func envBool(key string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean: %w", key, err)
	}
	return value, nil
}

func envIntRange(key string, fallback, minimum, maximum int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minimum || value > maximum {
		return 0, fmt.Errorf("%s must be an integer between %d and %d", key, minimum, maximum)
	}
	return value, nil
}

func envDuration(key string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	if raw == "0" {
		return 0, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be 0 or a valid duration: %w", key, err)
	}
	if value < 0 {
		return 0, fmt.Errorf("%s must be 0 or a non-negative duration", key)
	}
	return value, nil
}
