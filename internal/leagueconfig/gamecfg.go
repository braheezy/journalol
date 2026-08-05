// Package leagueconfig applies a short-lived, reversible League video
// configuration for automated replay capture.
package leagueconfig

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

var ErrCaptureBusy = errors.New("another replay capture is already using the League configuration")

const (
	backupFilename   = "game.cfg.backup"
	manifestFilename = "game.cfg.backup.json"
	lockFilename     = "capture.lock"
)

// CaptureSettings are the only game.cfg values Journalol changes. The exact
// original file is restored after the owned replay process exits.
type CaptureSettings struct {
	ConfigPath string
	StateDir   string
	Width      int
	Height     int
}

type manifest struct {
	ConfigPath string `json:"configPath"`
	Mode       uint32 `json:"mode"`
	SHA256     string `json:"sha256"`
}

// Lease holds the advisory capture lock and the durable backup needed to
// restore game.cfg. Restore is safe to call more than once.
type Lease struct {
	configPath   string
	stateDir     string
	lockFile     *os.File
	restoreOnce  sync.Once
	restoreError error
}

// Apply takes an exact backup, then atomically switches League to a small
// window and enables the local Replay API.
func Apply(settings CaptureSettings) (*Lease, error) {
	settings.ConfigPath = strings.TrimSpace(settings.ConfigPath)
	settings.StateDir = strings.TrimSpace(settings.StateDir)
	if settings.ConfigPath == "" || settings.StateDir == "" {
		return nil, errors.New("League config path and capture state directory are required")
	}
	if settings.Width < 640 || settings.Height < 480 || settings.Width > 7680 || settings.Height > 4320 {
		return nil, errors.New("capture window dimensions must be between 640x480 and 7680x4320")
	}
	configPath, err := filepath.Abs(settings.ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("resolve League config path: %w", err)
	}
	stateDir, err := filepath.Abs(settings.StateDir)
	if err != nil {
		return nil, fmt.Errorf("resolve capture state directory: %w", err)
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("create capture state directory: %w", err)
	}
	lockFile, err := acquireLock(filepath.Join(stateDir, lockFilename))
	if err != nil {
		return nil, err
	}
	lease := &Lease{configPath: configPath, stateDir: stateDir, lockFile: lockFile}
	fail := func(cause error) (*Lease, error) {
		lease.releaseLock()
		return nil, cause
	}

	if pendingBackup(stateDir) {
		return fail(fmt.Errorf(
			"an unfinished capture settings backup exists in %q; close League and run journalol capture restore-config before retrying",
			stateDir,
		))
	}
	original, err := os.ReadFile(configPath)
	if err != nil {
		return fail(fmt.Errorf("read League config: %w", err))
	}
	info, err := os.Stat(configPath)
	if err != nil {
		return fail(fmt.Errorf("stat League config: %w", err))
	}
	patched, err := patchGeneral(original, map[string]string{
		"EnableReplayApi": "1",
		"WindowMode":      "1",
		"Width":           strconv.Itoa(settings.Width),
		"Height":          strconv.Itoa(settings.Height),
	})
	if err != nil {
		return fail(err)
	}
	patched, err = patchSection(patched, "Replay", []string{"EnableDirectedCamera"}, map[string]string{
		"EnableDirectedCamera": "0",
	})
	if err != nil {
		return fail(err)
	}
	digest := sha256.Sum256(original)
	metadata, err := json.Marshal(manifest{
		ConfigPath: configPath,
		Mode:       uint32(info.Mode().Perm()),
		SHA256:     hex.EncodeToString(digest[:]),
	})
	if err != nil {
		return fail(fmt.Errorf("encode League config backup metadata: %w", err))
	}
	if err := atomicWrite(filepath.Join(stateDir, backupFilename), original, 0o600); err != nil {
		return fail(fmt.Errorf("save League config backup: %w", err))
	}
	if err := atomicWrite(filepath.Join(stateDir, manifestFilename), append(metadata, '\n'), 0o600); err != nil {
		_ = os.Remove(filepath.Join(stateDir, backupFilename))
		return fail(fmt.Errorf("save League config backup metadata: %w", err))
	}
	if err := atomicWrite(configPath, patched, info.Mode().Perm()); err != nil {
		restoreErr := restoreFiles(configPath, stateDir)
		lease.releaseLock()
		return nil, errors.Join(fmt.Errorf("apply League capture settings: %w", err), restoreErr)
	}
	return lease, nil
}

