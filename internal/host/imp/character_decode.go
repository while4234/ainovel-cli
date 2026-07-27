package imp

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

var importCharacterFields = map[string]struct{}{
	"id": {}, "name": {}, "aliases": {}, "role": {}, "description": {},
	"arc": {}, "traits": {}, "tier": {}, "faction": {}, "goal": {},
	"motivation": {}, "conflict": {}, "voice": {}, "constraints": {},
	"contrast_details": {}, "key_backstory": {}, "initial_state": {},
	"knowledge_boundary": {}, "notes": {},
	// Legacy merge prompts emitted these fields even though domain.Character
	// could not consume them. They are migrated below rather than discarded.
	"goals": {}, "relationships": {},
}

func decodeCharactersJSON(label, body string, out *[]domain.Character, optional bool) error {
	body = stripFences(body)
	if strings.TrimSpace(body) == "" {
		if optional {
			return nil
		}
		return fmt.Errorf("%s array is empty", label)
	}
	segment, err := extractJSONSegment(body)
	if err != nil {
		return fmt.Errorf("extract %s JSON: %w", label, err)
	}
	var objects []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(segment), &objects); err != nil {
		return fmt.Errorf("parse %s JSON: %w", label, err)
	}
	characters := make([]domain.Character, 0, len(objects))
	for index, object := range objects {
		for key := range object {
			if _, supported := importCharacterFields[key]; !supported {
				return fmt.Errorf("%s[%d] contains unsupported field %q", label, index, key)
			}
		}
		encoded, marshalErr := json.Marshal(object)
		if marshalErr != nil {
			return fmt.Errorf("encode %s[%d]: %w", label, index, marshalErr)
		}
		var character domain.Character
		if unmarshalErr := json.Unmarshal(encoded, &character); unmarshalErr != nil {
			return fmt.Errorf("parse %s[%d]: %w", label, index, unmarshalErr)
		}
		var legacy struct {
			Goals         stringListCompatibility `json:"goals"`
			Relationships stringListCompatibility `json:"relationships"`
		}
		if unmarshalErr := json.Unmarshal(encoded, &legacy); unmarshalErr != nil {
			return fmt.Errorf("parse legacy %s[%d]: %w", label, index, unmarshalErr)
		}
		if strings.TrimSpace(character.Goal) == "" && len(legacy.Goals) > 0 {
			character.Goal = strings.Join(legacy.Goals, "；")
		}
		if len(legacy.Relationships) > 0 {
			relationshipNote := "来源关系：" + strings.Join(legacy.Relationships, "；")
			if strings.TrimSpace(character.Notes) == "" {
				character.Notes = relationshipNote
			} else {
				character.Notes = strings.TrimSpace(character.Notes) + "；" + relationshipNote
			}
		}
		characters = append(characters, character)
	}
	if len(characters) == 0 && !optional {
		return fmt.Errorf("%s array is empty", label)
	}
	*out = characters
	return nil
}

type stringListCompatibility []string

func (s *stringListCompatibility) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		return nil
	}
	var values []string
	if err := json.Unmarshal(data, &values); err == nil {
		*s = values
		return nil
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("want string or string array")
	}
	if strings.TrimSpace(value) != "" {
		*s = []string{value}
	}
	return nil
}
