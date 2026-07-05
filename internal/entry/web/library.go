package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const (
	simulationLibraryDirName = "simulation_library"
	novelLibraryDirName      = "novel_library"
	novelLibrarySourceName   = "source.txt"
	novelLibraryManifestName = "library.json"
)

type LibraryService struct {
	runtimeRoot string
}

type apiLibraryItem struct {
	Name         string    `json:"name"`
	FileName     string    `json:"file_name,omitempty"`
	Size         int64     `json:"size,omitempty"`
	UpdatedAt    time.Time `json:"updated_at,omitempty"`
	SourceCount  int       `json:"source_count,omitempty"`
	ChapterCount int       `json:"chapter_count,omitempty"`
	SourceFile   string    `json:"source_file,omitempty"`
}

type novelLibraryManifest struct {
	Version      int       `json:"version"`
	Name         string    `json:"name"`
	SourceFile   string    `json:"source_file"`
	ChapterCount int       `json:"chapter_count"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (s *Server) handleSimulationLibrary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	items, err := s.libraries.ListSimulationProfiles(r.URL.Query().Get("q"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleSimulationLibraryUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	headers, cleanup, err := parseMultipartFiles(w, r, maxMultipartUploadBytes)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(headers) == 0 {
		writeError(w, http.StatusBadRequest, "at least one simulation profile JSON file is required")
		return
	}
	uploads, err := readPendingUploads(headers, profileUploadExtensions, maxProfileUploadBytes, s.libraries.SimulationDir())
	if err != nil {
		writeUploadValidationError(w, err)
		return
	}
	items := make([]apiLibraryItem, 0, len(uploads))
	for _, upload := range uploads {
		profile, err := decodeSimulationProfile(upload.data)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("%s: %v", upload.Name, err))
			return
		}
		items = append(items, apiLibraryItem{
			Name:        strings.TrimSuffix(upload.Name, filepath.Ext(upload.Name)),
			FileName:    upload.Name,
			Size:        int64(len(upload.data)),
			SourceCount: len(profile.Corpus.Sources),
		})
	}
	if err := writePendingUploads(uploads, s.libraries.SimulationDir()); err != nil {
		writeUploadValidationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":   items,
		"message": fmt.Sprintf("uploaded %d simulation profile(s)", len(items)),
	})
}

func (s *Server) handleNovelLibrary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	items, err := s.libraries.ListNovelEntries(r.URL.Query().Get("q"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleProjectSimulationLibrarySave(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid simulation library save request: "+err.Error())
		return
	}
	manifest, err := s.store.OpenProject(id)
	if err != nil {
		writeProjectSessionError(w, fmt.Errorf("%w: %v", ErrProjectNotFound, err))
		return
	}
	item, err := s.libraries.SaveSimulationProfileFromProject(manifest, req.Name)
	if err != nil {
		writeLibraryActionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project": manifest,
		"item":    item,
	})
}

func (s *Server) handleProjectSimulationLibraryLoad(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid simulation library load request: "+err.Error())
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	item, importPath, err := s.libraries.LoadSimulationProfileIntoProject(manifest, req.Name)
	if err != nil {
		writeLibraryActionError(w, err)
		return
	}
	if err := session.StartImportSimulationProfile(importPath); err != nil {
		writeSimulationActionError(w, err, nil)
		return
	}
	status, err := projectSimulationStatus(
		manifest,
		session.isActionRunning(projectActionKindSimulationAnalysis),
		session.isActionRunning(projectActionKindSimulationImport),
	)
	if err != nil {
		writeSimulationActionError(w, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project":    manifest,
		"snapshot":   session.Snapshot(),
		"simulation": status,
		"item":       item,
		"events":     status.ImportEvents,
		"running":    true,
		"accepted":   true,
	})
}

func (s *Server) handleProjectNovelLibrarySave(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Name       string `json:"name"`
		SourceFile string `json:"source_file"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid novel library save request: "+err.Error())
		return
	}
	manifest, err := s.store.OpenProject(id)
	if err != nil {
		writeProjectSessionError(w, fmt.Errorf("%w: %v", ErrProjectNotFound, err))
		return
	}
	item, err := s.libraries.SaveNovelFromProject(manifest, req.Name, req.SourceFile)
	if err != nil {
		writeLibraryActionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project": manifest,
		"item":    item,
	})
}

