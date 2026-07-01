package web

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/host"
)

const manifestVersion = 1

type ProjectManifest struct {
	Version        int       `json:"version"`
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	RootDir        string    `json:"root_dir"`
	OutputDir      string    `json:"output_dir"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	LastAccessedAt time.Time `json:"last_accessed_at"`
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

func (s *ProjectStore) CreateProject(name string) (ProjectManifest, error) {
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

func (s *ProjectStore) OpenProjectHost(cfg bootstrap.Config, bundle assets.Bundle, manifest ProjectManifest) (*host.Host, error) {
	manifest = s.normalizeManifest(manifest.RootDir, manifest)
	if err := s.ensureProjectDirs(manifest); err != nil {
		return nil, err
	}
	cfg.OutputDir = manifest.OutputDir
	return host.New(cfg, bundle)
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
		filepath.Join(manifest.RootDir, "profiles"),
		filepath.Join(manifest.RootDir, "profiles", "imported"),
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