// Restore puts back the exact pre-capture bytes and releases the capture lock.
func (l *Lease) Restore() error {
	if l == nil {
		return nil
	}
	l.restoreOnce.Do(func() {
		l.restoreError = restoreFiles(l.configPath, l.stateDir)
		l.releaseLock()
	})
	return l.restoreError
}

// RestorePending recovers settings left behind if a prior Journalol process
// was killed before its normal deferred cleanup ran.
func RestorePending(configPath, stateDir string) error {
	resolvedConfig, err := filepath.Abs(strings.TrimSpace(configPath))
	if err != nil || strings.TrimSpace(configPath) == "" {
		return errors.New("League config path is required")
	}
	resolvedState, err := filepath.Abs(strings.TrimSpace(stateDir))
	if err != nil || strings.TrimSpace(stateDir) == "" {
		return errors.New("capture state directory is required")
	}
	lockFile, err := acquireLock(filepath.Join(resolvedState, lockFilename))
	if err != nil {
		return err
	}
	defer releaseLockFile(lockFile)
	if !pendingBackup(resolvedState) {
		return errors.New("no unfinished League capture settings backup was found")
	}
	return restoreFiles(resolvedConfig, resolvedState)
}

func restoreFiles(expectedConfigPath, stateDir string) error {
	metadataBytes, err := os.ReadFile(filepath.Join(stateDir, manifestFilename))
	if err != nil {
		return fmt.Errorf("read League config backup metadata: %w", err)
	}
	var metadata manifest
	if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
		return fmt.Errorf("decode League config backup metadata: %w", err)
	}
	if metadata.ConfigPath != expectedConfigPath {
		return fmt.Errorf("League config backup belongs to %q, not %q", metadata.ConfigPath, expectedConfigPath)
	}
	backup, err := os.ReadFile(filepath.Join(stateDir, backupFilename))
	if err != nil {
		return fmt.Errorf("read League config backup: %w", err)
	}
	digest := sha256.Sum256(backup)
	if hex.EncodeToString(digest[:]) != metadata.SHA256 {
		return errors.New("League config backup checksum does not match its manifest")
	}
	if err := atomicWrite(expectedConfigPath, backup, os.FileMode(metadata.Mode)); err != nil {
		return fmt.Errorf("restore League config: %w", err)
	}
	if err := os.Remove(filepath.Join(stateDir, manifestFilename)); err != nil {
		return fmt.Errorf("remove restored League config manifest: %w", err)
	}
	if err := os.Remove(filepath.Join(stateDir, backupFilename)); err != nil {
		return fmt.Errorf("remove restored League config backup: %w", err)
	}
	return nil
}

func pendingBackup(stateDir string) bool {
	for _, name := range []string{backupFilename, manifestFilename} {
		if _, err := os.Stat(filepath.Join(stateDir, name)); err == nil {
			return true
		}
	}
	return false
}

func acquireLock(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open capture lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrCaptureBusy
		}
		return nil, fmt.Errorf("lock League capture settings: %w", err)
	}
	return file, nil
}

func (l *Lease) releaseLock() {
	if l.lockFile == nil {
		return
	}
	releaseLockFile(l.lockFile)
	l.lockFile = nil
}

func releaseLockFile(file *os.File) {
	if file == nil {
		return
	}
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	_ = file.Close()
}

func atomicWrite(path string, content []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".journalol-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(mode.Perm()); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func patchGeneral(original []byte, values map[string]string) ([]byte, error) {
	keys := []string{"EnableReplayApi", "WindowMode", "Width", "Height"}
	return patchSection(original, "General", keys, values)
}

