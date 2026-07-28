package workflow

import (
	"encoding/json"
	"strings"
)

// ArgumentsFromInput matches saved-workflow slash argument handling.
func ArgumentsFromInput(input string) json.RawMessage {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil
	}
	var object map[string]any
	if json.Unmarshal([]byte(input), &object) == nil && object != nil {
		return json.RawMessage(input)
	}
	args, _ := json.Marshal(map[string]string{"query": input, "objective": input})
	return args
}
