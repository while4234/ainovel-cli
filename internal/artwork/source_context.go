package artwork

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

const (
	maxSourceFragments     = 64
	maxSourceFragmentBytes = 8 * 1024
	maxSourceContentBytes  = 32 * 1024
)

// BuildSourceSnapshot reads only already-published AINovel artifacts. Missing
// summaries are represented by deterministic fallbacks and are never created.
func BuildSourceSnapshot(st *storepkg.Store, workType WorkType, scope, scopeID, templateVersion string) (SourceSnapshot, error) {
	if st == nil {
		return SourceSnapshot{}, fmt.Errorf("%w: project store is required", ErrSourceUnavailable)
	}
	scope = strings.ToLower(strings.TrimSpace(scope))
	scopeID = strings.TrimSpace(scopeID)
	if templateVersion = strings.TrimSpace(templateVersion); templateVersion == "" {
		templateVersion = ArtworkPromptTemplateVersion
	}

	builder := sourceBuilder{store: st}
	var fragments []SourceFragment
	var err error
	switch scope {
	case "project":
		if scopeID != "" || (workType != WorkTypeCover && workType != WorkTypeIllustration) {
			return SourceSnapshot{}, fmt.Errorf("%w: project scope is invalid for %s", ErrSourceUnavailable, workType)
		}
		fragments, err = builder.book()
	case "volume":
		if workType != WorkTypeIllustration {
			return SourceSnapshot{}, fmt.Errorf("%w: volume scope is invalid for %s", ErrSourceUnavailable, workType)
		}
		fragments, err = builder.volume(scopeID)
	case "chapter":
		if workType != WorkTypeIllustration {
			return SourceSnapshot{}, fmt.Errorf("%w: chapter scope is invalid for %s", ErrSourceUnavailable, workType)
		}
		fragments, err = builder.chapter(scopeID)
	case "character":
		if workType != WorkTypeCharacterPortrait {
			return SourceSnapshot{}, fmt.Errorf("%w: character scope is invalid for %s", ErrSourceUnavailable, workType)
		}
		fragments, err = builder.character(scopeID)
	default:
		return SourceSnapshot{}, fmt.Errorf("%w: unsupported source scope %q", ErrSourceUnavailable, scope)
	}
	if err != nil {
		return SourceSnapshot{}, err
	}
	return NewSourceSnapshot(workType, scope, scopeID, templateVersion, fragments)
}

// NewSourceSnapshot bounds exact source fragments before hashing them. The
// digest covers only the exact text that can be sent to the model.
func NewSourceSnapshot(workType WorkType, scope, scopeID, templateVersion string, fragments []SourceFragment) (SourceSnapshot, error) {
	snapshot := SourceSnapshot{
		SchemaVersion: ArtworkSourceSchemaVersion,
		WorkType:      workType, Scope: strings.ToLower(strings.TrimSpace(scope)),
		ScopeID: strings.TrimSpace(scopeID), TemplateVersion: strings.TrimSpace(templateVersion),
	}
	if snapshot.TemplateVersion == "" {
		return SourceSnapshot{}, errors.New("artwork prompt template version is required")
	}
	remaining := maxSourceContentBytes
	for _, fragment := range fragments {
		if len(snapshot.Fragments) == maxSourceFragments || remaining <= 0 {
			break
		}
		fragment.Kind = strings.TrimSpace(fragment.Kind)
		fragment.ID = strings.TrimSpace(fragment.ID)
		fragment.Label = strings.TrimSpace(fragment.Label)
		fragment.Content = strings.TrimSpace(fragment.Content)
		if fragment.Kind == "" || fragment.ID == "" || fragment.Content == "" {
			continue
		}
		limit := min(maxSourceFragmentBytes, remaining)
		var clipped bool
		fragment.Content, clipped = clipUTF8HeadTail(fragment.Content, limit)
		fragment.Truncated = fragment.Truncated || clipped
		remaining -= len([]byte(fragment.Content))
		snapshot.Fragments = append(snapshot.Fragments, fragment)
	}
	if len(snapshot.Fragments) == 0 {
		return SourceSnapshot{}, fmt.Errorf("%w: selected scope has no published source", ErrSourceUnavailable)
	}
	digest, err := fingerprint(struct {
		SchemaVersion   int
		WorkType        WorkType
		Scope           string
		ScopeID         string
		TemplateVersion string
		Fragments       []SourceFragment
	}{snapshot.SchemaVersion, snapshot.WorkType, snapshot.Scope, snapshot.ScopeID, snapshot.TemplateVersion, snapshot.Fragments})
	if err != nil {
		return SourceSnapshot{}, err
	}
	snapshot.Digest = digest
	return snapshot, nil
}