func patchSection(original []byte, sectionName string, keys []string, values map[string]string) ([]byte, error) {
	lines := splitLines(original)
	lineEnding := []byte("\n")
	if bytes.Contains(original, []byte("\r\n")) {
		lineEnding = []byte("\r\n")
	}
	sectionStart, sectionEnd := -1, len(lines)
	for index, line := range lines {
		trimmed := strings.TrimSpace(string(bytes.TrimSuffix(bytes.TrimSuffix(line, []byte("\n")), []byte("\r"))))
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			if sectionStart >= 0 {
				sectionEnd = index
				break
			}
			if strings.EqualFold(trimmed, "["+sectionName+"]") {
				sectionStart = index
			}
		}
	}
	if sectionStart < 0 {
		result := append([]byte(nil), original...)
		if len(result) > 0 && !bytes.HasSuffix(result, []byte("\n")) {
			result = append(result, lineEnding...)
		}
		if len(result) > 0 && !bytes.HasSuffix(result, append(lineEnding, lineEnding...)) {
			result = append(result, lineEnding...)
		}
		result = append(result, []byte("["+sectionName+"]")...)
		result = append(result, lineEnding...)
		for _, key := range keys {
			result = append(result, []byte(key+"="+values[key])...)
			result = append(result, lineEnding...)
		}
		return result, nil
	}

	found := make(map[string]bool, len(keys))
	canonical := make(map[string]string, len(keys))
	for _, key := range keys {
		canonical[strings.ToLower(key)] = key
	}
	for index := sectionStart + 1; index < sectionEnd; index++ {
		body, ending := lineBodyAndEnding(lines[index])
		equals := bytes.IndexByte(body, '=')
		if equals < 0 {
			continue
		}
		candidate := strings.ToLower(strings.TrimSpace(string(body[:equals])))
		key, ok := canonical[candidate]
		if !ok {
			continue
		}
		if found[key] {
			return nil, fmt.Errorf("League config contains duplicate %s values in [%s]", key, sectionName)
		}
		found[key] = true
		indentLength := len(body[:equals]) - len(bytes.TrimLeft(body[:equals], " \t"))
		replacement := append([]byte(nil), body[:indentLength]...)
		replacement = append(replacement, []byte(key+"="+values[key])...)
		replacement = append(replacement, ending...)
		lines[index] = replacement
	}
	missing := make([][]byte, 0, len(keys))
	for _, key := range keys {
		if !found[key] {
			line := append([]byte(key+"="+values[key]), lineEnding...)
			missing = append(missing, line)
		}
	}
	if len(missing) > 0 {
		if sectionEnd > 0 && len(lines[sectionEnd-1]) > 0 && !bytes.HasSuffix(lines[sectionEnd-1], []byte("\n")) {
			lines[sectionEnd-1] = append(lines[sectionEnd-1], lineEnding...)
		}
		lines = append(lines[:sectionEnd], append(missing, lines[sectionEnd:]...)...)
	}
	return bytes.Join(lines, nil), nil
}

func splitLines(content []byte) [][]byte {
	if len(content) == 0 {
		return nil
	}
	lines := make([][]byte, 0, bytes.Count(content, []byte("\n"))+1)
	for len(content) > 0 {
		index := bytes.IndexByte(content, '\n')
		if index < 0 {
			lines = append(lines, append([]byte(nil), content...))
			break
		}
		lines = append(lines, append([]byte(nil), content[:index+1]...))
		content = content[index+1:]
	}
	return lines
}

func lineBodyAndEnding(line []byte) ([]byte, []byte) {
	switch {
	case bytes.HasSuffix(line, []byte("\r\n")):
		return line[:len(line)-2], line[len(line)-2:]
	case bytes.HasSuffix(line, []byte("\n")):
		return line[:len(line)-1], line[len(line)-1:]
	default:
		return line, nil
	}
}
