package host

import (
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host/adapt"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestParseSuggestionsStripsListMarkersAndCapsResults(t *testing.T) {
	got := parseSuggestions(`
<uggestions>
- 增强女主线
* 改成双主角
1. 加一条悬疑暗线
2. 这一条超过上限
</suggestions>
`)
	want := []string{"增强女主线", "改成双主角", "加一条悬疑暗线"}
	if len(got) != len(want) {
		t.Fatalf("suggestions length = %d, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("suggestion[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestParseSuggestionJudgeResponseCleansAndCapsResults(t *testing.T) {
	got := parseSuggestionJudgeResponse("```json\n" + `{
		"suggestions": [
			"- 保持黑暗基调",
			"1. 改成纯爱方向",
			"保持黑暗基调",
			"这一条故意写得非常非常非常非常非常非常非常长超过按钮限制"
		]
	}` + "\n```")
	want := []string{"保持黑暗基调", "改成纯爱方向"}
	if len(got) != len(want) {
		t.Fatalf("suggestions length = %d, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("suggestion[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestAdaptSystemPromptIncludesLateDossierRelationshipRisks(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	sources := make([]domain.AdaptationSource, 0, 40)
	for chapter := 1; chapter <= 40; chapter++ {
		sources = append(sources, domain.AdaptationSource{
			Chapter: chapter,
			Title:   "Source",
			SHA256:  store.TextSHA256(strings.Repeat("x", chapter)),
			Path:    store.SourceChapterRelPath(chapter),
			Runes:   chapter,
		})
	}
	manifest := domain.AdaptationSourceManifest{
		SourcePath:   "source.txt",
		ChapterCount: len(sources),
		Chapters:     sources,
	}
	if err := st.Adaptation.SaveSourceManifest(manifest); err != nil {
		t.Fatalf("SaveSourceManifest: %v", err)
	}
	dossier := domain.AdaptationCoCreateDossier{
		Version:            1,
		PromptVersion:      adapt.CoCreateDossierPromptVersion,
		SourcePath:         manifest.SourcePath,
		SourceChapterCount: manifest.ChapterCount,
		SourceSignature:    store.AdaptationSourceSignature(manifest),
		BatchSize:          adapt.CoCreateDossierBatchSize,
		Batches: []domain.AdaptationCoCreateDossierBatch{
			{Index: 1, SourceFrom: 1, SourceTo: 40, SourceSignature: "batch"},
		},
		AmbiguityRisks: []domain.AdaptationRelationshipRisk{
			{
				Chapters:   []int{35},
				Characters: []string{"男主", "小狐狸"},
				Risk:       "小狐狸向男主表达喜欢，容易形成后宫暧昧感。",
				Evidence:   "第35章小狐狸明确说喜欢男主。",
				Suggestion: "单女主改编中改为普通感激或阵营依赖。",
			},
		},
	}
	if err := st.Adaptation.SaveCoCreateDossier(dossier); err != nil {
		t.Fatalf("SaveCoCreateDossier: %v", err)
	}

	prompt := adaptSystemPrompt(st)
	for _, want := range []string{"第 35 章", "小狐狸向男主表达喜欢", "普通感激或阵营依赖"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("adapt prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "其余 10 章") {
		t.Fatalf("adapt prompt should not use first-30 snapshot fallback:\n%s", prompt)
	}
}

func TestRejectIncompleteCoCreateXML(t *testing.T) {
	for _, raw := range []string{
		"<reply>ok</reply><draft>half",
		"<reply>ok</reply><draft>## plan</draft><ready>true",
		"<reply>ok</reply><draft>## plan</draft><ready>true</ready><suggestions>- x",
	} {
		if err := rejectIncompleteCoCreateXML(raw); err == nil {
			t.Fatalf("rejectIncompleteCoCreateXML(%q) = nil, want error", raw)
		}
	}
	if err := rejectIncompleteCoCreateXML("plain natural language"); err != nil {
		t.Fatalf("plain fallback should remain allowed: %v", err)
	}
}