func (s *Server) handleProjectNovelLibraryLoad(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid novel library load request: "+err.Error())
		return
	}
	manifest, err := s.store.OpenProject(id)
	if err != nil {
		writeProjectSessionError(w, fmt.Errorf("%w: %v", ErrProjectNotFound, err))
		return
	}
	item, sourceFile, err := s.libraries.LoadNovelIntoProject(manifest, req.Name)
	if err != nil {
		writeLibraryActionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project":     manifest,
		"item":        item,
		"source_file": sourceFile,
		"analyzed":    true,
	})
}

func (s *Server) trySaveImportedSimulationProfile(profile pendingUpload) (apiLibraryItem, bool, string) {
	name := strings.TrimSuffix(profile.Name, filepath.Ext(profile.Name))
	item, err := s.libraries.SaveSimulationProfile(name, profile.data)
	if err != nil {
		return apiLibraryItem{}, false, err.Error()
	}
	return item, true, ""
}

func NewLibraryService(runtimeRoot string) *LibraryService {
	return &LibraryService{runtimeRoot: filepath.Clean(runtimeRoot)}
}

func (s *LibraryService) SimulationDir() string {
	return filepath.Join(s.runtimeRoot, simulationLibraryDirName)
}

func (s *LibraryService) NovelDir() string {
	return filepath.Join(s.runtimeRoot, novelLibraryDirName)
}

func (s *LibraryService) ListSimulationProfiles(query string) ([]apiLibraryItem, error) {
	entries, err := os.ReadDir(s.SimulationDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list simulation library: %w", err)
	}
	query = strings.ToLower(strings.TrimSpace(query))
	items := make([]apiLibraryItem, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || strings.ToLower(filepath.Ext(entry.Name())) != ".json" {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		if query != "" && !strings.Contains(strings.ToLower(name), query) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("stat simulation library item %s: %w", entry.Name(), err)
		}
		item := apiLibraryItem{
			Name:      name,
			FileName:  entry.Name(),
			Size:      info.Size(),
			UpdatedAt: info.ModTime(),
		}
		if profile, err := readSimulationProfileFile(filepath.Join(s.SimulationDir(), entry.Name())); err == nil {
			item.SourceCount = len(profile.Corpus.Sources)
		}
		items = append(items, item)
	}
	sortLibraryItems(items)
	return items, nil
}

func (s *LibraryService) SaveSimulationProfile(name string, data []byte) (apiLibraryItem, error) {
	displayName, fileName, err := libraryJSONFilename(name)
	if err != nil {
		return apiLibraryItem{}, err
	}
	profile, err := decodeSimulationProfile(data)
	if err != nil {
		return apiLibraryItem{}, err
	}
	if err := os.MkdirAll(s.SimulationDir(), 0o755); err != nil {
		return apiLibraryItem{}, fmt.Errorf("create simulation library: %w", err)
	}
	target, err := safeLibraryTarget(s.SimulationDir(), fileName)
	if err != nil {
		return apiLibraryItem{}, err
	}
	if err := writeNewFile(target, bytes.TrimSpace(data)); err != nil {
		return apiLibraryItem{}, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return apiLibraryItem{}, fmt.Errorf("stat simulation profile %s: %w", displayName, err)
	}
	return apiLibraryItem{
		Name:        displayName,
		FileName:    fileName,
		Size:        info.Size(),
		UpdatedAt:   info.ModTime(),
		SourceCount: len(profile.Corpus.Sources),
	}, nil
}

func (s *LibraryService) SaveSimulationProfileFromProject(manifest ProjectManifest, name string) (apiLibraryItem, error) {
	sourcePath, err := findProjectSimulationProfile(manifest)
	if err != nil {
		return apiLibraryItem{}, err
	}
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		return apiLibraryItem{}, fmt.Errorf("read simulation profile: %w", err)
	}
	return s.SaveSimulationProfile(name, data)
}

