package domain

import (
	"fmt"
	"strings"
	"unicode"
)

// OutlineDuplicate identifies a chapter whose outline promise repeats an
// earlier or already-known chapter.
type OutlineDuplicate struct {
	Chapter         int
	ExistingChapter int
	Title           string
}

func (d OutlineDuplicate) Error() string {
	return fmt.Sprintf(
		"chapter %d duplicates outline beats from chapter %d: title/core_event/hook are too similar (%q)",
		d.Chapter,
		d.ExistingChapter,
		d.Title,
	)
}

// FindDuplicateOutlineEntries returns the first chapter in entries whose
// title/core_event/hook signature duplicates a chapter in previousGroups or an
// earlier item in entries.
func FindDuplicateOutlineEntries(
	entries []OutlineEntry,
	previousGroups ...[]OutlineEntry,
) (OutlineDuplicate, bool) {
	seen := make(map[string]OutlineEntry)
	for _, group := range previousGroups {
		for _, entry := range group {
			addOutlineSignature(seen, entry)
		}
	}

	for _, entry := range entries {
		signature := outlineSignature(entry)
		if signature == "" {
			continue
		}
		if existing, ok := seen[signature]; ok && existing.Chapter != entry.Chapter {
			return OutlineDuplicate{
				Chapter:         entry.Chapter,
				ExistingChapter: existing.Chapter,
				Title:           strings.TrimSpace(entry.Title),
			}, true
		}
		seen[signature] = entry
	}
	return OutlineDuplicate{}, false
}

func addOutlineSignature(seen map[string]OutlineEntry, entry OutlineEntry) {
	signature := outlineSignature(entry)
	if signature == "" {
		return
	}
	if _, ok := seen[signature]; ok {
		return
	}
	seen[signature] = entry
}

func outlineSignature(entry OutlineEntry) string {
	title := strings.TrimSpace(entry.Title)
	coreEvent := strings.TrimSpace(entry.CoreEvent)
	hook := strings.TrimSpace(entry.Hook)
	if title == "" || coreEvent == "" || hook == "" {
		return ""
	}
	titleKey := outlineSignaturePart(title)
	coreEventKey := outlineSignaturePart(coreEvent)
	hookKey := outlineSignaturePart(hook)
	if titleKey == "" || coreEventKey == "" || hookKey == "" {
		return ""
	}
	return strings.Join([]string{titleKey, coreEventKey, hookKey}, "\x00")
}

func outlineSignaturePart(text string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(text)) {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