func validateSourceSnapshot(snapshot SourceSnapshot) error {
	if snapshot.SchemaVersion != ArtworkSourceSchemaVersion || snapshot.Digest == "" {
		return errors.New("artwork source snapshot schema is invalid")
	}
	rebuilt, err := NewSourceSnapshot(snapshot.WorkType, snapshot.Scope, snapshot.ScopeID, snapshot.TemplateVersion, snapshot.Fragments)
	if err != nil {
		return err
	}
	if rebuilt.Digest != snapshot.Digest || !reflect.DeepEqual(rebuilt.Fragments, snapshot.Fragments) {
		return errors.New("artwork source snapshot digest is invalid")
	}
	return nil
}

func (d Draft) PublicWithFreshness(snapshot SourceSnapshot) DraftView {
	view := d.Public()
	if d.PromptSource != PromptSourceAI || d.SourceSignature == "" {
		return view
	}
	view.CurrentSourceSignature = snapshot.Digest
	view.IsStale = snapshot.Digest != d.SourceSignature
	view.SourceStatus = "current"
	if view.IsStale {
		view.SourceStatus = "stale"
		if d.ConfirmedSignature == snapshot.Digest {
			view.SourceStatus = "confirmed"
			view.IsStale = false
		}
	}
	return view
}

type sourceBuilder struct {
	store *storepkg.Store
}

func (b sourceBuilder) book() ([]SourceFragment, error) {
	foundation, err := b.store.Foundation.Load()
	if err != nil {
		return nil, fmt.Errorf("load story foundation for artwork: %w", err)
	}
	volumes, outline, err := b.formalOutline()
	if err != nil {
		return nil, err
	}
	fragments := make([]SourceFragment, 0, 32)
	fragments = appendFoundationFragment(fragments, foundation)
	if len(volumes) > 0 {
		fragments = appendJSONFragment(fragments, "outline", "formal-outline:layered", "Formal layered outline", volumes)
	} else if len(outline) > 0 {
		fragments = appendJSONFragment(fragments, "outline", "formal-outline:flat", "Formal outline", outline)
	}
	if len(volumes) > 0 {
		for _, volume := range volumes {
			if summary, loadErr := b.store.Summaries.LoadVolumeSummary(volume.Index); loadErr != nil {
				return nil, loadErr
			} else if summary != nil {
				fragments = appendJSONFragment(fragments, "summary", fmt.Sprintf("volume-summary:%04d", volume.Index), "Existing volume summary", summary)
			}
			for _, arc := range volume.Arcs {
				if summary, loadErr := b.store.Summaries.LoadArcSummary(volume.Index, arc.Index); loadErr != nil {
					return nil, loadErr
				} else if summary != nil {
					fragments = appendJSONFragment(fragments, "summary", fmt.Sprintf("arc-summary:%04d:%04d", volume.Index, arc.Index), "Existing arc summary", summary)
				}
			}
		}
	}
	for _, entry := range outline {
		summary, loadErr := b.store.Summaries.LoadSummary(entry.Chapter)
		if loadErr != nil {
			return nil, loadErr
		}
		if summary != nil && strings.TrimSpace(summary.Summary) != "" {
			fragments = appendJSONFragment(fragments, "summary", fmt.Sprintf("chapter-summary:%06d", entry.Chapter), "Existing chapter summary", summary)
		}
	}
	return fragments, nil
}

