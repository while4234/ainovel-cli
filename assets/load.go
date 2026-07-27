package assets

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/voocel/ainovel-cli/internal/globalprompt"
	"github.com/voocel/ainovel-cli/internal/tools"
)

//go:embed prompts/*.md
var promptsFS embed.FS

//go:embed references
var referencesFS embed.FS

//go:embed styles/*.md
var stylesFS embed.FS

// Prompts 表示嵌入的提示词集合。
type Prompts struct {
	Coordinator                 string
	ArchitectShort              string
	ArchitectLong               string
	Character                   string
	Writer                      string
	Editor                      string
	ImportFoundation            string
	ImportFoundationMerge       string
	ImportAnalyzer              string
	AdaptationPlanner           string
	SimulationSource            string
	SimulationMerge             string
	NormalManuscriptPolish      string
	NormalManuscriptRewrite     string
	NormalManuscriptAudit       string
	AdaptationManuscriptPolish  string
	AdaptationManuscriptRewrite string
	AdaptationManuscriptAudit   string
	NormalExpansionPlanner      string
	AdaptationExpansionPlanner  string
}

// Bundle 表示运行所需的静态资源集合。
type Bundle struct {
	References tools.References
	Prompts    Prompts
	Styles     map[string]string
}

// StyleDescriptor 是 Web/API 展示用的写作风格条目。
type StyleDescriptor struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// Load 返回指定风格对应的资源集合。
func Load(style string) Bundle {
	style = NormalizeStyleID(style)
	return Bundle{
		References: loadReferences(style),
		Prompts:    loadPrompts(),
		Styles:     loadStyles(),
	}
}

// NormalizeStyleID 返回配置中实际使用的 style id。
func NormalizeStyleID(style string) string {
	style = strings.TrimSpace(style)
	if style == "" {
		return "default"
	}
	return style
}

// StyleCatalog 返回嵌入的写作风格目录，显示名来自 Markdown 第一行标题。
func StyleCatalog() []StyleDescriptor {
	return styleCatalogFromFS(stylesFS, "styles")
}

// HasStyle 判断 style id 是否存在于嵌入资源中。
func HasStyle(style string) bool {
	style = NormalizeStyleID(style)
	for _, item := range StyleCatalog() {
		if item.ID == style {
			return true
		}
	}
	return false
}

func loadReferences(style string) tools.References {
	style = NormalizeStyleID(style)
	refs := tools.References{
		ChapterGuide:                    mustRead(referencesFS, "references/chapter-guide.md"),
		HookTechniques:                  mustRead(referencesFS, "references/hook-techniques.md"),
		QualityChecklist:                mustRead(referencesFS, "references/quality-checklist.md"),
		OutlineTemplate:                 mustRead(referencesFS, "references/outline-template.md"),
		CharacterTemplate:               mustRead(referencesFS, "references/character-template.md"),
		ChapterTemplate:                 mustRead(referencesFS, "references/chapter-template.md"),
		Consistency:                     mustRead(referencesFS, "references/consistency.md"),
		ContentExpansion:                mustRead(referencesFS, "references/content-expansion.md"),
		DialogueWriting:                 mustRead(referencesFS, "references/dialogue-writing.md"),
		LongformPlanning:                mustRead(referencesFS, "references/longform-planning.md"),
		Differentiation:                 mustRead(referencesFS, "references/differentiation.md"),
		AntiAITone:                      mustRead(referencesFS, "references/anti-ai-tone.md"),
		AdaptationWriter:                mustRead(promptsFS, "prompts/writer-adaptation.md"),
		AdaptationEditorPreserveDetails: mustRead(promptsFS, "prompts/editor-adaptation-preserve_details.md"),
		AdaptationEditorFullRewrite:     mustRead(promptsFS, "prompts/editor-adaptation-full_rewrite.md"),
	}
	if style != "" && style != "default" {
		genreDir := "references/genres/" + style + "/"
		if data, err := referencesFS.ReadFile(genreDir + "style-references.md"); err == nil {
			refs.StyleReference = string(data)
		}
		if data, err := referencesFS.ReadFile(genreDir + "arc-templates.md"); err == nil {
			refs.ArcTemplates = string(data)
		}
	}
	return refs
}

