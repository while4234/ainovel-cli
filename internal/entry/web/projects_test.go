package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
)

func TestProjectManifestCreateListOpenTouches(t *testing.T) {
	store := NewProjectStore(filepath.Join(t.TempDir(), "novels"))

	created, err := store.CreateProject("My Test Novel")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if created.ID == "" || created.Name != "My Test Novel" {
		t.Fatalf("created manifest mismatch: %+v", created)
	}
	for _, dir := range []string{
		created.RootDir,
		filepath.Join(created.RootDir, "simulate"),
		filepath.Join(created.RootDir, "uploads"),
		filepath.Join(created.RootDir, "uploads", "adaptation"),
		filepath.Join(created.RootDir, "profiles"),
		created.OutputDir,
	} {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Fatalf("expected project dir %s: info=%v err=%v", dir, info, err)
		}
	}
	if _, err := os.Stat(filepath.Join(created.RootDir, "project.json")); err != nil {
		t.Fatalf("project manifest not written: %v", err)
	}

	projects, err := store.ListProjects()
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 1 || projects[0].ID != created.ID {
		t.Fatalf("projects = %+v, want created project", projects)
	}

	time.Sleep(10 * time.Millisecond)
	opened, err := store.OpenProject(created.ID)
	if err != nil {
		t.Fatalf("OpenProject: %v", err)
	}
	if !opened.LastAccessedAt.After(created.LastAccessedAt) {
		t.Fatalf("LastAccessedAt was not updated: before=%s after=%s", created.LastAccessedAt, opened.LastAccessedAt)
	}
}

func TestOpenProjectHostUsesProjectOutputDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	store := NewProjectStore(filepath.Join(t.TempDir(), "novels"))
	manifest, err := store.CreateProject("Output Isolation")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	cfg := testWebConfig(t)

	h, err := store.OpenProjectHost(cfg, assets.Load("default"), manifest)
	if err != nil {
		t.Fatalf("OpenProjectHost: %v", err)
	}
	defer h.Close()
	if h.Dir() != manifest.OutputDir {
		t.Fatalf("host dir = %q, want project output %q", h.Dir(), manifest.OutputDir)
	}
	if info, err := os.Stat(filepath.Join(manifest.OutputDir, "meta")); err != nil || !info.IsDir() {
		t.Fatalf("host store did not initialize project output: info=%v err=%v", info, err)
	}
}

func TestOpenProjectHostUsesProjectModelOverrideAndGlobalFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	store := NewProjectStore(filepath.Join(t.TempDir(), "novels"))
	projectOverride, err := store.CreateProject("Project Override")
	if err != nil {
		t.Fatalf("CreateProject override: %v", err)
	}
	projectFallback, err := store.CreateProject("Project Fallback")
	if err != nil {
		t.Fatalf("CreateProject fallback: %v", err)
	}
	base := testWebConfig(t)
	base.ModelName = "global-model"
	base.Providers["openai"] = bootstrap.ProviderConfig{
		Type:   "openai",
		APIKey: "sk-global",
		Models: []string{"global-model", "project-model"},
	}
	if err := bootstrap.SaveConfig(ProjectConfigPath(projectOverride), bootstrap.Config{
		Provider:  "openai",
		ModelName: "project-model",
		Providers: map[string]bootstrap.ProviderConfig{
			"openai": {Models: []string{"project-model"}},
		},
	}); err != nil {
		t.Fatalf("write project config: %v", err)
	}

	overrideHost, err := store.OpenProjectHost(base, assets.Load("default"), projectOverride)
	if err != nil {
		t.Fatalf("OpenProjectHost override: %v", err)
	}
	defer overrideHost.Close()
	if snap := overrideHost.Snapshot(); snap.ModelName != "project-model" {
		t.Fatalf("override model = %q, want project-model", snap.ModelName)
	}

	fallbackHost, err := store.OpenProjectHost(base, assets.Load("default"), projectFallback)
	if err != nil {
		t.Fatalf("OpenProjectHost fallback: %v", err)
	}
	defer fallbackHost.Close()
	if snap := fallbackHost.Snapshot(); snap.ModelName != "global-model" {
		t.Fatalf("fallback model = %q, want global-model", snap.ModelName)
	}
}