func (s *LibraryService) LoadSimulationProfileIntoProject(manifest ProjectManifest, name string) (apiLibraryItem, string, error) {
	displayName, fileName, err := libraryJSONFilename(name)
	if err != nil {
		return apiLibraryItem{}, "", err
	}
	source, err := safeLibraryTarget(s.SimulationDir(), fileName)
	if err != nil {
		return apiLibraryItem{}, "", err
	}
	profile, err := readSimulationProfileFile(source)
	if err != nil {
		return apiLibraryItem{}, "", err
	}
	targetDir := projectImportedProfilesDir(manifest)
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return apiLibraryItem{}, "", fmt.Errorf("create imported profile dir: %w", err)
	}
	target, err := safeUploadTarget(targetDir, fileName)
	if err != nil {
		return apiLibraryItem{}, "", err
	}
	if err := copyFileOverwrite(source, target); err != nil {
		return apiLibraryItem{}, "", err
	}
	info, err := os.Stat(source)
	if err != nil {
		return apiLibraryItem{}, "", fmt.Errorf("stat simulation library item %s: %w", displayName, err)
	}
	return apiLibraryItem{
		Name:        displayName,
		FileName:    fileName,
		Size:        info.Size(),
		UpdatedAt:   info.ModTime(),
		SourceCount: len(profile.Corpus.Sources),
	}, target, nil
}

func (s *LibraryService) ListNovelEntries(query string) ([]apiLibraryItem, error) {
	entries, err := os.ReadDir(s.NovelDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list novel library: %w", err)
	}
	query = strings.ToLower(strings.TrimSpace(query))
	items := make([]apiLibraryItem, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		manifest, err := readNovelLibraryManifest(filepath.Join(s.NovelDir(), entry.Name()))
		if err != nil {
			return nil, err
		}
		if query != "" && !strings.Contains(strings.ToLower(manifest.Name), query) {
			continue
		}
		items = append(items, apiLibraryItem{
			Name:         manifest.Name,
			FileName:     entry.Name(),
			UpdatedAt:    manifest.UpdatedAt,
			ChapterCount: manifest.ChapterCount,
			SourceFile:   manifest.SourceFile,
		})
	}
	sortLibraryItems(items)
	return items, nil
}

func (s *LibraryService) SaveNovelFromProject(manifest ProjectManifest, name, sourceFile string) (apiLibraryItem, error) {
	displayName, entryName, err := libraryEntryName(name)
	if err != nil {
		return apiLibraryItem{}, err
	}
	sourcePath, err := adaptationSourcePathFromName(sourceFile, manifest, false)
	if err != nil {
		return apiLibraryItem{}, err
	}
	adaptationRoot, err := findProjectAdaptationRoot(manifest)
	if err != nil {
		return apiLibraryItem{}, err
	}
	return s.saveNovelEntry(displayName, entryName, adaptationRoot, sourcePath)
}

func (s *LibraryService) SaveNovelFromPreparedRoot(name, adaptationRoot, sourcePath string) (apiLibraryItem, error) {
	displayName, entryName, err := libraryEntryName(name)
	if err != nil {
		return apiLibraryItem{}, err
	}
	return s.saveNovelEntry(displayName, entryName, adaptationRoot, sourcePath)
}