func (b sourceBuilder) volume(scopeID string) ([]SourceFragment, error) {
	volumes, _, err := b.formalOutline()
	if err != nil {
		return nil, err
	}
	volume, ok := findVolume(volumes, scopeID)
	if !ok {
		return nil, fmt.Errorf("%w: volume %q was not found", ErrSourceUnavailable, scopeID)
	}
	foundation, err := b.store.Foundation.Load()
	if err != nil {
		return nil, err
	}
	fragments := appendFoundationFragment(nil, foundation)
	fragments = appendJSONFragment(fragments, "outline", "volume-outline:"+stableScopeID(volume.ID, volume.Index), "Selected volume outline", volume)
	if summary, loadErr := b.store.Summaries.LoadVolumeSummary(volume.Index); loadErr != nil {
		return nil, loadErr
	} else if summary != nil {
		fragments = appendJSONFragment(fragments, "summary", fmt.Sprintf("volume-summary:%04d", volume.Index), "Existing volume summary", summary)
	}
	for _, arc := range volume.Arcs {
		if summary, loadErr := b.store.Summaries.LoadArcSummary(volume.Index, arc.Index); loadErr != nil {
			return nil, loadErr
		} else if summary != nil {
			fragments = appendJSONFragment(fragments, "summary", fmt.Sprintf("arc-summary:%04d:%04d", volume.Index, arc.Index), "Existing arc summary", summary)
		}
		for _, chapter := range arc.Chapters {
			summary, loadErr := b.store.Summaries.LoadSummary(chapter.Chapter)
			if loadErr != nil {
				return nil, loadErr
			}
			if summary != nil && strings.TrimSpace(summary.Summary) != "" {
				fragments = appendJSONFragment(fragments, "summary", fmt.Sprintf("chapter-summary:%06d", chapter.Chapter), "Existing chapter summary", summary)
			} else {
				fragments = appendJSONFragment(fragments, "outline", fmt.Sprintf("chapter-outline:%06d", chapter.Chapter), "Chapter outline fallback", chapter)
			}
		}
	}
	return fragments, nil
}

func (b sourceBuilder) chapter(scopeID string) ([]SourceFragment, error) {
	volumes, outline, err := b.formalOutline()
	if err != nil {
		return nil, err
	}
	entry, volume, ok := findChapter(volumes, outline, scopeID)
	if !ok {
		return nil, fmt.Errorf("%w: chapter %q was not found", ErrSourceUnavailable, scopeID)
	}
	foundation, err := b.store.Foundation.Load()
	if err != nil {
		return nil, err
	}
	fragments := appendFoundationFragment(nil, foundation)
	if volume != nil {
		fragments = appendJSONFragment(fragments, "outline", "volume-outline:"+stableScopeID(volume.ID, volume.Index), "Containing volume outline", volume)
	}
	summary, err := b.store.Summaries.LoadSummary(entry.Chapter)
	if err != nil {
		return nil, err
	}
	switch {
	case summary != nil && strings.TrimSpace(summary.Summary) != "":
		fragments = appendJSONFragment(fragments, "summary", fmt.Sprintf("chapter-summary:%06d", entry.Chapter), "Existing chapter summary", summary)
	default:
		prose, loadErr := b.store.Drafts.LoadChapterText(entry.Chapter)
		if loadErr != nil {
			return nil, loadErr
		}
		if strings.TrimSpace(prose) != "" {
			fragments = append(fragments, SourceFragment{Kind: "prose", ID: fmt.Sprintf("chapter-prose:%06d", entry.Chapter), Label: "Bounded final chapter prose", Content: prose})
		} else {
			fragments = appendJSONFragment(fragments, "outline", fmt.Sprintf("chapter-outline:%06d", entry.Chapter), "Chapter outline fallback", entry)
		}
	}
	return fragments, nil
}

func (b sourceBuilder) character(scopeID string) ([]SourceFragment, error) {
	foundation, err := b.store.Foundation.Load()
	if err != nil {
		return nil, err
	}
	characters := append([]domain.Character(nil), foundation.Characters...)
	sort.Slice(characters, func(i, j int) bool { return characters[i].ID < characters[j].ID })
	selected, ok := findCharacter(characters, scopeID)
	if !ok {
		return nil, fmt.Errorf("%w: character %q was not found", ErrSourceUnavailable, scopeID)
	}
	relationships := make([]domain.CharacterRelationship, 0)
	relatedIDs := map[string]bool{selected.ID: true}
	for _, relationship := range foundation.Relationships {
		if relationship.SourceCharacterID != selected.ID && relationship.TargetCharacterID != selected.ID {
			continue
		}
		relationships = append(relationships, relationship)
		relatedIDs[relationship.SourceCharacterID] = true
		relatedIDs[relationship.TargetCharacterID] = true
	}
	sort.Slice(relationships, func(i, j int) bool { return relationships[i].ID < relationships[j].ID })
	fragments := appendJSONFragment(nil, "character", "character:"+selected.ID, "Canonical character card", selected)
	for _, character := range characters {
		if character.ID != selected.ID && relatedIDs[character.ID] {
			fragments = appendJSONFragment(fragments, "character", "related-character:"+character.ID, "Related canonical character card", character)
		}
	}
	for _, relationship := range relationships {
		fragments = appendJSONFragment(fragments, "relationship", "relationship:"+relationship.ID, "Canonical relationship", relationship)
	}
	worldRules := append([]domain.WorldRule(nil), foundation.WorldRules...)
	sort.Slice(worldRules, func(i, j int) bool { return worldRules[i].ID < worldRules[j].ID })
	for _, rule := range worldRules {
		fragments = appendJSONFragment(fragments, "world", "world-rule:"+rule.ID, "Canonical world rule", rule)
	}
	return fragments, nil
}

