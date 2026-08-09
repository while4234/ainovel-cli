package artwork

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const workspaceKind = "ainovel-artwork-workspace"

var workspaceLocks sync.Map

type workspaceSchema struct {
	SchemaVersion int       `json:"schema_version"`
	Kind          string    `json:"kind"`
	CreatedAt     time.Time `json:"created_at"`
}

type cursorValue struct {
	CreatedAt time.Time `json:"created_at"`
	ID        string    `json:"id"`
}

type WorkspaceStore struct {
	root  string
	mu    *sync.Mutex
	now   func() time.Time
	fault func(string) error
}

func NewWorkspaceStore(outputDir string) (*WorkspaceStore, error) {
	outputDir = strings.TrimSpace(outputDir)
	if outputDir == "" {
		return nil, errors.New("project output directory is required")
	}
	absOutput, err := filepath.Abs(outputDir)
	if err != nil {
		return nil, fmt.Errorf("resolve project output directory: %w", err)
	}
	root := filepath.Clean(filepath.Join(absOutput, "artwork"))
	lockValue, _ := workspaceLocks.LoadOrStore(strings.ToLower(root), &sync.Mutex{})
	store := &WorkspaceStore{
		root: root,
		mu:   lockValue.(*sync.Mutex),
		now:  func() time.Time { return time.Now().UTC() },
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.initializeUnlocked(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *WorkspaceStore) Root() string { return s.root }

func (s *WorkspaceStore) initializeUnlocked() error {
	for _, name := range []string{"drafts", "prompts", "jobs", "assets", "images", "apply", "staging", "journals"} {
		if err := os.MkdirAll(filepath.Join(s.root, name), 0o755); err != nil {
			return fmt.Errorf("create artwork %s directory: %w", name, err)
		}
	}
	schemaPath := filepath.Join(s.root, "schema.json")
	var schema workspaceSchema
	if err := readJSONFile(schemaPath, &schema); err == nil {
		if schema.SchemaVersion != WorkspaceSchemaVersion || schema.Kind != workspaceKind {
			return fmt.Errorf("unsupported artwork workspace schema %d", schema.SchemaVersion)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read artwork workspace schema: %w", err)
	}
	schema = workspaceSchema{SchemaVersion: WorkspaceSchemaVersion, Kind: workspaceKind, CreatedAt: s.now()}
	if err := writeJSONAtomic(schemaPath, schema, false); err != nil {
		return fmt.Errorf("create artwork workspace schema: %w", err)
	}
	return nil
}

func (s *WorkspaceStore) path(kind, id string) (string, error) {
	if err := validateRecordID(id); err != nil {
		return "", err
	}
	name := id
	if kind != "images" && kind != "staging" {
		name += ".json"
	}
	target := filepath.Join(s.root, kind, name)
	if err := ensureContained(s.root, target); err != nil {
		return "", err
	}
	return target, nil
}

func validateRecordID(id string) error {
	id = strings.TrimSpace(id)
	if len(id) < 3 || len(id) > 96 {
		return errors.New("artwork identifier is invalid")
	}
	for index, char := range id {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || (index > 0 && char == '-') {
			continue
		}
		return errors.New("artwork identifier is invalid")
	}
	return nil
}

func ensureContained(root, target string) error {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
	if err != nil || rel == "." || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("artwork path escapes its project workspace")
	}
	return nil
}

func readJSONFile(path string, target any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 2*1024*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("JSON record contains trailing content")
		}
		return err
	}
	return nil
}

func writeJSONAtomic(path string, value any, immutable bool) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeFileAtomic(path, data, immutable, 0o644)
}

func writeFileAtomic(path string, data []byte, immutable bool, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if immutable {
		if _, err := os.Lstat(path); err == nil {
			return os.ErrExist
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".artwork-write-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Chmod(mode); err != nil {
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
	if immutable {
		if err := os.Rename(temporaryPath, path); err != nil {
			return err
		}
		return syncDirectory(filepath.Dir(path))
	}
	return replaceFileDurably(temporaryPath, path)
}

func replaceFileDurably(temporaryPath, targetPath string) error {
	backupPath := targetPath + ".replace-backup"
	if err := recoverReplacement(targetPath); err != nil {
		return err
	}
	backedUp := false
	if _, err := os.Lstat(targetPath); err == nil {
		if err := os.Rename(targetPath, backupPath); err != nil {
			return err
		}
		backedUp = true
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(temporaryPath, targetPath); err != nil {
		if backedUp {
			_ = os.Rename(backupPath, targetPath)
		}
		return err
	}
	if err := syncDirectory(filepath.Dir(targetPath)); err != nil {
		return err
	}
	if backedUp {
		if err := os.Remove(backupPath); err != nil && !os.IsNotExist(err) {
			return err
		}
		return syncDirectory(filepath.Dir(targetPath))
	}
	return nil
}

func recoverReplacement(targetPath string) error {
	backupPath := targetPath + ".replace-backup"
	if _, err := os.Lstat(backupPath); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	if _, err := os.Lstat(targetPath); os.IsNotExist(err) {
		return os.Rename(backupPath, targetPath)
	} else if err != nil {
		return err
	}
	return os.Remove(backupPath)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		if runtime.GOOS == "windows" {
			return nil
		}
		return err
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil && runtime.GOOS != "windows" {
		return err
	}
	return nil
}

func randomID(prefix string) (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return prefix + "-" + hex.EncodeToString(value[:]), nil
}

func deterministicID(prefix string, values ...string) string {
	digest := sha256.New()
	for _, value := range values {
		_, _ = io.WriteString(digest, value)
		_, _ = digest.Write([]byte{0})
	}
	return prefix + "-" + hex.EncodeToString(digest.Sum(nil)[:16])
}

func fingerprint(value any) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func hashString(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func encodeCursor(createdAt time.Time, id string) string {
	payload, _ := json.Marshal(cursorValue{CreatedAt: createdAt.UTC(), ID: id})
	return base64.RawURLEncoding.EncodeToString(payload)
}

func decodeCursor(value string) (cursorValue, error) {
	if strings.TrimSpace(value) == "" {
		return cursorValue{}, nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(payload) > 512 {
		return cursorValue{}, ErrInvalidCursor
	}
	var cursor cursorValue
	if err := json.Unmarshal(payload, &cursor); err != nil || cursor.CreatedAt.IsZero() || validateRecordID(cursor.ID) != nil {
		return cursorValue{}, ErrInvalidCursor
	}
	return cursor, nil
}

func recordBeforeCursor(createdAt time.Time, id string, cursor cursorValue) bool {
	if cursor.CreatedAt.IsZero() {
		return true
	}
	return createdAt.Before(cursor.CreatedAt) || (createdAt.Equal(cursor.CreatedAt) && id > cursor.ID)
}

func normalizeLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	if limit > 100 {
		return 100
	}
	return limit
}

func jsonRecordPaths(directory string) ([]string, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".json") && !strings.HasSuffix(entry.Name(), ".replace-backup") {
			paths = append(paths, filepath.Join(directory, entry.Name()))
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func (s *WorkspaceStore) injectFault(stage string) error {
	if s.fault == nil {
		return nil
	}
	return s.fault(stage)
}