func (s *LibraryService) LoadNovelIntoProject(manifest ProjectManifest, name string) (apiLibraryItem, apiUploadedFile, error) {
	displayName, entryName, err := libraryEntryName(name)
	if err != nil {
		return apiLibraryItem{}, apiUploadedFile{}, err
	}
	entryRoot, err := safeLibraryTarget(s.NovelDir(), entryName)
	if err != nil {
		return apiLibraryItem{}, apiUploadedFile{}, err
	}
	libraryManifest, err := readNovelLibraryManifest(entryRoot)
	if err != nil {
		return apiLibraryItem{}, apiUploadedFile{}, err
	}
	sourcePath := filepath.Join(entryRoot, "source", novelLibrarySourceName)
	if _, err := os.Stat(sourcePath); err != nil {
		if os.IsNotExist(err) {
			return apiLibraryItem{}, apiUploadedFile{}, fmt.Errorf("novel library source is missing for %q", displayName)
		}
		return apiLibraryItem{}, apiUploadedFile{}, fmt.Errorf("stat novel library source: %w", err)
	}

	sourceDir := projectAdaptationUploadDir(manifest)
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		return apiLibraryItem{}, apiUploadedFile{}, fmt.Errorf("create adaptation upload dir: %w", err)
	}
	projectSourceName := entryName + ".txt"
	projectSourcePath, err := safeUploadTarget(sourceDir, projectSourceName)
	if err != nil {
		return apiLibraryItem{}, apiUploadedFile{}, err
	}
	if err := copyFileOverwrite(sourcePath, projectSourcePath); err != nil {
		return apiLibraryItem{}, apiUploadedFile{}, err
	}
	projectSourceInfo, err := os.Stat(projectSourcePath)
	if err != nil {
		return apiLibraryItem{}, apiUploadedFile{}, fmt.Errorf("stat loaded source: %w", err)
	}

	projectRoot := projectNovelStoreRoot(manifest)
	projectAdaptationRoot := filepath.Join(projectRoot, "meta", "adaptation")
	projectMetaRoot := filepath.Dir(projectAdaptationRoot)
	if err := os.MkdirAll(projectMetaRoot, 0o755); err != nil {
		return apiLibraryItem{}, apiUploadedFile{}, fmt.Errorf("create project adaptation meta dir: %w", err)
	}
	tmpAdaptationRoot, err := os.MkdirTemp(projectMetaRoot, ".adaptation-tmp-*")
	if err != nil {
		return apiLibraryItem{}, apiUploadedFile{}, fmt.Errorf("create temporary project adaptation dir: %w", err)
	}
	defer os.RemoveAll(tmpAdaptationRoot)
	if err := copyDir(filepath.Join(entryRoot, "meta", "adaptation"), tmpAdaptationRoot); err != nil {
		return apiLibraryItem{}, apiUploadedFile{}, err
	}
	if err := rewriteAdaptationManifestFile(filepath.Join(tmpAdaptationRoot, "source_manifest.json"), projectSourcePath); err != nil {
		return apiLibraryItem{}, apiUploadedFile{}, err
	}
	if err := os.RemoveAll(projectAdaptationRoot); err != nil {
		return apiLibraryItem{}, apiUploadedFile{}, fmt.Errorf("replace project adaptation analysis: %w", err)
	}
	if err := os.Rename(tmpAdaptationRoot, projectAdaptationRoot); err != nil {
		return apiLibraryItem{}, apiUploadedFile{}, fmt.Errorf("install project adaptation analysis: %w", err)
	}

	item := apiLibraryItem{
		Name:         libraryManifest.Name,
		FileName:     entryName,
		UpdatedAt:    libraryManifest.UpdatedAt,
		ChapterCount: libraryManifest.ChapterCount,
		SourceFile:   libraryManifest.SourceFile,
	}
	sourceFile := apiUploadedFile{
		Name:         projectSourceName,
		OriginalName: libraryManifest.SourceFile,
		Size:         projectSourceInfo.Size(),
		RelativePath: filepath.ToSlash(projectSourceName),
	}
	return item, sourceFile, nil
}

