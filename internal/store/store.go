// Package store persists Journalol's local-first data in SQLite.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"journalol/internal/model"
)

var (
	ErrNotFound                 = errors.New("not found")
	ErrDemoProfileConflict      = errors.New("demo data cannot be mixed with a non-demo profile")
	ErrActiveTrainingBlock      = errors.New("another training block is already active")
	ErrTrainingBlockNeedsTarget = errors.New("a training block needs at least one target before activation")
	ErrInvalidInput             = errors.New("invalid input")
)

const (
	defaultMatchLimit = 50
	maxMatchLimit     = 200
	demoPUUID         = "demo-journalol-player-v1"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Store owns a single SQLite connection pool. SQLite is deliberately limited
// to one open connection for predictable in-memory tests and short local write
// transactions.
type Store struct {
	db *sql.DB
}

// Open opens (or creates) a database, configures SQLite, and applies every
// embedded migration before returning.
func Open(path string) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("%w: database path is required", ErrInvalidInput)
	}

	dsn, err := sqliteDSN(path)
	if err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	store := &Store{db: db}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	if err := configureSQLite(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}

	return store, nil
}

func sqliteDSN(path string) (string, error) {
	var dsn string
	if path == ":memory:" {
		dsn = "file:journalol-memory?mode=memory&cache=shared"
	} else if strings.HasPrefix(path, "file:") {
		dsn = path
	} else {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("resolve database path: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
			return "", fmt.Errorf("create database directory: %w", err)
		}
		dsn = (&url.URL{Scheme: "file", Path: filepath.ToSlash(absolute)}).String()
	}

	databaseURL, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("parse sqlite DSN: %w", err)
	}
	query := databaseURL.Query()
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "busy_timeout(5000)")
	databaseURL.RawQuery = query.Encode()
	return databaseURL.String(), nil
}

func configureSQLite(ctx context.Context, db *sql.DB) error {
	pragmas := []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
	}
	for _, statement := range pragmas {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("configure sqlite (%s): %w", statement, err)
		}
	}

	var foreignKeys int
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return fmt.Errorf("verify sqlite foreign keys: %w", err)
	}
	if foreignKeys != 1 {
		return errors.New("sqlite foreign key enforcement is unavailable")
	}
	var busyTimeout int
	if err := db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		return fmt.Errorf("verify sqlite busy timeout: %w", err)
	}
	if busyTimeout != 5000 {
		return fmt.Errorf("sqlite busy timeout is %dms, want 5000ms", busyTimeout)
	}
	return nil
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at INTEGER NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("create migration ledger: %w", err)
	}

	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return fmt.Errorf("read embedded migrations: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version, err := migrationVersion(entry.Name())
		if err != nil {
			return err
		}

		var exists int
		err = s.db.QueryRowContext(ctx,
			"SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = ?)",
			version,
		).Scan(&exists)
		if err != nil {
			return fmt.Errorf("check migration %s: %w", entry.Name(), err)
		}
		if exists == 1 {
			continue
		}

		body, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", entry.Name(), err)
		}
		if _, err = tx.ExecContext(ctx, string(body)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", entry.Name(), err)
		}
		if _, err = tx.ExecContext(ctx,
			"INSERT INTO schema_migrations(version, name, applied_at) VALUES (?, ?, ?)",
			version, entry.Name(), time.Now().UTC().Unix(),
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %s: %w", entry.Name(), err)
		}
		if err = tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", entry.Name(), err)
		}
	}
	return nil
}

func migrationVersion(name string) (int, error) {
	prefix, _, ok := strings.Cut(name, "_")
	if !ok {
		return 0, fmt.Errorf("migration %q has no numeric prefix", name)
	}
	version, err := strconv.Atoi(prefix)
	if err != nil || version < 1 {
		return 0, fmt.Errorf("migration %q has invalid version", name)
	}
	return version, nil
}

// Close flushes and closes the SQLite database.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Ping verifies that the database is reachable. It backs the readiness check.
func (s *Store) Ping(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("sqlite store is not open")
	}
	if err := s.db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping sqlite: %w", err)
	}
	return nil
}