func loadPrompts() Prompts {
	return Prompts{
		Coordinator:                 loadRolePrompt("prompts/coordinator.md", "coordinator"),
		ArchitectShort:              loadRolePrompt("prompts/architect-short.md", "architect"),
		ArchitectLong:               loadRolePrompt("prompts/architect-long.md", "architect"),
		Character:                   loadRolePrompt("prompts/character.md", "character"),
		Writer:                      loadRolePrompt("prompts/writer.md", "writer"),
		Editor:                      loadRolePrompt("prompts/editor.md", "editor"),
		ImportFoundation:            loadSystemPrompt("prompts/import-foundation.md"),
		ImportFoundationMerge:       loadSystemPrompt("prompts/import-foundation-merge.md"),
		ImportAnalyzer:              loadSystemPrompt("prompts/import-chapter-analyzer.md"),
		AdaptationPlanner:           loadSystemPrompt("prompts/adaptation-planner.md"),
		SimulationSource:            loadSystemPrompt("prompts/simulation-source.md"),
		SimulationMerge:             loadSystemPrompt("prompts/simulation-merge.md"),
		NormalManuscriptPolish:      loadSystemPrompt("prompts/manuscript-normal-polish.md"),
		NormalManuscriptRewrite:     loadSystemPrompt("prompts/manuscript-normal-rewrite.md"),
		NormalManuscriptAudit:       loadSystemPrompt("prompts/manuscript-normal-audit.md"),
		AdaptationManuscriptPolish:  loadSystemPrompt("prompts/manuscript-adaptation-polish.md"),
		AdaptationManuscriptRewrite: loadSystemPrompt("prompts/manuscript-adaptation-rewrite.md"),
		AdaptationManuscriptAudit:   loadSystemPrompt("prompts/manuscript-adaptation-audit.md"),
		NormalExpansionPlanner:      loadSystemPrompt("prompts/normal-expansion-planner.md"),
		AdaptationExpansionPlanner:  loadSystemPrompt("prompts/adaptation-expansion-planner.md"),
	}
}

func loadRolePrompt(path, role string) string {
	return globalprompt.Apply(withSimulationGuidance(mustRead(promptsFS, path), role))
}

func loadSystemPrompt(path string) string {
	return globalprompt.Apply(mustRead(promptsFS, path))
}

func withSimulationGuidance(prompt, role string) string {
	return prompt + "\n\n" + strings.ReplaceAll(simulationGuidance, "{{role}}", role)
}

const simulationGuidance = `## 仿写画像

当 novel_context 返回 simulation_profile 时，必须把它视为当前作品的仿写方向约束。{{role}} 应读取其中的 style、lexicon、plot_design、hook_design、pacing_density、reader_engagement 和 role_guidance。

当 simulation_profile.mode == "reinforced" 或 novel_context.simulation_mode == "reinforced" 时，说明用户选择了强化仿写。强化仿写是 opt-in 的风格/结构信号；在不违背用户显式要求时，{{role}} 要更主动地把画像转化为规划、写作或审阅标准。

强化仿写 guidance：
- Architect：更主动应用 plot_design、hook_design、pacing_density、reader_engagement，落实到结构、悬念、章节钩子、信息释放、反转和回收。
- Writer：更主动应用 style、lexicon、pacing_density，落实到叙事声音、句式节奏、意象/词汇倾向、场景密度和段落推进。
- Editor：检查画像漂移，并额外审查复制、人物/地名/专有设定和固定桥段风险。

使用原则：只使用 compact/synthesis simulation profile 作为风格和结构信号；不要索取或依赖 source_reports、raw simulate source text；不要复制原文句子、人物、地名、专有设定或固定桥段。若 simulation_profile 与用户显式要求冲突，优先服从用户要求。`

func loadStyles() map[string]string {
	styles := make(map[string]string)
	entries, err := stylesFS.ReadDir("styles")
	if err != nil {
		return styles
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".md")
		data, err := stylesFS.ReadFile("styles/" + e.Name())
		if err != nil {
			continue
		}
		styles[name] = string(data)
	}
	return styles
}

func styleCatalogFromFS(fsys fs.FS, dir string) []StyleDescriptor {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil
	}
	styles := make([]StyleDescriptor, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".md") {
			continue
		}
		id := entry.Name()[:len(entry.Name())-len(".md")]
		data, err := fs.ReadFile(fsys, dir+"/"+entry.Name())
		if err != nil {
			continue
		}
		styles = append(styles, StyleDescriptor{
			ID:    id,
			Label: styleLabel(id, string(data)),
		})
	}
	sort.Slice(styles, func(i, j int) bool {
		if styles[i].Label != styles[j].Label {
			return styles[i].Label < styles[j].Label
		}
		return styles[i].ID < styles[j].ID
	})
	return styles
}

func styleLabel(id, content string) string {
	firstLine := content
	if idx := strings.IndexAny(firstLine, "\r\n"); idx >= 0 {
		firstLine = firstLine[:idx]
	}
	firstLine = strings.TrimPrefix(firstLine, "\ufeff")
	label := strings.TrimSpace(firstLine)
	label = strings.TrimLeft(label, "#")
	label = strings.TrimSpace(label)
	if label == "" {
		return id
	}
	return label
}

func mustRead(fs embed.FS, path string) string {
	data, err := fs.ReadFile(path)
	if err != nil {
		panic(fmt.Sprintf("embed read %s: %v", path, err))
	}
	return string(data)
}
