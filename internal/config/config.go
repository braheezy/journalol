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

// Config contains the small set of runtime settings needed by the local app.
type Config struct {
	Addr            string
	DBPath          string
	Location        *time.Location
	Demo            bool
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
		AllowedHosts:    allowedHosts,
		ShutdownTimeout: 10 * time.Second,
	}, nil
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
