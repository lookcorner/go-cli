package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestResponsesInputSerializesImageContent(t *testing.T) {
	request := ResponseRequest{Model: "test", Reasoning: &ReasoningConfig{Effort: "high"}, Input: []InputItem{{
		Type: "message", Role: "user", Content: []ContentPart{
			{Type: "input_text", Text: "inspect"},
			{Type: "input_image", ImageURL: "data:image/png;base64,cG5n"},
		},
	}}}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"type":"input_text"`, `"type":"input_image"`, `"image_url":"data:image/png;base64,cG5n"`, `"reasoning":{"effort":"high"}`} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("missing %s from Responses request: %s", want, encoded)
		}
	}
}