func (s *LibraryService) saveNovelEntry(displayName, entryName, adaptationRoot, sourcePath string) (apiLibraryItem, error) {
	if strings.TrimSpace(adaptationRoot) == "" {
		return apiLibraryItem{}, fmt.Errorf("adaptation analysis path is required")
	}
	if strings.TrimSpace(sourcePath) == "" {
		return apiLibraryItem{}, fmt.Errorf("source file path is required")
	}
	info, err := os.Stat(sourcePath)
	if err != nil {
		return apiLibraryItem{}, fmt.Errorf("stat source file: %w", err)
	}
	if info.IsDir() {
		return apiLibraryItem{}, fmt.Errorf("source file must not be a directory")
	}
	sourceRoot, err := inferNovelStoreRoot(adaptationRoot)
	if err != nil {
		return apiLibraryItem{}, err
	}
	manifest, err := readAdaptationManifest(sourceRoot)
	if err != nil {
		return apiLibraryItem{}, err
	}
	if err := requirePreparedAdaptationFiles(sourceRoot, manifest.ChapterCount); err != nil {
		return apiLibraryItem{}, err
	}
	if err := os.MkdirAll(s.NovelDir(), 0o755); err != nil {
		return apiLibraryItem{}, fmt.Errorf("create novel library: %w", err)
	}
	entryRoot, err := safeLibraryTarget(s.NovelDir(), entryName)
	if err != nil {
		return apiLibraryItem{}, err
	}
	if _, err := os.Stat(entryRoot); err == nil {
		return apiLibraryItem{}, fmt.Errorf("library item %q already exists", displayName)
	} else if !os.IsNotExist(err) {
		return apiLibraryItem{}, fmt.Errorf("stat library item %q: %w", displayName, err)
	}

	tmpRoot, err := os.MkdirTemp(s.NovelDir(), "."+entryName+".tmp-*")
	if err != nil {
		return apiLibraryItem{}, fmt.Errorf("create temporary novel library entry: %w", err)
	}
	defer os.RemoveAll(tmpRoot)

	sourceTarget := filepath.Join(tmpRoot, "source", novelLibrarySourceName)
	if err := os.MkdirAll(filepath.Dir(sourceTarget), 0o755); err != nil {
		return apiLibraryItem{}, fmt.Errorf("create novel source dir: %w", err)
	}
	if err := copyFileOverwrite(sourcePath, sourceTarget); err != nil {
		return apiLibraryItem{}, err
	}
	if err := copyPreparedAdaptationFiles(sourceRoot, filepath.Join(tmpRoot, "meta", "adaptation")); err != nil {
		return apiLibraryItem{}, err
	}
	finalSourceTarget := filepath.Join(entryRoot, "source", novelLibrarySourceName)
	if err := rewriteAdaptationManifestSource(tmpRoot, finalSourceTarget); err != nil {
		return apiLibraryItem{}, err
	}

	now := time.Now().UTC()
	libraryManifest := novelLibraryManifest{
		Version:      1,
		Name:         displayName,
		SourceFile:   filepath.Base(sourcePath),
		ChapterCount: manifest.ChapterCount,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := writeJSONFile(filepath.Join(tmpRoot, novelLibraryManifestName), libraryManifest); err != nil {
		return apiLibraryItem{}, err
	}
	if err := os.Rename(tmpRoot, entryRoot); err != nil {
		return apiLibraryItem{}, err
	}
	return apiLibraryItem{
		Name:         displayName,
		FileName:     entryName,
		UpdatedAt:    now,
		ChapterCount: manifest.ChapterCount,
		SourceFile:   libraryManifest.SourceFile,
	}, nil
}

func sortLibraryItems(items []apiLibraryItem) {
	sort.SliceStable(items, func(i, j int) bool {
		if !items[i].UpdatedAt.Equal(items[j].UpdatedAt) {
			return items[i].UpdatedAt.After(items[j].UpdatedAt)
		}
		return items[i].Name < items[j].Name
	})
}

func writeLibraryActionError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	msg := err.Error()
	switch {
	case strings.Contains(msg, "already exists"):
		status = http.StatusConflict
	case strings.Contains(msg, "required") ||
		strings.Contains(msg, "not safe") ||
		strings.Contains(msg, "reserved") ||
		strings.Contains(msg, "has not been") ||
		strings.Contains(msg, "missing") ||
		strings.Contains(msg, "incomplete") ||
		strings.Contains(msg, "unsupported") ||
		strings.Contains(msg, "invalid"):
		status = http.StatusBadRequest
	}
	writeError(w, status, msg)
}

func decodeSimulationProfile(data []byte) (domain.SimulationProfile, error) {
	var profile domain.SimulationProfile
	if err := json.Unmarshal(bytes.TrimSpace(data), &profile); err != nil {
		return profile, fmt.Errorf("invalid simulation profile JSON: %w", err)
	}
	if err := domain.ValidateSimulationProfile(&profile); err != nil {
		return profile, err
	}
	return profile, nil
}

func readSimulationProfileFile(path string) (domain.SimulationProfile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return domain.SimulationProfile{}, fmt.Errorf("read simulation profile: %w", err)
	}
	return decodeSimulationProfile(data)
}

func libraryJSONFilename(name string) (string, string, error) {
	displayName, base, err := libraryEntryName(name)
	if err != nil {
		return "", "", err
	}
	return displayName, base + ".json", nil
}

