package web

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/host"
)

const manifestVersion = 1

type ProjectManifest struct {
	Version        int        `json:"version"`
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	RootDir        string     `json:"root_dir"`
	OutputDir      string     `json:"output_dir"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	LastAccessedAt time.Time  `json:"last_accessed_at"`
	DeletedAt      *time.Time `json:"deleted_at,omitempty"`
}

type ProjectStore struct {
	RuntimeRoot string
}

func NewProjectStore(runtimeRoot string) *ProjectStore {
	return &ProjectStore{RuntimeRoot: filepath.Clean(runtimeRoot)}
}

func (s *ProjectStore) ProjectsDir() string {
	return filepath.Join(s.RuntimeRoot, "projects")
}

func (s *ProjectStore) ProjectTrashDir() string {
	return filepath.Join(s.RuntimeRoot, "trash", "projects")
}

func (s *ProjectStore) CreateProject(name string) (ProjectManifest, error) {
	return s.createProject(name)
}

func (s *ProjectStore) CreateProjectWithStyle(name, style string) (ProjectManifest, error) {
	style = assets.NormalizeStyleID(style)
	if !assets.HasStyle(style) {
		return ProjectManifest{}, fmt.Errorf("unknown style %q", style)
	}
	manifest, err := s.createProject(name)
	if err != nil {
		return ProjectManifest{}, err
	}
	if err := s.SaveProjectStyle(manifest, style); err != nil {
		return ProjectManifest{}, err
	}
	return manifest, nil
}

func (s *ProjectStore) createProject(name string) (ProjectManifest, error) {
	if err := EnsureRuntimeRoot(s.RuntimeRoot); err != nil {
		return ProjectManifest{}, err
	}
	now := time.Now().UTC()
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Untitled Novel"
	}
	id, err := newProjectID(name, now)
	if err != nil {
		return ProjectManifest{}, err
	}
	root := filepath.Join(s.ProjectsDir(), id)
	manifest := ProjectManifest{
		Version:        manifestVersion,
		ID:             id,
		Name:           name,
		RootDir:        root,
		OutputDir:      filepath.Join(root, "output"),
		CreatedAt:      now,
		UpdatedAt:      now,
		LastAccessedAt: now,
	}
	if err := s.ensureProjectDirs(manifest); err != nil {
		return ProjectManifest{}, err
	}
	if err := writeProjectManifest(manifest); err != nil {
		return ProjectManifest{}, err
	}
	return manifest, nil
}

func (s *ProjectStore) ListProjects() ([]ProjectManifest, error) {
	entries, err := os.ReadDir(s.ProjectsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list projects: %w", err)
	}
	projects := make([]ProjectManifest, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		root := filepath.Join(s.ProjectsDir(), entry.Name())
		manifest, err := readProjectManifest(filepath.Join(root, "project.json"))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		if manifest.DeletedAt != nil {
			continue
		}
		manifest = s.normalizeManifest(root, manifest)
		projects = append(projects, manifest)
	}
	sort.Slice(projects, func(i, j int) bool {
		if !projects[i].LastAccessedAt.Equal(projects[j].LastAccessedAt) {
			return projects[i].LastAccessedAt.After(projects[j].LastAccessedAt)
		}
		if !projects[i].CreatedAt.Equal(projects[j].CreatedAt) {
			return projects[i].CreatedAt.After(projects[j].CreatedAt)
		}
		return projects[i].ID < projects[j].ID
	})
	return projects, nil
}

func (s *ProjectStore) OpenProject(id string) (ProjectManifest, error) {
	if err := validateProjectID(id); err != nil {
		return ProjectManifest{}, err
	}
	root := filepath.Join(s.ProjectsDir(), id)
	manifest, err := readProjectManifest(filepath.Join(root, "project.json"))
	if err != nil {
		return ProjectManifest{}, err
	}
	if manifest.DeletedAt != nil {
		return ProjectManifest{}, os.ErrNotExist
	}
	manifest = s.normalizeManifest(root, manifest)
	now := time.Now().UTC()
	manifest.LastAccessedAt = now
	manifest.UpdatedAt = now
	if err := s.ensureProjectDirs(manifest); err != nil {
		return ProjectManifest{}, err
	}
	if err := writeProjectManifest(manifest); err != nil {
		return ProjectManifest{}, err
	}
	return manifest, nil
}

func (s *ProjectStore) RenameProject(id, name string) (ProjectManifest, error) {
	if err := validateProjectID(strings.TrimSpace(id)); err != nil {
		return ProjectManifest{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return ProjectManifest{}, fmt.Errorf("project name is required")
	}
	root := filepath.Join(s.ProjectsDir(), strings.TrimSpace(id))
	manifest, err := readProjectManifest(filepath.Join(root, "project.json"))
	if err != nil {
		return ProjectManifest{}, err
	}
	if manifest.DeletedAt != nil {
		return ProjectManifest{}, os.ErrNotExist
	}
	manifest = s.normalizeManifest(root, manifest)
	manifest.Name = name
	manifest.UpdatedAt = time.Now().UTC()
	if err := writeProjectManifest(manifest); err != nil {
		return ProjectManifest{}, err
	}
	return manifest, nil
}

func (s *ProjectStore) TrashProject(id string) (ProjectManifest, string, error) {
	id = strings.TrimSpace(id)
	if err := validateProjectID(id); err != nil {
		return ProjectManifest{}, "", err
	}
	root := filepath.Join(s.ProjectsDir(), id)
	manifest, err := readProjectManifest(filepath.Join(root, "project.json"))
	if err != nil {
		return ProjectManifest{}, "", err
	}
	if manifest.DeletedAt != nil {
		return ProjectManifest{}, "", os.ErrNotExist
	}
	manifest = s.normalizeManifest(root, manifest)
	deletedAt := time.Now().UTC()
	manifest.DeletedAt = &deletedAt
	manifest.UpdatedAt = deletedAt
	if err := writeProjectManifest(manifest); err != nil {
		return ProjectManifest{}, "", err
	}

	trashDir := s.ProjectTrashDir()
	if err := os.MkdirAll(trashDir, 0o755); err != nil {
		return ProjectManifest{}, "", fmt.Errorf("create trash dir: %w", err)
	}
	target := s.uniqueTrashProjectPath(trashDir, id, deletedAt)
	if err := os.Rename(root, target); err != nil {
		manifest.DeletedAt = nil
		_ = writeProjectManifest(manifest)
		return ProjectManifest{}, "", fmt.Errorf("move project to trash: %w", err)
	}
	manifest.RootDir = target
	manifest.OutputDir = filepath.Join(target, "output")
	if err := writeProjectManifest(manifest); err != nil {
		return ProjectManifest{}, "", err
	}
	return manifest, target, nil
}

func (s *ProjectStore) ListTrashedProjects() ([]ProjectManifest, error) {
	entries, err := os.ReadDir(s.ProjectTrashDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list project trash: %w", err)
	}
	projects := make([]ProjectManifest, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		root := filepath.Join(s.ProjectTrashDir(), entry.Name())
		manifest, err := readProjectManifest(filepath.Join(root, "project.json"))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		manifest = s.normalizeManifest(root, manifest)
		if manifest.DeletedAt == nil {
			deletedAt := manifest.UpdatedAt
			manifest.DeletedAt = &deletedAt
		}
		projects = append(projects, manifest)
	}
	sort.Slice(projects, func(i, j int) bool {
		left := projects[i].UpdatedAt
		right := projects[j].UpdatedAt
		if projects[i].DeletedAt != nil {
			left = *projects[i].DeletedAt
		}
		if projects[j].DeletedAt != nil {
			right = *projects[j].DeletedAt
		}
		if !left.Equal(right) {
			return left.After(right)
		}
		return projects[i].ID < projects[j].ID
	})
	return projects, nil
}

func (s *ProjectStore) ClearProjectTrash() (int, error) {
	trashDir := s.ProjectTrashDir()
	entries, err := os.ReadDir(trashDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read project trash: %w", err)
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() {
			count++
		}
	}
	if err := removeAllWithRetry(trashDir); err != nil {
		return 0, fmt.Errorf("clear project trash: %w", err)
	}
	return count, nil
}

func (s *ProjectStore) ListTrashProjects() ([]ProjectManifest, error) {
	return s.ListTrashedProjects()
}

func (s *ProjectStore) RestoreTrashProject(id string) (ProjectManifest, error) {
	if err := validateProjectID(strings.TrimSpace(id)); err != nil {
		return ProjectManifest{}, err
	}
	manifest, root, err := s.findTrashedProject(id)
	if err != nil {
		return ProjectManifest{}, err
	}
	target := filepath.Join(s.ProjectsDir(), id)
	if _, err := os.Stat(target); err == nil {
		return ProjectManifest{}, fmt.Errorf("project %s already exists", id)
	} else if !os.IsNotExist(err) {
		return ProjectManifest{}, err
	}
	if err := os.MkdirAll(s.ProjectsDir(), 0o755); err != nil {
		return ProjectManifest{}, fmt.Errorf("create projects dir: %w", err)
	}
	now := time.Now().UTC()
	manifest.RootDir = target
	manifest.OutputDir = filepath.Join(target, "output")
	manifest.DeletedAt = nil
	manifest.UpdatedAt = now
	manifest.LastAccessedAt = now
	if err := os.Rename(root, target); err != nil {
		return ProjectManifest{}, fmt.Errorf("restore project: %w", err)
	}
	if err := s.ensureProjectDirs(manifest); err != nil {
		return ProjectManifest{}, err
	}
	if err := writeProjectManifest(manifest); err != nil {
		return ProjectManifest{}, err
	}
	return manifest, nil
}

func (s *ProjectStore) EmptyTrashProjects() (int, error) {
	return s.ClearProjectTrash()
}

func (s *ProjectStore) findTrashedProject(id string) (ProjectManifest, string, error) {
	entries, err := os.ReadDir(s.ProjectTrashDir())
	if err != nil {
		if os.IsNotExist(err) {
			return ProjectManifest{}, "", os.ErrNotExist
		}
		return ProjectManifest{}, "", fmt.Errorf("list trash projects: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		root := filepath.Join(s.ProjectTrashDir(), entry.Name())
		manifest, err := readProjectManifest(filepath.Join(root, "project.json"))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return ProjectManifest{}, "", err
		}
		if manifest.ID != id {
			continue
		}
		manifest = s.normalizeManifest(root, manifest)
		if manifest.DeletedAt == nil {
			deletedAt := manifest.UpdatedAt
			manifest.DeletedAt = &deletedAt
		}
		manifest.RootDir = root
		manifest.OutputDir = filepath.Join(root, "output")
		return manifest, root, nil
	}
	return ProjectManifest{}, "", os.ErrNotExist
}

func (s *ProjectStore) uniqueTrashProjectPath(trashDir, id string, deletedAt time.Time) string {
	base := filepath.Join(trashDir, fmt.Sprintf("%s-%s", id, deletedAt.Format("20060102150405")))
	path := base
	for i := 2; ; i++ {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return path
		}
		path = fmt.Sprintf("%s-%d", base, i)
	}
}

func removeAllWithRetry(path string) error {
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		err = os.RemoveAll(path)
		if err == nil || os.IsNotExist(err) {
			return nil
		}
		time.Sleep(time.Duration(attempt+1) * 25 * time.Millisecond)
	}
	return err
}

func (s *ProjectStore) OpenProjectHost(cfg bootstrap.Config, bundle assets.Bundle, manifest ProjectManifest) (*host.Host, error) {
	manifest = s.normalizeManifest(manifest.RootDir, manifest)
	if err := s.ensureProjectDirs(manifest); err != nil {
		return nil, err
	}
	projectConfigPath := ProjectConfigPath(manifest)
	projectCfg, found, err := s.loadProjectConfig(manifest)
	if err != nil {
		return nil, err
	}
	if found {
		cfg = bootstrap.MergeConfig(cfg, projectCfg)
	}
	cfg.Style = assets.NormalizeStyleID(cfg.Style)
	bundle = assets.Load(cfg.Style)
	cfg.OutputDir = manifest.OutputDir
	cfg.PersistPath = projectConfigPath
	cfg.PersistProjectOverlay = true
	cfg.PersistProviders = projectOwnedProviders(projectCfg)
	cfg.PersistProjectConfig = &projectCfg
	return host.New(cfg, bundle)
}

func ProjectConfigPath(manifest ProjectManifest) string {
	return filepath.Join(manifest.RootDir, ".ainovel", "config.json")
}

func (s *ProjectStore) loadProjectConfig(manifest ProjectManifest) (bootstrap.Config, bool, error) {
	path := ProjectConfigPath(manifest)
	cfg, err := bootstrap.LoadConfigFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return bootstrap.Config{}, false, nil
		}
		return bootstrap.Config{}, false, fmt.Errorf("load project config %s: %w", path, err)
	}
	return cfg, true, nil
}

func (s *ProjectStore) RefreshProjectProviderReferences(globalCfg bootstrap.Config, originalProvider, provider string) (int, error) {
	originalProvider = strings.TrimSpace(originalProvider)
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return 0, fmt.Errorf("provider is required")
	}
	if originalProvider == "" {
		originalProvider = provider
	}
	projects, err := s.ListProjects()
	if err != nil {
		return 0, err
	}
	updated := 0
	for _, manifest := range projects {
		cfg, found, err := s.loadProjectConfig(manifest)
		if err != nil {
			return updated, err
		}
		if !found {
			continue
		}
		next, changed := refreshProjectProviderConfig(cfg, globalCfg, originalProvider, provider)
		if !changed {
			continue
		}
		if err := bootstrap.SaveConfig(ProjectConfigPath(manifest), next); err != nil {
			return updated, err
		}
		updated++
	}
	return updated, nil
}

func refreshProjectProviderConfig(cfg bootstrap.Config, globalCfg bootstrap.Config, originalProvider, provider string) (bootstrap.Config, bool) {
	originalProvider = strings.TrimSpace(originalProvider)
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return cfg, false
	}
	if originalProvider == "" {
		originalProvider = provider
	}
	next := cloneWebConfig(cfg)
	changed := false
	if originalProvider != provider {
		var renamed bool
		next, renamed = host.RenameProviderInConfig(next, originalProvider, provider)
		changed = changed || renamed
	}
	if refreshProjectProviderDisplayMetadata(&next, globalCfg, provider) {
		changed = true
	}
	return next, changed
}

func refreshProjectProviderDisplayMetadata(cfg *bootstrap.Config, globalCfg bootstrap.Config, provider string) bool {
	provider = strings.TrimSpace(provider)
	if provider == "" || cfg.Providers == nil {
		return false
	}
	pc, ok := cfg.Providers[provider]
	if !ok {
		return false
	}
	globalPC, hasGlobal := globalCfg.Providers[provider]
	globalLabel := ""
	if hasGlobal {
		globalLabel = strings.TrimSpace(globalPC.Label)
	}
	if providerHasPrivateConfig(pc) {
		if pc.Label == globalLabel {
			return false
		}
		pc.Label = globalLabel
		cfg.Providers[provider] = pc
		return true
	}
	safe := bootstrap.ProviderConfig{Label: globalLabel, Models: append([]string(nil), pc.Models...)}
	if reflect.DeepEqual(pc, safe) {
		return false
	}
	cfg.Providers[provider] = safe
	return true
}

func (s *ProjectStore) SaveProjectStyle(manifest ProjectManifest, style string) error {
	style = assets.NormalizeStyleID(style)
	if !assets.HasStyle(style) {
		return fmt.Errorf("unknown style %q", style)
	}
	cfg, found, err := s.loadProjectConfig(manifest)
	if err != nil {
		return err
	}
	if !found {
		cfg = bootstrap.Config{}
	}
	cfg.Style = style
	return bootstrap.SaveConfig(ProjectConfigPath(manifest), cfg)
}

func projectOwnedProviders(cfg bootstrap.Config) map[string]bool {
	if len(cfg.Providers) == 0 {
		return nil
	}
	out := make(map[string]bool, len(cfg.Providers))
	for name, pc := range cfg.Providers {
		if providerHasPrivateConfig(pc) {
			out[name] = true
		}
	}
	return out
}

func providerHasPrivateConfig(pc bootstrap.ProviderConfig) bool {
	return pc.Type != "" ||
		pc.Auth != "" ||
		pc.AccountID != "" ||
		pc.API != "" ||
		pc.APIKey != "" ||
		pc.BaseURL != "" ||
		len(pc.ExtraBody) > 0 ||
		len(pc.Extra) > 0
}

func (s *ProjectStore) normalizeManifest(root string, manifest ProjectManifest) ProjectManifest {
	if manifest.Version == 0 {
		manifest.Version = manifestVersion
	}
	if manifest.RootDir == "" {
		manifest.RootDir = root
	}
	if manifest.OutputDir == "" {
		manifest.OutputDir = filepath.Join(manifest.RootDir, "output")
	}
	if manifest.ID == "" {
		manifest.ID = filepath.Base(manifest.RootDir)
	}
	if manifest.Name == "" {
		manifest.Name = manifest.ID
	}
	return manifest
}

func (s *ProjectStore) ensureProjectDirs(manifest ProjectManifest) error {
	for _, dir := range []string{
		manifest.RootDir,
		filepath.Join(manifest.RootDir, "simulate"),
		filepath.Join(manifest.RootDir, "uploads"),
		filepath.Join(manifest.RootDir, "uploads", "adaptation"),
		filepath.Join(manifest.RootDir, "uploads", "import"),
		filepath.Join(manifest.RootDir, "profiles"),
		filepath.Join(manifest.RootDir, "profiles", "imported"),
		filepath.Join(manifest.RootDir, "exports"),
		manifest.OutputDir,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create project dir %s: %w", dir, err)
		}
	}
	return nil
}

func writeProjectManifest(manifest ProjectManifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(manifest.RootDir, "project.json")
	tmp, err := os.CreateTemp(filepath.Dir(path), "project.json.tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func readProjectManifest(path string) (ProjectManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ProjectManifest{}, err
	}
	var manifest ProjectManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return ProjectManifest{}, fmt.Errorf("parse project manifest %s: %w", path, err)
	}
	if err := validateProjectID(manifest.ID); err != nil {
		return ProjectManifest{}, fmt.Errorf("project manifest %s has invalid id: %w", path, err)
	}
	return manifest, nil
}

func newProjectID(name string, now time.Time) (string, error) {
	slug := slugify(name)
	if slug == "" {
		slug = "project"
	}
	var suffix [3]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%s-%s", slug, now.Format("20060102150405"), hex.EncodeToString(suffix[:])), nil
}

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func validateProjectID(id string) error {
	if id == "" {
		return fmt.Errorf("project id is required")
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return fmt.Errorf("project id %q contains invalid character %q", id, r)
	}
	if strings.Contains(id, "..") {
		return fmt.Errorf("project id %q must not contain path traversal", id)
	}
	return nil
}
