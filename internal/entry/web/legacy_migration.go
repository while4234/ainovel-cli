package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/voocel/ainovel-cli/internal/bootstrap"
)

const legacyImportMarkerPath = ".ainovel/legacy-import.json"

var (
	errLegacySourceRequired = errors.New("source_dir is required")
	errLegacySourceMissing  = errors.New("legacy source directory does not exist")
	errLegacySourceInvalid  = errors.New("legacy source directory is invalid")
	legacyMigrationMu       sync.Mutex
)

type legacyMigrationRequest struct {
	SourceDir string `json:"source_dir"`
	Name      string `json:"name"`
}

type legacyMigrationResult struct {
	Project      ProjectManifest `json:"project"`
	Created      bool            `json:"created"`
	SourceHash   string          `json:"source_hash"`
	CopiedFiles  int             `json:"copied_files"`
	SkippedFiles []string        `json:"skipped_files,omitempty"`
}

type legacyImportMarker struct {
	Version     int       `json:"version"`
	SourceDir   string    `json:"source_dir"`
	SourceHash  string    `json:"source_hash"`
	ImportedAt  time.Time `json:"imported_at"`
	CopiedFiles int       `json:"copied_files"`
}

type legacyImportEntry struct {
	RelativePath string
	SourcePath   string
	Mode         fs.FileMode
	IsDir        bool
	Content      []byte
}

type legacyImportPlan struct {
	SourceDir    string
	Entries      []legacyImportEntry
	SafeConfig   []byte
	SkippedFiles []string
	SourceHash   string
}

func (s *Server) handleLegacyProjectMigration(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req legacyMigrationRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid migration request: "+err.Error())
		return
	}
	result, err := s.store.MigrateLegacyProject(req.SourceDir, req.Name)
	if err != nil {
		switch {
		case errors.Is(err, errLegacySourceRequired), errors.Is(err, errLegacySourceInvalid):
			writeError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, errLegacySourceMissing):
			writeError(w, http.StatusNotFound, err.Error())
		default:
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	status := http.StatusCreated
	if !result.Created {
		status = http.StatusOK
	}
	writeJSON(w, status, result)
}

// MigrateLegacyProject imports one explicitly selected legacy output directory.
// It never writes into an existing project and treats a matching content hash as
// an idempotent retry.
func (s *ProjectStore) MigrateLegacyProject(sourceDir, name string) (legacyMigrationResult, error) {
	plan, err := s.buildLegacyImportPlan(sourceDir)
	if err != nil {
		return legacyMigrationResult{}, err
	}

	legacyMigrationMu.Lock()
	defer legacyMigrationMu.Unlock()

	if existing, marker, found, err := s.findLegacyImport(plan.SourceHash); err != nil {
		return legacyMigrationResult{}, err
	} else if found {
		return legacyMigrationResult{
			Project:     existing,
			Created:     false,
			SourceHash:  marker.SourceHash,
			CopiedFiles: marker.CopiedFiles,
		}, nil
	}

	name = strings.TrimSpace(name)
	if name == "" {
		name = filepath.Base(plan.SourceDir)
	}
	manifest, err := s.CreateProject(name)
	if err != nil {
		return legacyMigrationResult{}, fmt.Errorf("create migrated project: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = removeAllWithRetry(manifest.RootDir)
		}
	}()

	copied, copiedHash, err := copyLegacyImportPlan(plan, manifest.OutputDir)
	if err != nil {
		return legacyMigrationResult{}, err
	}
	if copiedHash != plan.SourceHash {
		return legacyMigrationResult{}, fmt.Errorf("%w: source changed while it was being imported", errLegacySourceInvalid)
	}
	if len(plan.SafeConfig) > 0 {
		if err := writeFileAtomically(ProjectConfigPath(manifest), plan.SafeConfig, 0o600); err != nil {
			return legacyMigrationResult{}, fmt.Errorf("write sanitized project config: %w", err)
		}
	}
	marker := legacyImportMarker{
		Version:     1,
		SourceDir:   plan.SourceDir,
		SourceHash:  plan.SourceHash,
		ImportedAt:  time.Now().UTC(),
		CopiedFiles: copied,
	}
	if err := writeLegacyImportMarker(manifest, marker); err != nil {
		return legacyMigrationResult{}, err
	}
	committed = true
	return legacyMigrationResult{
		Project:      manifest,
		Created:      true,
		SourceHash:   plan.SourceHash,
		CopiedFiles:  copied,
		SkippedFiles: plan.SkippedFiles,
	}, nil
}