func libraryEntryName(name string) (string, string, error) {
	displayName := strings.TrimSpace(name)
	if displayName == "" {
		return "", "", fmt.Errorf("library item name is required")
	}
	base := safeLibraryBase(displayName)
	if base == "" {
		return "", "", fmt.Errorf("library item name %q is not safe", displayName)
	}
	if isReservedWindowsName(base) {
		return "", "", fmt.Errorf("library item name %q is reserved", displayName)
	}
	return displayName, base, nil
}

func safeLibraryBase(name string) string {
	var b strings.Builder
	lastUnderscore := false
	for _, r := range strings.TrimSpace(name) {
		unsafe := r < 32 || strings.ContainsRune(`<>:"/\|?*`, r) || unicode.IsControl(r)
		if unsafe {
			if !lastUnderscore {
				b.WriteByte('_')
				lastUnderscore = true
			}
			continue
		}
		b.WriteRune(r)
		lastUnderscore = false
	}
	base := strings.Trim(b.String(), ". _")
	if len([]rune(base)) > 80 {
		runes := []rune(base)
		base = string(runes[:80])
		base = strings.Trim(base, ". _")
	}
	return base
}

func safeLibraryTarget(root, name string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("library item name is required")
	}
	if filepath.IsAbs(name) || isWindowsAbsolutePath(name) || strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("library item name %q must not contain a path", name)
	}
	target := filepath.Join(root, name)
	cleanRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	cleanTarget, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(cleanRoot, cleanTarget)
	if err != nil {
		return "", err
	}
	if rel == "." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || rel == ".." {
		return "", fmt.Errorf("library item path escapes library root")
	}
	return cleanTarget, nil
}

func findProjectSimulationProfile(manifest ProjectManifest) (string, error) {
	candidates := []string{
		filepath.Join(manifest.OutputDir, "meta", "simulation_profile.json"),
		filepath.Join(manifest.OutputDir, "novel", "meta", "simulation_profile.json"),
		filepath.Join(manifest.RootDir, "output", "novel", "meta", "simulation_profile.json"),
	}
	for _, path := range uniqueStrings(candidates) {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, nil
		}
	}
	return "", fmt.Errorf("simulation profile has not been generated; run simulation analysis first")
}

func findProjectAdaptationRoot(manifest ProjectManifest) (string, error) {
	candidates := []string{
		filepath.Join(projectNovelStoreRoot(manifest), "meta", "adaptation"),
		filepath.Join(manifest.OutputDir, "novel", "meta", "adaptation"),
		filepath.Join(manifest.RootDir, "output", "novel", "meta", "adaptation"),
	}
	for _, path := range uniqueStrings(candidates) {
		if info, err := os.Stat(filepath.Join(path, "source_manifest.json")); err == nil && !info.IsDir() {
			return path, nil
		}
	}
	return "", fmt.Errorf("adaptation source has not been analyzed; run source analysis first")
}

func projectNovelStoreRoot(manifest ProjectManifest) string {
	return manifest.OutputDir
}

func inferNovelStoreRoot(adaptationRoot string) (string, error) {
	root := filepath.Clean(adaptationRoot)
	if filepath.Base(root) != "adaptation" || filepath.Base(filepath.Dir(root)) != "meta" {
		return "", fmt.Errorf("adaptation root must end with meta/adaptation: %s", adaptationRoot)
	}
	return filepath.Dir(filepath.Dir(root)), nil
}

func readAdaptationManifest(storeRoot string) (domain.AdaptationSourceManifest, error) {
	var manifest domain.AdaptationSourceManifest
	path := filepath.Join(storeRoot, "meta", "adaptation", "source_manifest.json")
	if err := readJSONFile(path, &manifest); err != nil {
		return manifest, fmt.Errorf("read adaptation manifest: %w", err)
	}
	if manifest.ChapterCount <= 0 || len(manifest.Chapters) != manifest.ChapterCount {
		return manifest, fmt.Errorf("source manifest missing or incomplete")
	}
	return manifest, nil
}

