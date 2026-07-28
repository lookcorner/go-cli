package workflow

import (
	"encoding/json"
	"testing"
)

func TestArgumentsFromInput(t *testing.T) {
	if ArgumentsFromInput("") != nil {
		t.Fatal("empty input did not preserve null arguments")
	}
	var args map[string]any
	if err := json.Unmarshal(ArgumentsFromInput(`{"depth":3}`), &args); err != nil || args["depth"] != float64(3) {
		t.Fatalf("object args=%#v err=%v", args, err)
	}
	if err := json.Unmarshal(ArgumentsFromInput("review release"), &args); err != nil || args["query"] != "review release" || args["objective"] != "review release" {
		t.Fatalf("text args=%#v err=%v", args, err)
	}
}