func TestProjectModelPersistenceWritesSecretFreeOverlay(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	store := NewProjectStore(filepath.Join(t.TempDir(), "novels"))
	manifest, err := store.CreateProject("Secret Free Overlay")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	cfg := testWebConfig(t)
	cfg.ModelName = "global-model"
	cfg.Providers["openai"] = bootstrap.ProviderConfig{
		Type:   "openai",
		APIKey: "sk-global-secret",
		Models: []string{"global-model", "writer-model"},
	}

	h, err := store.OpenProjectHost(cfg, assets.Load("default"), manifest)
	if err != nil {
		t.Fatalf("OpenProjectHost: %v", err)
	}
	defer h.Close()
	if err := h.SwitchModel("writer", "openai", "writer-model"); err != nil {
		t.Fatalf("SwitchModel: %v", err)
	}
	data, err := os.ReadFile(ProjectConfigPath(manifest))
	if err != nil {
		t.Fatalf("read project config: %v", err)
	}
	text := string(data)
	if strings.Contains(text, "sk-global-secret") || strings.Contains(text, "api_key") {
		t.Fatalf("project overlay leaked inherited secret: %s", text)
	}
	if !strings.Contains(text, `"writer"`) || !strings.Contains(text, `"writer-model"`) {
		t.Fatalf("project overlay missing writer route: %s", text)
	}
}

func TestProjectRoleSwitchPersistsOnlyExplicitRoleOverlay(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	store := NewProjectStore(filepath.Join(t.TempDir(), "novels"))
	manifest, err := store.CreateProject("Role Overlay")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	base := testWebConfig(t)
	base.ModelName = "global-a"
	base.ReasoningEffort = "high"
	base.Providers["openai"] = bootstrap.ProviderConfig{
		Type:   "openai",
		APIKey: "sk-global-secret",
		Models: []string{"global-a", "global-b", "writer-global", "writer-project", "editor-global"},
	}
	base.Roles = map[string]bootstrap.RoleConfig{
		"writer": {Provider: "openai", Model: "writer-global", ReasoningEffort: "medium"},
		"editor": {Provider: "openai", Model: "editor-global", ReasoningEffort: "low"},
	}

	h, err := store.OpenProjectHost(base, assets.Load("default"), manifest)
	if err != nil {
		t.Fatalf("OpenProjectHost: %v", err)
	}
	if err := h.SwitchModel("writer", "openai", "writer-project"); err != nil {
		t.Fatalf("SwitchModel: %v", err)
	}
	h.Close()

	overlay := readProjectOverlay(t, manifest)
	if overlay.Provider != "" || overlay.ModelName != "" {
		t.Fatalf("concrete role switch persisted inherited default route: provider=%q model=%q", overlay.Provider, overlay.ModelName)
	}
	if overlay.ReasoningEffort != "" {
		t.Fatalf("concrete role switch persisted inherited reasoning default: %q", overlay.ReasoningEffort)
	}
	if len(overlay.Roles) != 1 {
		t.Fatalf("overlay roles = %+v, want only writer", overlay.Roles)
	}
	writer := overlay.Roles["writer"]
	if writer.Provider != "openai" || writer.Model != "writer-project" || writer.ReasoningEffort != "" {
		t.Fatalf("writer overlay = %+v, want only explicit route", writer)
	}
	if _, ok := overlay.Roles["editor"]; ok {
		t.Fatalf("overlay persisted unrelated inherited editor role: %+v", overlay.Roles)
	}
	if pc := overlay.Providers["openai"]; pc.APIKey != "" || pc.Type != "" || !containsString(pc.Models, "writer-project") || containsString(pc.Models, "global-a") {
		t.Fatalf("inherited provider overlay = %+v, want safe selected model metadata only", pc)
	}

	changedGlobal := base
	changedGlobal.ModelName = "global-b"
	reopened, err := store.OpenProjectHost(changedGlobal, assets.Load("default"), manifest)
	if err != nil {
		t.Fatalf("reopen with changed global default: %v", err)
	}
	defer reopened.Close()
	if snap := reopened.Snapshot(); snap.ModelName != "global-b" {
		t.Fatalf("default route after reopen = %q, want changed global default", snap.ModelName)
	}
	provider, model, explicit := reopened.CurrentModelSelection("architect")
	if explicit || provider != "openai" || model != "global-b" {
		t.Fatalf("unset architect route = %s/%s explicit=%v, want inherited changed global default", provider, model, explicit)
	}
}