func (s *ProjectStore) buildLegacyImportPlan(sourceDir string) (legacyImportPlan, error) {
	sourceDir = strings.TrimSpace(sourceDir)
	if sourceDir == "" {
		return legacyImportPlan{}, errLegacySourceRequired
	}
	absSource, err := filepath.Abs(sourceDir)
	if err != nil {
		return legacyImportPlan{}, fmt.Errorf("%w: resolve source directory: %v", errLegacySourceInvalid, err)
	}
	absSource = filepath.Clean(absSource)
	info, err := os.Lstat(absSource)
	if err != nil {
		if os.IsNotExist(err) {
			return legacyImportPlan{}, fmt.Errorf("%w: %s", errLegacySourceMissing, absSource)
		}
		return legacyImportPlan{}, fmt.Errorf("inspect source directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return legacyImportPlan{}, fmt.Errorf("%w: source must be a real directory, not a link", errLegacySourceInvalid)
	}
	resolved, err := filepath.EvalSymlinks(absSource)
	if err != nil {
		return legacyImportPlan{}, fmt.Errorf("%w: resolve source directory: %v", errLegacySourceInvalid, err)
	}
	absSource, err = filepath.Abs(resolved)
	if err != nil {
		return legacyImportPlan{}, fmt.Errorf("%w: resolve source directory: %v", errLegacySourceInvalid, err)
	}
	absSource = filepath.Clean(absSource)
	runtimeRoot, err := canonicalPathForContainment(s.RuntimeRoot)
	if err != nil {
		return legacyImportPlan{}, fmt.Errorf("resolve runtime root: %w", err)
	}
	if pathsOverlap(absSource, runtimeRoot) {
		return legacyImportPlan{}, fmt.Errorf("%w: source directory must be outside the Web runtime root", errLegacySourceInvalid)
	}

	plan := legacyImportPlan{SourceDir: absSource}
	recognized := false
	err = filepath.WalkDir(absSource, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(absSource, path)
		if err != nil || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("%w: path escapes source directory", errLegacySourceInvalid)
		}
		if rel == "." {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: symbolic links are not allowed: %s", errLegacySourceInvalid, rel)
		}
		if entry.IsDir() {
			plan.Entries = append(plan.Entries, legacyImportEntry{RelativePath: rel, SourcePath: path, Mode: info.Mode(), IsDir: true})
			if isRecognizedLegacyTopLevel(rel) {
				recognized = true
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%w: special files are not allowed: %s", errLegacySourceInvalid, rel)
		}
		if isLegacyConfigPath(rel) {
			if len(plan.SafeConfig) == 0 {
				sanitized, err := sanitizeLegacyConfig(path)
				if err == nil {
					plan.SafeConfig = sanitized
				} else {
					plan.SkippedFiles = append(plan.SkippedFiles, filepath.ToSlash(rel)+" (unsafe or invalid config)")
				}
			} else {
				plan.SkippedFiles = append(plan.SkippedFiles, filepath.ToSlash(rel)+" (duplicate config)")
			}
			return nil
		}
		plan.Entries = append(plan.Entries, legacyImportEntry{RelativePath: rel, SourcePath: path, Mode: info.Mode()})
		if isRecognizedLegacyTopLevel(rel) {
			recognized = true
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, errLegacySourceInvalid) {
			return legacyImportPlan{}, err
		}
		return legacyImportPlan{}, fmt.Errorf("scan legacy source: %w", err)
	}
	if !recognized {
		return legacyImportPlan{}, fmt.Errorf("%w: directory does not contain recognizable novel output", errLegacySourceInvalid)
	}
	sort.Slice(plan.Entries, func(i, j int) bool { return plan.Entries[i].RelativePath < plan.Entries[j].RelativePath })
	plan.SourceHash, err = hashLegacyImportPlan(plan)
	if err != nil {
		return legacyImportPlan{}, err
	}
	return plan, nil
}

func sanitizeLegacyConfig(path string) ([]byte, error) {
	cfg, err := bootstrap.LoadConfigFile(path)
	if err != nil {
		return nil, err
	}
	cfg.OutputDir = ""
	cfg.PersistPath = ""
	cfg.PersistProjectOverlay = false
	cfg.PersistProviders = nil
	cfg.PersistProjectConfig = nil
	cfg.ProjectOwnedProviders = nil
	cfg.RuntimeRoot = ""
	cfg.Proxy = ""
	cfg.Notify = bootstrap.NotifyConfig{}
	for name, provider := range cfg.Providers {
		cfg.Providers[name] = bootstrap.ProviderConfig{Label: provider.Label, Models: append([]string(nil), provider.Models...)}
	}
	return json.MarshalIndent(cfg, "", "  ")
}

func hashLegacyImportPlan(plan legacyImportPlan) (string, error) {
	h := sha256.New()
	for _, entry := range plan.Entries {
		if err := hashLegacyEntry(h, entry, entry.SourcePath); err != nil {
			return "", fmt.Errorf("hash legacy source %s: %w", entry.RelativePath, err)
		}
	}
	if len(plan.SafeConfig) > 0 {
		hashLegacyHeader(h, "config", ".ainovel/config.json")
		_, _ = h.Write(plan.SafeConfig)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func copyLegacyImportPlan(plan legacyImportPlan, destination string) (int, string, error) {
	h := sha256.New()
	copied := 0
	for _, entry := range plan.Entries {
		if err := validateLegacyEntryBeforeCopy(plan.SourceDir, entry); err != nil {
			return 0, "", err
		}
		target, err := safeLegacyDestination(destination, entry.RelativePath)
		if err != nil {
			return 0, "", err
		}
		if entry.IsDir {
			if err := os.MkdirAll(target, entry.Mode.Perm()); err != nil {
				return 0, "", fmt.Errorf("create imported directory %s: %w", entry.RelativePath, err)
			}
			hashLegacyHeader(h, "dir", filepath.ToSlash(entry.RelativePath))
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return 0, "", err
		}
		if err := copyLegacyFile(entry.SourcePath, target, entry.Mode.Perm(), h, entry.RelativePath); err != nil {
			return 0, "", err
		}
		copied++
	}
	if len(plan.SafeConfig) > 0 {
		hashLegacyHeader(h, "config", ".ainovel/config.json")
		_, _ = h.Write(plan.SafeConfig)
	}
	return copied, hex.EncodeToString(h.Sum(nil)), nil
}

func validateLegacyEntryBeforeCopy(sourceRoot string, entry legacyImportEntry) error {
	info, err := os.Lstat(entry.SourcePath)
	if err != nil {
		return fmt.Errorf("%w: source changed before copy: %s", errLegacySourceInvalid, entry.RelativePath)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: symbolic links are not allowed: %s", errLegacySourceInvalid, entry.RelativePath)
	}
	if entry.IsDir != info.IsDir() || (!entry.IsDir && !info.Mode().IsRegular()) {
		return fmt.Errorf("%w: source entry type changed before copy: %s", errLegacySourceInvalid, entry.RelativePath)
	}
	resolved, err := filepath.EvalSymlinks(entry.SourcePath)
	if err != nil {
		return fmt.Errorf("%w: resolve source entry %s: %v", errLegacySourceInvalid, entry.RelativePath, err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil || !isSameOrChild(sourceRoot, resolved) || !sameFilesystemPath(entry.SourcePath, resolved) {
		return fmt.Errorf("%w: source entry resolves outside its declared path: %s", errLegacySourceInvalid, entry.RelativePath)
	}
	return nil
}

func copyLegacyFile(source, destination string, mode fs.FileMode, h hash.Hash, relativePath string) error {
	in, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open legacy file %s: %w", relativePath, err)
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return fmt.Errorf("create imported file %s: %w", relativePath, err)
	}
	remove := true
	defer func() {
		_ = out.Close()
		if remove {
			_ = os.Remove(destination)
		}
	}()
	hashLegacyHeader(h, "file", filepath.ToSlash(relativePath))
	if _, err := io.Copy(io.MultiWriter(out, h), in); err != nil {
		return fmt.Errorf("copy legacy file %s: %w", relativePath, err)
	}
	if err := out.Close(); err != nil {
		return err
	}
	remove = false
	return nil
}

func hashLegacyEntry(h hash.Hash, entry legacyImportEntry, source string) error {
	kind := "file"
	if entry.IsDir {
		kind = "dir"
	}
	hashLegacyHeader(h, kind, filepath.ToSlash(entry.RelativePath))
	if entry.IsDir {
		return nil
	}
	f, err := os.Open(source)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(h, f)
	return err
}

func hashLegacyHeader(h hash.Hash, kind, relativePath string) {
	_, _ = io.WriteString(h, kind+"\x00"+relativePath+"\x00")
}

func safeLegacyDestination(root, relativePath string) (string, error) {
	target := filepath.Join(root, relativePath)
	if !isSameOrChild(root, target) || sameFilesystemPath(root, target) {
		return "", fmt.Errorf("%w: invalid relative path %q", errLegacySourceInvalid, relativePath)
	}
	return target, nil
}

func (s *ProjectStore) findLegacyImport(sourceHash string) (ProjectManifest, legacyImportMarker, bool, error) {
	projects, err := s.ListProjects()
	if err != nil {
		return ProjectManifest{}, legacyImportMarker{}, false, err
	}
	for _, manifest := range projects {
		data, err := os.ReadFile(filepath.Join(manifest.RootDir, filepath.FromSlash(legacyImportMarkerPath)))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return ProjectManifest{}, legacyImportMarker{}, false, err
		}
		var marker legacyImportMarker
		if err := json.Unmarshal(data, &marker); err != nil {
			continue
		}
		if marker.SourceHash == sourceHash {
			return manifest, marker, true, nil
		}
	}
	return ProjectManifest{}, legacyImportMarker{}, false, nil
}

func writeLegacyImportMarker(manifest ProjectManifest, marker legacyImportMarker) error {
	data, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(manifest.RootDir, filepath.FromSlash(legacyImportMarkerPath))
	if err := writeFileAtomically(path, data, 0o600); err != nil {
		return fmt.Errorf("write legacy import marker: %w", err)
	}
	return nil
}

func writeFileAtomically(path string, data []byte, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func isRecognizedLegacyTopLevel(relativePath string) bool {
	top := strings.ToLower(strings.Split(filepath.ToSlash(relativePath), "/")[0])
	switch top {
	case "chapters", "drafts", "reviews", "summaries", "meta", "progress.json", "outline.json", "layered_outline.json", "premise.md", "premise.txt":
		return true
	default:
		return false
	}
}

func isLegacyConfigPath(relativePath string) bool {
	normalized := strings.ToLower(filepath.ToSlash(relativePath))
	return normalized == ".ainovel/config.json" || normalized == "config.json" || normalized == "config.jsonc"
}

func pathsOverlap(left, right string) bool {
	return isSameOrChild(left, right) || isSameOrChild(right, left)
}

func sameFilesystemPath(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}