func requirePreparedAdaptationFiles(storeRoot string, chapterCount int) error {
	requiredFiles := []string{
		filepath.Join(storeRoot, "meta", "adaptation", "source_manifest.json"),
		filepath.Join(storeRoot, "meta", "adaptation", "source_reports.json"),
		filepath.Join(storeRoot, "meta", "adaptation", "source_foundation.json"),
	}
	for _, path := range requiredFiles {
		if info, err := os.Stat(path); err != nil {
			return fmt.Errorf("required adaptation file missing %s: %w", path, err)
		} else if info.IsDir() {
			return fmt.Errorf("required adaptation file is a directory: %s", path)
		}
	}
	for _, dir := range []string{"source_chapters", "source_reports"} {
		path := filepath.Join(storeRoot, "meta", "adaptation", dir)
		entries, err := os.ReadDir(path)
		if err != nil {
			return fmt.Errorf("read adaptation %s: %w", dir, err)
		}
		count := 0
		for _, entry := range entries {
			if !entry.IsDir() {
				count++
			}
		}
		if count < chapterCount {
			return fmt.Errorf("adaptation %s incomplete: got %d files, want at least %d", dir, count, chapterCount)
		}
	}
	return nil
}

func copyPreparedAdaptationFiles(sourceRoot, targetAdaptationRoot string) error {
	if err := os.RemoveAll(targetAdaptationRoot); err != nil {
		return fmt.Errorf("replace adaptation library entry: %w", err)
	}
	if err := os.MkdirAll(targetAdaptationRoot, 0o755); err != nil {
		return fmt.Errorf("create adaptation library entry: %w", err)
	}
	sourceAdaptationRoot := filepath.Join(sourceRoot, "meta", "adaptation")
	for _, file := range []string{"source_manifest.json", "source_reports.json", "source_foundation.json"} {
		if err := copyFileOverwrite(filepath.Join(sourceAdaptationRoot, file), filepath.Join(targetAdaptationRoot, file)); err != nil {
			return err
		}
	}
	for _, dir := range []string{"source_chapters", "source_reports"} {
		if err := copyDir(filepath.Join(sourceAdaptationRoot, dir), filepath.Join(targetAdaptationRoot, dir)); err != nil {
			return err
		}
	}
	return nil
}

func rewriteAdaptationManifestSource(storeRoot, sourcePath string) error {
	manifestPath := filepath.Join(storeRoot, "meta", "adaptation", "source_manifest.json")
	return rewriteAdaptationManifestFile(manifestPath, sourcePath)
}

func rewriteAdaptationManifestFile(manifestPath, sourcePath string) error {
	var manifest domain.AdaptationSourceManifest
	if err := readJSONFile(manifestPath, &manifest); err != nil {
		return fmt.Errorf("read adaptation manifest: %w", err)
	}
	absSource, err := filepath.Abs(sourcePath)
	if err == nil {
		sourcePath = absSource
	}
	manifest.SourcePath = sourcePath
	return writeJSONFile(manifestPath, manifest)
}

func readNovelLibraryManifest(entryRoot string) (novelLibraryManifest, error) {
	var manifest novelLibraryManifest
	path := filepath.Join(entryRoot, novelLibraryManifestName)
	if err := readJSONFile(path, &manifest); err != nil {
		return manifest, fmt.Errorf("read novel library manifest: %w", err)
	}
	if strings.TrimSpace(manifest.Name) == "" {
		return manifest, fmt.Errorf("novel library item %s is missing a name", entryRoot)
	}
	return manifest, nil
}

func copyDir(sourceDir, targetDir string) error {
	info, err := os.Stat(sourceDir)
	if err != nil {
		return fmt.Errorf("stat directory %s: %w", sourceDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", sourceDir)
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return fmt.Errorf("create directory %s: %w", targetDir, err)
	}
	return filepath.WalkDir(sourceDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(targetDir, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFileOverwrite(path, target)
	})
}

func copyFileOverwrite(source, target string) error {
	input, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open source file %s: %w", source, err)
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create target dir %s: %w", filepath.Dir(target), err)
	}
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("open target file %s: %w", target, err)
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return fmt.Errorf("copy %s to %s: %w", source, target, err)
	}
	if err := output.Close(); err != nil {
		return fmt.Errorf("close target file %s: %w", target, err)
	}
	return nil
}

func writeNewFile(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("library item %q already exists", strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
		}
		return fmt.Errorf("write library file %s: %w", path, err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write library file %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("write library file %s: %w", path, err)
	}
	return nil
}

func readJSONFile(path string, out any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func writeJSONFile(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create JSON dir: %w", err)
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		key := strings.ToLower(filepath.Clean(value))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, value)
	}
	return unique
}