func TestProjectRoleThinkingPersistsOnlyRoleScopeAndFallsBack(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	store := NewProjectStore(filepath.Join(t.TempDir(), "novels"))
	manifest, err := store.CreateProject("Thinking Overlay")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	base := testWebConfig(t)
	base.ModelName = "gpt-5"
	base.ReasoningEffort = "high"
	base.Providers["openai"] = bootstrap.ProviderConfig{
		Type:   "openai",
		APIKey: "sk-test",
		Models: []string{"gpt-5"},
	}

	h, err := store.OpenProjectHost(base, assets.Load("default"), manifest)
	if err != nil {
		t.Fatalf("OpenProjectHost: %v", err)
	}
	if err := h.SetRoleThinking("writer", "low"); err != nil {
		t.Fatalf("SetRoleThinking: %v", err)
	}
	h.Close()

	overlay := readProjectOverlay(t, manifest)
	if overlay.Provider != "" || overlay.ModelName != "" || overlay.ReasoningEffort != "" {
		t.Fatalf("role thinking persisted unrelated defaults: %+v", overlay)
	}
	if len(overlay.Roles) != 1 {
		t.Fatalf("roles = %+v, want only writer thinking", overlay.Roles)
	}
	writer := overlay.Roles["writer"]
	if writer.Provider != "" || writer.Model != "" || writer.ReasoningEffort != "low" {
		t.Fatalf("writer thinking overlay = %+v, want only reasoning_effort", writer)
	}

	changedGlobal := base
	changedGlobal.ReasoningEffort = "medium"
	reopened, err := store.OpenProjectHost(changedGlobal, assets.Load("default"), manifest)
	if err != nil {
		t.Fatalf("reopen with changed global thinking: %v", err)
	}
	defer reopened.Close()
	if got := reopened.CurrentThinking("writer"); got != "low" {
		t.Fatalf("writer thinking = %q, want project override low", got)
	}
	if got := reopened.CurrentThinking("editor"); got != "medium" {
		t.Fatalf("editor thinking = %q, want changed global fallback medium", got)
	}
}

func TestProjectOwnedProviderSecretPreservedWhileInheritedProviderRedacted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	store := NewProjectStore(filepath.Join(t.TempDir(), "novels"))
	manifest, err := store.CreateProject("Owned Provider Overlay")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := bootstrap.SaveConfig(ProjectConfigPath(manifest), bootstrap.Config{
		Providers: map[string]bootstrap.ProviderConfig{
			"project-openai": {
				Type:    "openai",
				API:     "chat",
				APIKey:  "sk-project-owned",
				BaseURL: "https://project.example/v1",
				Models:  []string{"project-model"},
			},
		},
	}); err != nil {
		t.Fatalf("write project config: %v", err)
	}
	base := testWebConfig(t)
	base.ModelName = "global-model"
	base.Providers["openai"] = bootstrap.ProviderConfig{
		Type:   "openai",
		APIKey: "sk-global-secret",
		Models: []string{"global-model", "global-editor"},
	}

	h, err := store.OpenProjectHost(base, assets.Load("default"), manifest)
	if err != nil {
		t.Fatalf("OpenProjectHost: %v", err)
	}
	if err := h.SwitchModel("writer", "project-openai", "project-model"); err != nil {
		t.Fatalf("SwitchModel project provider: %v", err)
	}
	if err := h.SwitchModel("editor", "openai", "global-editor"); err != nil {
		t.Fatalf("SwitchModel inherited provider: %v", err)
	}
	h.Close()

	overlay := readProjectOverlay(t, manifest)
	projectProvider := overlay.Providers["project-openai"]
	if projectProvider.APIKey != "sk-project-owned" || projectProvider.BaseURL != "https://project.example/v1" || projectProvider.Type != "openai" {
		t.Fatalf("project-owned provider was not preserved: %+v", projectProvider)
	}
	inheritedProvider := overlay.Providers["openai"]
	if inheritedProvider.APIKey != "" || inheritedProvider.Type != "" || inheritedProvider.BaseURL != "" {
		t.Fatalf("inherited provider leaked private config: %+v", inheritedProvider)
	}
	if !containsString(inheritedProvider.Models, "global-editor") {
		t.Fatalf("inherited provider missing safe selected model metadata: %+v", inheritedProvider)
	}
	data, err := os.ReadFile(ProjectConfigPath(manifest))
	if err != nil {
		t.Fatalf("read overlay: %v", err)
	}
	if strings.Contains(string(data), "sk-global-secret") {
		t.Fatalf("project overlay leaked inherited global secret: %s", string(data))
	}
}

func readProjectOverlay(t *testing.T, manifest ProjectManifest) bootstrap.Config {
	t.Helper()
	cfg, err := bootstrap.LoadConfigFile(ProjectConfigPath(manifest))
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	return cfg
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
