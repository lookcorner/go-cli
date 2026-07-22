package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lookcorner/go-cli/internal/lsp"
)

type codeNavigator interface {
	CodeNavigationServers() int
	CodeLocations(context.Context, string, string, int, int) ([]lsp.Location, error)
	CodeSymbols(context.Context, string) ([]lsp.Symbol, error)
}

type codeLocation struct {
	Path          string `json:"path"`
	Line          int    `json:"line"`
	Column        int    `json:"column"`
	EndLine       int    `json:"endLine"`
	EndColumn     int    `json:"endColumn"`
	MatchedSymbol string `json:"matchedSymbol,omitempty"`
}

func (s *Server) handleCodeNavigation(ctx context.Context, incoming message) {
	var base struct {
		SessionID string `json:"sessionId"`
		CWD       string `json:"cwd"`
	}
	if json.Unmarshal(incoming.Params, &base) != nil {
		s.respondError(incoming.ID, -32602, "invalid code navigation parameters")
		return
	}
	current := s.lookupSession(base.SessionID)
	if base.SessionID == "" || current == nil || current.runner == nil || current.runner.Tools == nil {
		s.respondError(incoming.ID, -32602, "sessionId is required for code navigation and must refer to a valid active session")
		return
	}
	current.mu.Lock()
	closed := current.closed
	current.mu.Unlock()
	if closed {
		s.respondError(incoming.ID, -32602, "sessionId is required for code navigation and must refer to a valid active session")
		return
	}
	navigator := sessionCodeNavigator(current)
	if incoming.Method == "x.ai/code/status" {
		active := navigator != nil && navigator.CodeNavigationServers() > 0
		reason := "disabledByConfig"
		if active {
			reason = "active"
		}
		s.respond(incoming.ID, map[string]any{"result": map[string]any{
			"indexed": active, "eligible": active, "reason": reason,
		}, "error": nil})
		return
	}
	if navigator == nil || navigator.CodeNavigationServers() == 0 {
		s.respondError(incoming.ID, -32602, "code navigation is unavailable because no LSP server is configured")
		return
	}
	root := current.cwd
	if strings.TrimSpace(base.CWD) != "" {
		root = base.CWD
	}
	if strings.HasPrefix(incoming.Method, "x.ai/code/goto-") {
		s.handleCodeGoto(ctx, incoming, navigator, root)
		return
	}
	s.handleCodeFind(ctx, incoming, navigator, root)
}

func sessionCodeNavigator(current *session) codeNavigator {
	for _, tool := range current.runner.Tools.SnapshotTools() {
		if navigator, ok := tool.(codeNavigator); ok {
			return navigator
		}
	}
	return nil
}

func (s *Server) handleCodeGoto(ctx context.Context, incoming message, navigator codeNavigator, root string) {
	var req struct {
		Path   *string `json:"path"`
		Row    int     `json:"row"`
		Column int     `json:"column"`
	}
	if json.Unmarshal(incoming.Params, &req) != nil || req.Path == nil || req.Row < 1 || req.Column < 1 {
		s.respondError(incoming.ID, -32602, "path, row, and column are required for code navigation")
		return
	}
	path := *req.Path
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	operation := strings.TrimPrefix(incoming.Method, "x.ai/code/goto-")
	locations, err := navigator.CodeLocations(ctx, operation, path, req.Row, req.Column)
	if err != nil {
		s.respondError(incoming.ID, -32000, "code navigation: "+err.Error())
		return
	}
	s.respondCodeLocations(incoming.ID, "", locations, "", "")
}

func (s *Server) handleCodeFind(ctx context.Context, incoming message, navigator codeNavigator, root string) {
	var req struct {
		Symbol      *string `json:"symbol"`
		ContextPath string  `json:"contextPath"`
	}
	if json.Unmarshal(incoming.Params, &req) != nil || req.Symbol == nil || *req.Symbol == "" {
		s.respondError(incoming.ID, -32602, "symbol is required for code navigation")
		return
	}
	symbols, err := navigator.CodeSymbols(ctx, *req.Symbol)
	if err != nil {
		s.respondError(incoming.ID, -32000, "code navigation: "+err.Error())
		return
	}
	contextPath := req.ContextPath
	if contextPath != "" && !filepath.IsAbs(contextPath) {
		contextPath = filepath.Join(root, contextPath)
	}
	matched := make([]lsp.Symbol, 0, len(symbols))
	for _, symbol := range symbols {
		if symbol.Name == *req.Symbol {
			matched = append(matched, symbol)
		}
	}
	sort.SliceStable(matched, func(i, j int) bool {
		return matched[i].Location.Path == contextPath && matched[j].Location.Path != contextPath
	})
	if incoming.Method == "x.ai/code/find-definitions" {
		locations := make([]lsp.Location, 0, len(matched))
		for _, symbol := range matched {
			locations = append(locations, symbol.Location)
		}
		s.respondCodeLocations(incoming.ID, *req.Symbol, locations, *req.Symbol, contextPath)
		return
	}
	var locations []lsp.Location
	for _, symbol := range matched {
		position := symbol.Location.Range.Start
		found, queryErr := navigator.CodeLocations(ctx, "references", symbol.Location.Path, position.Line+1, position.Character+1)
		if queryErr != nil {
			err = errors.Join(err, queryErr)
			continue
		}
		locations = append(locations, found...)
	}
	if len(locations) == 0 && err != nil {
		s.respondError(incoming.ID, -32000, "code navigation: "+err.Error())
		return
	}
	s.respondCodeLocations(incoming.ID, *req.Symbol, locations, *req.Symbol, contextPath)
}

func (s *Server) respondCodeLocations(id json.RawMessage, symbol string, locations []lsp.Location, matchedSymbol, contextPath string) {
	values := make([]codeLocation, 0, len(locations))
	seen := make(map[string]bool, len(locations))
	for _, location := range locations {
		value := codeLocation{
			Path: location.Path, Line: location.Range.Start.Line + 1, Column: location.Range.Start.Character + 1,
			EndLine: location.Range.End.Line + 1, EndColumn: location.Range.End.Character + 1, MatchedSymbol: matchedSymbol,
		}
		key := fmt.Sprintf("%s:%d:%d:%d:%d", value.Path, value.Line, value.Column, value.EndLine, value.EndColumn)
		if !seen[key] {
			seen[key] = true
			values = append(values, value)
		}
	}
	sort.SliceStable(values, func(i, j int) bool {
		if contextPath != "" && (values[i].Path == contextPath) != (values[j].Path == contextPath) {
			return values[i].Path == contextPath
		}
		if values[i].Path != values[j].Path {
			return values[i].Path < values[j].Path
		}
		if values[i].Line != values[j].Line {
			return values[i].Line < values[j].Line
		}
		return values[i].Column < values[j].Column
	})
	s.respond(id, map[string]any{"result": map[string]any{"symbol": symbol, "locations": values}, "error": nil})
}
