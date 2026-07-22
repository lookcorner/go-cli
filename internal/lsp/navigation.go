package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
)

type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

type Location struct {
	Path  string
	Range Range
}

type Symbol struct {
	Name     string
	Location Location
}

func (t *Tool) CodeNavigationServers() int { return len(t.manager.Names()) }

func (t *Tool) CodeLocations(ctx context.Context, operation, path string, line, character int) ([]Location, error) {
	if operation != "definition" && operation != "references" {
		return nil, fmt.Errorf("unsupported code navigation operation %q", operation)
	}
	path, err := t.manager.workspace.Resolve(path)
	if err != nil {
		return nil, err
	}
	method := "textDocument/" + operation
	var locations []Location
	var requestErrors []error
	attempted := false
	succeeded := false
	for _, name := range t.manager.Names() {
		client := t.manager.client(name)
		if client == nil || !supportsExtension(client.Extensions(), filepath.Ext(path)) {
			continue
		}
		attempted = true
		uri, err := client.SyncDocument(path)
		if err != nil {
			requestErrors = append(requestErrors, err)
			continue
		}
		params := map[string]any{
			"textDocument": map[string]any{"uri": uri},
			"position":     map[string]any{"line": max(line, 1) - 1, "character": max(character, 1) - 1},
		}
		if operation == "references" {
			params["context"] = map[string]any{"includeDeclaration": true}
		}
		raw, err := client.Request(ctx, method, params)
		if err != nil {
			requestErrors = append(requestErrors, err)
			continue
		}
		parsed, err := parseLocations(raw)
		if err != nil {
			requestErrors = append(requestErrors, err)
			continue
		}
		succeeded = true
		locations = append(locations, parsed...)
	}
	if !attempted {
		return nil, fmt.Errorf("no configured LSP server supports %s files", filepath.Ext(path))
	}
	if !succeeded && len(requestErrors) > 0 {
		return nil, errors.Join(requestErrors...)
	}
	return uniqueLocations(locations), nil
}

func (t *Tool) CodeSymbols(ctx context.Context, query string) ([]Symbol, error) {
	var symbols []Symbol
	var requestErrors []error
	succeeded := false
	for _, name := range t.manager.Names() {
		client := t.manager.client(name)
		if client == nil {
			continue
		}
		raw, err := client.Request(ctx, "workspace/symbol", map[string]any{"query": query})
		if err != nil {
			requestErrors = append(requestErrors, err)
			continue
		}
		parsed, err := parseSymbols(raw)
		if err != nil {
			requestErrors = append(requestErrors, err)
			continue
		}
		succeeded = true
		symbols = append(symbols, parsed...)
	}
	if !succeeded && len(requestErrors) > 0 {
		return nil, errors.Join(requestErrors...)
	}
	sort.SliceStable(symbols, func(i, j int) bool {
		if symbols[i].Name != symbols[j].Name {
			return symbols[i].Name < symbols[j].Name
		}
		if symbols[i].Location.Path != symbols[j].Location.Path {
			return symbols[i].Location.Path < symbols[j].Location.Path
		}
		return symbols[i].Location.Range.Start.Line < symbols[j].Location.Range.Start.Line
	})
	return symbols, nil
}

func parseLocations(raw json.RawMessage) ([]Location, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var values []json.RawMessage
	if raw[0] == '[' {
		if err := json.Unmarshal(raw, &values); err != nil {
			return nil, err
		}
	} else {
		values = []json.RawMessage{raw}
	}
	locations := make([]Location, 0, len(values))
	for _, value := range values {
		var item struct {
			URI                  string `json:"uri"`
			Range                Range  `json:"range"`
			TargetURI            string `json:"targetUri"`
			TargetRange          Range  `json:"targetRange"`
			TargetSelectionRange Range  `json:"targetSelectionRange"`
		}
		if err := json.Unmarshal(value, &item); err != nil {
			return nil, err
		}
		uri, selected := item.URI, item.Range
		if uri == "" {
			uri, selected = item.TargetURI, item.TargetSelectionRange
			if selected == (Range{}) {
				selected = item.TargetRange
			}
		}
		if uri == "" {
			continue
		}
		path, err := pathFromFileURI(uri)
		if err != nil {
			return nil, err
		}
		locations = append(locations, Location{Path: path, Range: selected})
	}
	return locations, nil
}

func parseSymbols(raw json.RawMessage) ([]Symbol, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var values []struct {
		Name     string `json:"name"`
		Location struct {
			URI   string `json:"uri"`
			Range Range  `json:"range"`
		} `json:"location"`
	}
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, err
	}
	symbols := make([]Symbol, 0, len(values))
	for _, value := range values {
		if value.Name == "" || value.Location.URI == "" {
			continue
		}
		path, err := pathFromFileURI(value.Location.URI)
		if err != nil {
			return nil, err
		}
		symbols = append(symbols, Symbol{Name: value.Name, Location: Location{Path: path, Range: value.Location.Range}})
	}
	return symbols, nil
}

func uniqueLocations(values []Location) []Location {
	seen := make(map[string]bool, len(values))
	result := make([]Location, 0, len(values))
	for _, value := range values {
		key := fmt.Sprintf("%s:%d:%d:%d:%d", value.Path, value.Range.Start.Line, value.Range.Start.Character, value.Range.End.Line, value.Range.End.Character)
		if !seen[key] {
			seen[key] = true
			result = append(result, value)
		}
	}
	return result
}