func (b sourceBuilder) formalOutline() ([]domain.VolumeOutline, []domain.OutlineEntry, error) {
	volumes, err := b.store.Outline.LoadLayeredOutline()
	if err != nil {
		return nil, nil, fmt.Errorf("load layered artwork outline: %w", err)
	}
	outline, err := b.store.Outline.LoadOutline()
	if err != nil {
		return nil, nil, fmt.Errorf("load artwork outline: %w", err)
	}
	if len(volumes) > 0 {
		volumes = domain.ProjectLayeredOutlineOrder(volumes)
		outline = domain.FlattenOutline(volumes)
	}
	return volumes, outline, nil
}

func appendFoundationFragment(fragments []SourceFragment, foundation domain.StoryFoundation) []SourceFragment {
	if strings.TrimSpace(foundation.Premise) == "" && len(foundation.Characters) == 0 && len(foundation.Relationships) == 0 && len(foundation.WorldRules) == 0 {
		return fragments
	}
	copy := domain.CloneStoryFoundation(foundation)
	copy.UpdatedAt = ""
	return appendJSONFragment(fragments, "foundation", "story-foundation", "Published StoryFoundation", copy)
}

func appendJSONFragment(fragments []SourceFragment, kind, id, label string, value any) []SourceFragment {
	payload, err := json.Marshal(value)
	if err != nil || string(payload) == "null" || string(payload) == "[]" || string(payload) == "{}" {
		return fragments
	}
	return append(fragments, SourceFragment{Kind: kind, ID: id, Label: label, Content: string(payload)})
}

func findVolume(volumes []domain.VolumeOutline, scopeID string) (domain.VolumeOutline, bool) {
	index, _ := strconv.Atoi(scopeID)
	for _, volume := range volumes {
		if (index > 0 && volume.Index == index) || (volume.ID != "" && volume.ID == scopeID) {
			return volume, true
		}
	}
	return domain.VolumeOutline{}, false
}

func findChapter(volumes []domain.VolumeOutline, outline []domain.OutlineEntry, scopeID string) (domain.OutlineEntry, *domain.VolumeOutline, bool) {
	chapterNumber, _ := strconv.Atoi(scopeID)
	for volumeIndex := range volumes {
		for _, arc := range volumes[volumeIndex].Arcs {
			for _, entry := range arc.Chapters {
				if (chapterNumber > 0 && entry.Chapter == chapterNumber) || (entry.ID != "" && entry.ID == scopeID) {
					volume := volumes[volumeIndex]
					return entry, &volume, true
				}
			}
		}
	}
	for _, entry := range outline {
		if (chapterNumber > 0 && entry.Chapter == chapterNumber) || (entry.ID != "" && entry.ID == scopeID) {
			return entry, nil, true
		}
	}
	return domain.OutlineEntry{}, nil, false
}

func findCharacter(characters []domain.Character, scopeID string) (domain.Character, bool) {
	for _, character := range characters {
		if character.ID == scopeID || (character.ID == "" && character.Name == scopeID) {
			return character, true
		}
	}
	return domain.Character{}, false
}

func stableScopeID(id string, index int) string {
	if strings.TrimSpace(id) != "" {
		return id
	}
	return fmt.Sprintf("%04d", index)
}

func clipUTF8HeadTail(value string, limit int) (string, bool) {
	value = strings.TrimSpace(value)
	if limit <= 0 {
		return "", value != ""
	}
	if len([]byte(value)) <= limit {
		return value, false
	}
	const marker = "\n...[truncated]...\n"
	if limit <= len(marker) {
		return utf8Prefix(value, limit), true
	}
	contentBudget := limit - len(marker)
	head := utf8Prefix(value, contentBudget/2)
	tail := utf8Suffix(value, contentBudget-len([]byte(head)))
	return head + marker + tail, true
}

func utf8Prefix(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	end := 0
	for end < len(value) {
		_, width := utf8.DecodeRuneInString(value[end:])
		if end+width > limit {
			break
		}
		end += width
	}
	return value[:end]
}

func utf8Suffix(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	start := len(value)
	used := 0
	for start > 0 {
		_, width := utf8.DecodeLastRuneInString(value[:start])
		if used+width > limit {
			break
		}
		start -= width
		used += width
	}
	return value[start:]
}
