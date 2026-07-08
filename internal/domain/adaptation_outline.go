package domain

import (
	"fmt"
	"strings"
	"unicode"
)

// AdaptationChapterOutlineDuplicate identifies a target chapter whose story
// promise repeats an earlier or already-known chapter plan.
type AdaptationChapterOutlineDuplicate struct {
	Chapter         int
	ExistingChapter int
	Title           string
}

func (d AdaptationChapterOutlineDuplicate) Error() string {
	return fmt.Sprintf(
		"chapter %d duplicates outline beats from chapter %d: title/core_event/hook are too similar (%q)",
		d.Chapter,
		d.ExistingChapter,
		d.Title,
	)
}

// FindDuplicateAdaptationChapterOutline returns the first chapter in chapters
// whose title/core_event/hook signature duplicates a chapter in previousGroups
// or an earlier item in chapters. Reused source ranges are allowed; this only
// catches repeated story promises.
func FindDuplicateAdaptationChapterOutline(
	chapters []AdaptationChapterPlan,
	previousGroups ...[]AdaptationChapterPlan,
) (AdaptationChapterOutlineDuplicate, bool) {
	seen := make(map[string]AdaptationChapterPlan)
	for _, group := range previousGroups {
		for _, chapter := range group {
			addAdaptationOutlineSignature(seen, chapter)
		}
	}

	for _, chapter := range chapters {
		signature := adaptationOutlineSignature(chapter)
		if signature == "" {
			continue
		}
		if existing, ok := seen[signature]; ok && existing.Chapter != chapter.Chapter {
			return AdaptationChapterOutlineDuplicate{
				Chapter:         chapter.Chapter,
				ExistingChapter: existing.Chapter,
				Title:           adaptationChapterTitle(chapter),
			}, true
		}
		seen[signature] = chapter
	}
	return AdaptationChapterOutlineDuplicate{}, false
}

func addAdaptationOutlineSignature(seen map[string]AdaptationChapterPlan, chapter AdaptationChapterPlan) {
	signature := adaptationOutlineSignature(chapter)
	if signature == "" {
		return
	}
	if _, ok := seen[signature]; ok {
		return
	}
	seen[signature] = chapter
}

func adaptationOutlineSignature(chapter AdaptationChapterPlan) string {
	title := adaptationChapterTitle(chapter)
	coreEvent := adaptationChapterCoreEvent(chapter)
	hook := adaptationChapterHook(chapter)
	if title == "" || coreEvent == "" || hook == "" {
		return ""
	}
	titleKey := adaptationOutlineSignaturePart(title)
	coreEventKey := adaptationOutlineSignaturePart(coreEvent)
	hookKey := adaptationOutlineSignaturePart(hook)
	if titleKey == "" || coreEventKey == "" || hookKey == "" {
		return ""
	}
	return strings.Join([]string{titleKey, coreEventKey, hookKey}, "\x00")
}

func adaptationChapterTitle(chapter AdaptationChapterPlan) string {
	if title := strings.TrimSpace(chapter.Title); title != "" {
		return title
	}
	return strings.TrimSpace(chapter.OutlineEntry.Title)
}

func adaptationChapterCoreEvent(chapter AdaptationChapterPlan) string {
	return strings.TrimSpace(chapter.OutlineEntry.CoreEvent)
}

func adaptationChapterHook(chapter AdaptationChapterPlan) string {
	return strings.TrimSpace(chapter.OutlineEntry.Hook)
}

func adaptationOutlineSignaturePart(text string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(text)) {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
