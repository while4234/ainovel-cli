package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
)

// decodeOutlineEntries accepts the stable save_foundation contract as well as
// common model-produced wrappers and structured scene objects. Structured
// scenes are flattened to deterministic, readable strings without discarding
// any supplied field, because the durable OutlineEntry schema intentionally
// keeps scenes as prose-ready []string.
func decodeOutlineEntries(typeName, content string) ([]domain.OutlineEntry, error) {
	rawEntries, err := unwrapOutlineEntries([]byte(content))
	if err != nil {
		return nil, decodeFoundationJSON(typeName, content, &[]domain.OutlineEntry{})
	}
	entries := make([]domain.OutlineEntry, 0, len(rawEntries))
	for index, raw := range rawEntries {
		entry, decodeErr := decodeOutlineEntry(raw)
		if decodeErr != nil {
			return nil, fmt.Errorf("parse %s JSON entry %d: %w: %w", typeName, index+1, decodeErr, errs.ErrToolArgs)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func unwrapOutlineEntries(raw []byte) ([]json.RawMessage, error) {
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err == nil {
		return entries, nil
	}
	var wrapper map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return nil, err
	}
	for _, key := range []string{"chapters", "outline", "entries"} {
		value, ok := wrapper[key]
		if !ok {
			continue
		}
		if err := json.Unmarshal(value, &entries); err != nil {
			return nil, err
		}
		return entries, nil
	}
	return nil, fmt.Errorf("expected an array or an object containing chapters")
}

func decodeOutlineEntry(raw json.RawMessage) (domain.OutlineEntry, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return domain.OutlineEntry{}, err
	}
	sceneRaw := fields["scenes"]
	delete(fields, "scenes")
	withoutScenes, err := json.Marshal(fields)
	if err != nil {
		return domain.OutlineEntry{}, err
	}
	var entry domain.OutlineEntry
	if err := json.Unmarshal(withoutScenes, &entry); err != nil {
		return domain.OutlineEntry{}, err
	}
	if len(sceneRaw) > 0 && !bytes.Equal(bytes.TrimSpace(sceneRaw), []byte("null")) {
		entry.Scenes, err = decodeOutlineScenes(sceneRaw)
		if err != nil {
			return domain.OutlineEntry{}, fmt.Errorf("scenes: %w", err)
		}
	}
	return entry, nil
}

func decodeOutlineScenes(raw json.RawMessage) ([]string, error) {
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		if strings.TrimSpace(single) == "" {
			return nil, nil
		}
		return []string{single}, nil
	}
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, err
	}
	scenes := make([]string, 0, len(values))
	for _, value := range values {
		scene, err := stringifyOutlineScene(value)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(scene) != "" {
			scenes = append(scenes, scene)
		}
	}
	return scenes, nil
}

func stringifyOutlineScene(raw json.RawMessage) (string, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		var value any
		if valueErr := json.Unmarshal(raw, &value); valueErr != nil {
			return "", valueErr
		}
		compact, compactErr := json.Marshal(value)
		return string(compact), compactErr
	}

	preferred := []string{
		"title", "name", "scene", "location", "time", "characters", "goal",
		"purpose", "conflict", "action", "turn", "choice", "cost", "outcome",
		"hook", "detail", "description",
	}
	rank := make(map[string]int, len(preferred))
	for index, key := range preferred {
		rank[key] = index
	}
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		left, leftKnown := rank[keys[i]]
		right, rightKnown := rank[keys[j]]
		if leftKnown != rightKnown {
			return leftKnown
		}
		if leftKnown && left != right {
			return left < right
		}
		return keys[i] < keys[j]
	})

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		value, err := readableSceneValue(object[key])
		if err != nil {
			return "", err
		}
		if value != "" {
			parts = append(parts, key+": "+value)
		}
	}
	return strings.Join(parts, "；"), nil
}

func readableSceneValue(raw json.RawMessage) (string, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	compact, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(compact), nil
}
