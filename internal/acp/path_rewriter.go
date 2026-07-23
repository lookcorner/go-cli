package acp

import (
	"encoding/json"
	"net/url"
	"strings"
)

type pathRewriter struct {
	real, display               string
	encodedReal, encodedDisplay string
}

func newPathRewriter(real, display string) *pathRewriter {
	if real == "" || display == "" || real == display {
		return nil
	}
	encode := func(value string) string {
		return strings.ReplaceAll(url.QueryEscape(value), "+", "%20")
	}
	return &pathRewriter{real: real, display: display, encodedReal: encode(real), encodedDisplay: encode(display)}
}

func (r *pathRewriter) rewrite(text string) string {
	if r == nil {
		return text
	}
	text = strings.ReplaceAll(text, r.real, r.display)
	return strings.ReplaceAll(text, r.encodedReal, r.encodedDisplay)
}

func (r *pathRewriter) rewriteJSON(value any) any {
	if r == nil {
		return value
	}
	data, err := json.Marshal(value)
	if err != nil {
		return value
	}
	rewritten := r.rewrite(string(data))
	if rewritten == string(data) {
		return value
	}
	var result any
	if json.Unmarshal([]byte(rewritten), &result) != nil {
		return value
	}
	return result
}

func (s *Server) rewriteSessionValue(sessionID string, value any) any {
	rewriter, ok := s.pathRewriters.Load(sessionID)
	if !ok {
		return value
	}
	return rewriter.(*pathRewriter).rewriteJSON(value)
}
