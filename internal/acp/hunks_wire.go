package acp

import (
	"path/filepath"
	"time"

	"github.com/lookcorner/go-cli/internal/tools"
)

type hunkLineInfoWire struct {
	OldStart int `json:"oldStart"`
	OldCount int `json:"oldCount"`
	NewStart int `json:"newStart"`
	NewCount int `json:"newCount"`
}

type hunkSourceWire struct {
	Type        string `json:"type"`
	PromptIndex *int   `json:"prompt_index,omitempty"`
}

type hunkWire struct {
	ID        string           `json:"id"`
	Path      string           `json:"path"`
	LineInfo  hunkLineInfoWire `json:"lineInfo"`
	Source    hunkSourceWire   `json:"source"`
	OldText   *string          `json:"oldText"`
	NewText   string           `json:"newText"`
	Patch     *string          `json:"patch"`
	CreatedAt time.Time        `json:"createdAt"`
}

type getHunksWire struct {
	Hunks           []hunkWire             `json:"hunks"`
	Baseline        *tools.FileContentView `json:"baseline,omitempty"`
	Current         *tools.FileContentView `json:"current,omitempty"`
	BaselineContent *string                `json:"baselineContent,omitempty"`
	CurrentContent  *string                `json:"currentContent,omitempty"`
}

type hunkTurnWire struct {
	PromptIndex  int        `json:"promptIndex"`
	Files        []string   `json:"files"`
	PendingHunks []hunkWire `json:"pendingHunks"`
	LinesAdded   int        `json:"linesAdded"`
	LinesRemoved int        `json:"linesRemoved"`
}

type hunkSummaryWire struct {
	Stats               tools.HunkSessionStats `json:"stats"`
	Turns               []hunkTurnWire         `json:"turns"`
	FilesModified       int                    `json:"filesModified"`
	FilesWithPending    int                    `json:"filesWithPending"`
	PendingHunks        int                    `json:"pendingHunks"`
	PendingLinesAdded   int                    `json:"pendingLinesAdded"`
	PendingLinesRemoved int                    `json:"pendingLinesRemoved"`
	UnattributedPending int                    `json:"unattributedPending"`
}

func hunkWires(hunks []tools.Hunk, tracker *tools.HunkTracker, realCWD, displayCWD string, includePatch bool) []hunkWire {
	result := make([]hunkWire, 0, len(hunks))
	for _, hunk := range hunks {
		sourceType := "external"
		if hunk.Source == "agent" {
			sourceType = "agentEdit"
		} else if tracker.IsAgentFile(hunk.Path) {
			sourceType = "externalEditOnAgentFile"
		}
		var oldText *string
		if hunk.OldLines > 0 {
			value := hunk.OldText
			oldText = &value
		}
		var patch *string
		if includePatch {
			value := hunk.Patch
			patch = &value
		}
		result = append(result, hunkWire{
			ID: hunk.ID, Path: displayHunkPath(realCWD, displayCWD, hunk.Path),
			LineInfo: hunkLineInfoWire{OldStart: hunk.OldStart, OldCount: hunk.OldLines, NewStart: hunk.NewStart, NewCount: hunk.NewLines},
			Source:   hunkSourceWire{Type: sourceType, PromptIndex: hunk.PromptIndex},
			OldText:  oldText, NewText: hunk.NewText, Patch: patch, CreatedAt: hunk.CreatedAt,
		})
	}
	return result
}

func hunkTurnWires(turns []tools.HunkTurnSummary, tracker *tools.HunkTracker, realCWD, displayCWD string) []hunkTurnWire {
	result := make([]hunkTurnWire, 0, len(turns))
	for _, turn := range turns {
		files := make([]string, len(turn.Files))
		for index, path := range turn.Files {
			files[index] = displayHunkPath(realCWD, displayCWD, path)
		}
		result = append(result, hunkTurnWire{
			PromptIndex: turn.PromptIndex, Files: files, PendingHunks: hunkWires(turn.PendingHunks, tracker, realCWD, displayCWD, false),
			LinesAdded: turn.LinesAdded, LinesRemoved: turn.LinesRemoved,
		})
	}
	return result
}

func displayHunkPath(realCWD, displayCWD, path string) string {
	if displayCWD == "" {
		displayCWD = realCWD
	}
	if rewriter := newPathRewriter(realCWD, displayCWD); rewriter != nil {
		return rewriter.rewritePath(path)
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(realCWD, filepath.FromSlash(path))
}
