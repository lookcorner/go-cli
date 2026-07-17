package lsp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/lookcorner/go-cli/internal/workspace"
)

const maxWorkspaceEditFiles = 100
const maxWorkspaceTextEdits = 10000

type lspTextEdit struct {
	Range struct {
		Start workspace.TextPosition `json:"start"`
		End   workspace.TextPosition `json:"end"`
	} `json:"range"`
	NewText string `json:"newText"`
}

type editOperation struct {
	uri          string
	path         string
	version      *int
	failedChange *int
	edits        []workspace.TextEdit
}

func (c *Client) applyWorkspaceEdit(raw json.RawMessage) map[string]any {
	c.documentMu.Lock()
	defer c.documentMu.Unlock()
	operations, err := decodeWorkspaceEdit(raw)
	if err != nil {
		return map[string]any{"applied": false, "failureReason": err.Error()}
	}
	if err := c.prepareEditOperations(operations); err != nil {
		return map[string]any{"applied": false, "failureReason": err.Error()}
	}
	for _, operation := range operations {
		content, err := c.workspace.ApplyTextEdits(operation.path, operation.edits)
		if err != nil {
			return workspaceEditFailure(err, operation.failedChange)
		}
		if err := c.recordAppliedDocument(operation.uri, content); err != nil {
			return workspaceEditFailure(err, operation.failedChange)
		}
	}
	return map[string]any{"applied": true}
}

func decodeWorkspaceEdit(raw json.RawMessage) ([]editOperation, error) {
	var params struct {
		Edit struct {
			Changes         map[string][]lspTextEdit `json:"changes"`
			DocumentChanges []json.RawMessage        `json:"documentChanges"`
		} `json:"edit"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, errors.New("invalid workspace/applyEdit parameters")
	}
	operations := make([]editOperation, 0, len(params.Edit.Changes)+len(params.Edit.DocumentChanges))
	keys := make([]string, 0, len(params.Edit.Changes))
	for uri := range params.Edit.Changes {
		keys = append(keys, uri)
	}
	sort.Strings(keys)
	for _, uri := range keys {
		operations = append(operations, editOperation{uri: uri, edits: convertTextEdits(params.Edit.Changes[uri])})
	}
	for documentIndex, rawChange := range params.Edit.DocumentChanges {
		var probe struct {
			Kind string `json:"kind"`
		}
		if json.Unmarshal(rawChange, &probe) != nil {
			return nil, errors.New("invalid workspace document change")
		}
		if probe.Kind != "" {
			return nil, fmt.Errorf("workspace resource operation %q is not supported", probe.Kind)
		}
		var change struct {
			TextDocument struct {
				URI     string `json:"uri"`
				Version *int   `json:"version"`
			} `json:"textDocument"`
			Edits []lspTextEdit `json:"edits"`
		}
		if json.Unmarshal(rawChange, &change) != nil || change.TextDocument.URI == "" {
			return nil, errors.New("invalid text document edit")
		}
		index := documentIndex
		operations = append(operations, editOperation{
			uri: change.TextDocument.URI, version: change.TextDocument.Version,
			failedChange: &index, edits: convertTextEdits(change.Edits),
		})
	}
	if len(operations) > maxWorkspaceEditFiles {
		return nil, fmt.Errorf("workspace edit exceeds %d files", maxWorkspaceEditFiles)
	}
	count := 0
	for _, operation := range operations {
		count += len(operation.edits)
	}
	if count > maxWorkspaceTextEdits {
		return nil, fmt.Errorf("workspace edit exceeds %d text edits", maxWorkspaceTextEdits)
	}
	return operations, nil
}

func workspaceEditFailure(err error, failedChange *int) map[string]any {
	result := map[string]any{"applied": false, "failureReason": err.Error()}
	if failedChange != nil {
		result["failedChange"] = *failedChange
	}
	return result
}

func convertTextEdits(edits []lspTextEdit) []workspace.TextEdit {
	converted := make([]workspace.TextEdit, len(edits))
	for index, edit := range edits {
		converted[index] = workspace.TextEdit{Start: edit.Range.Start, End: edit.Range.End, NewText: edit.NewText}
	}
	return converted
}

func (c *Client) prepareEditOperations(operations []editOperation) error {
	seen := make(map[string]struct{}, len(operations))
	for index := range operations {
		path, err := pathFromFileURI(operations[index].uri)
		if err != nil {
			return err
		}
		resolved, err := c.workspace.Resolve(path)
		if err != nil {
			return err
		}
		uri := fileURI(resolved)
		if _, duplicate := seen[uri]; duplicate {
			return fmt.Errorf("workspace edit contains duplicate document %q", uri)
		}
		seen[uri] = struct{}{}
		operations[index].uri, operations[index].path = uri, resolved
		if operations[index].version == nil {
			continue
		}
		c.mu.Lock()
		state, open := c.documents[uri]
		c.mu.Unlock()
		if open && state.version != *operations[index].version {
			return fmt.Errorf("document %q version is %d, edit requires %d", uri, state.version, *operations[index].version)
		}
	}
	return nil
}

func (c *Client) recordAppliedDocument(uri, content string) error {
	c.mu.Lock()
	state, open := c.documents[uri]
	if open {
		state.content = content
		state.version++
		c.documents[uri] = state
	}
	c.mu.Unlock()
	if !open {
		return nil
	}
	return c.notify("textDocument/didChange", map[string]any{
		"textDocument":   map[string]any{"uri": uri, "version": state.version},
		"contentChanges": []any{map[string]any{"text": content}},
	})
}

func pathFromFileURI(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "file" || (parsed.Host != "" && !strings.EqualFold(parsed.Host, "localhost")) || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("workspace edit URI %q is not a local file URI", value)
	}
	path := filepath.FromSlash(parsed.Path)
	if runtime.GOOS == "windows" && len(path) >= 3 && filepath.IsAbs(path) && path[0] == filepath.Separator && path[2] == ':' {
		path = path[1:]
	}
	if path == "" {
		return "", errors.New("workspace edit file URI has an empty path")
	}
	return path, nil
}