// PrimaryPlayer returns the one configured primary player.
func (s *Store) PrimaryPlayer(ctx context.Context) (*model.PlayerProfile, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, game_name, tag_line, platform_route, regional_route, puuid,
		       profile_icon_id, summoner_level, is_primary, is_demo,
		       poll_interval_mins, history_limit, created_at, updated_at
		FROM player_profiles
		WHERE is_primary = 1
	`)
	player, err := scanPlayer(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get primary player: %w", err)
	}
	return &player, nil
}

// SavePrimaryPlayer creates or updates the primary profile. It is useful to the
// setup flow and deliberately refuses to coexist with demo data.
func (s *Store) SavePrimaryPlayer(ctx context.Context, player model.PlayerProfile) (*model.PlayerProfile, error) {
	player.GameName = strings.TrimSpace(player.GameName)
	player.TagLine = strings.TrimSpace(player.TagLine)
	player.PlatformRoute = strings.ToUpper(strings.TrimSpace(player.PlatformRoute))
	player.RegionalRoute = strings.ToUpper(strings.TrimSpace(player.RegionalRoute))
	player.PUUID = strings.TrimSpace(player.PUUID)
	if player.GameName == "" || player.TagLine == "" || player.PlatformRoute == "" ||
		player.RegionalRoute == "" || player.PUUID == "" {
		return nil, fmt.Errorf("%w: Riot ID, routes, and PUUID are required", ErrInvalidInput)
	}
	if player.IsDemo && player.PUUID != demoPUUID {
		return nil, fmt.Errorf("%w: reserved demo identity", ErrInvalidInput)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin save player: %w", err)
	}
	defer tx.Rollback()

	var otherProfiles int
	query := "SELECT COUNT(*) FROM player_profiles"
	args := []any{}
	if player.ID != 0 {
		query += " WHERE id <> ?"
		args = append(args, player.ID)
	}
	if err := tx.QueryRowContext(ctx, query, args...).Scan(&otherProfiles); err != nil {
		return nil, fmt.Errorf("check existing profiles: %w", err)
	}
	if otherProfiles > 0 {
		return nil, fmt.Errorf("%w: only one profile is supported", ErrInvalidInput)
	}

	var demoProfiles int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM player_profiles WHERE is_demo = 1").Scan(&demoProfiles); err != nil {
		return nil, fmt.Errorf("check demo profile: %w", err)
	}
	if demoProfiles > 0 && !player.IsDemo {
		return nil, ErrDemoProfileConflict
	}

	now := time.Now().UTC().Unix()
	if player.PollIntervalMins < 1 {
		player.PollIntervalMins = 5
	}
	if player.HistoryLimit < 1 || player.HistoryLimit > 100 {
		player.HistoryLimit = 20
	}

	if player.ID == 0 {
		result, err := tx.ExecContext(ctx, `
			INSERT INTO player_profiles (
				game_name, tag_line, platform_route, regional_route, puuid,
				profile_icon_id, summoner_level, is_primary, is_demo,
				poll_interval_mins, history_limit, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?)
		`, player.GameName, player.TagLine, player.PlatformRoute, player.RegionalRoute,
			player.PUUID, player.ProfileIconID, player.SummonerLevel, boolInt(player.IsDemo),
			player.PollIntervalMins, player.HistoryLimit, now, now)
		if err != nil {
			return nil, fmt.Errorf("insert player: %w", err)
		}
		player.ID, err = result.LastInsertId()
		if err != nil {
			return nil, fmt.Errorf("read player id: %w", err)
		}
	} else {
		result, err := tx.ExecContext(ctx, `
			UPDATE player_profiles
			SET game_name = ?, tag_line = ?, platform_route = ?, regional_route = ?,
			    puuid = ?, profile_icon_id = ?, summoner_level = ?, is_primary = 1,
			    is_demo = ?, poll_interval_mins = ?, history_limit = ?, updated_at = ?
			WHERE id = ?
		`, player.GameName, player.TagLine, player.PlatformRoute, player.RegionalRoute,
			player.PUUID, player.ProfileIconID, player.SummonerLevel, boolInt(player.IsDemo),
			player.PollIntervalMins, player.HistoryLimit, now, player.ID)
		if err != nil {
			return nil, fmt.Errorf("update player: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return nil, fmt.Errorf("read updated player count: %w", err)
		}
		if affected == 0 {
			return nil, ErrNotFound
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit player: %w", err)
	}
	return s.PrimaryPlayer(ctx)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanPlayer(row rowScanner) (model.PlayerProfile, error) {
	var player model.PlayerProfile
	var primary, demo int
	var createdAt, updatedAt int64
	err := row.Scan(
		&player.ID, &player.GameName, &player.TagLine, &player.PlatformRoute,
		&player.RegionalRoute, &player.PUUID, &player.ProfileIconID,
		&player.SummonerLevel, &primary, &demo, &player.PollIntervalMins,
		&player.HistoryLimit, &createdAt, &updatedAt,
	)
	player.IsPrimary = primary == 1
	player.IsDemo = demo == 1
	player.CreatedAt = unixTime(createdAt)
	player.UpdatedAt = unixTime(updatedAt)
	return player, err
}

func unixTime(seconds int64) time.Time {
	if seconds == 0 {
		return time.Time{}
	}
	return time.Unix(seconds, 0).UTC()
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
