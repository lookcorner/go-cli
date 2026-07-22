package acp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	worktrees "github.com/lookcorner/go-cli/internal/worktree"
)

type gitHeadChanged struct {
	SessionID  string  `json:"sessionId"`
	Branch     *string `json:"branch"`
	IsWorktree bool    `json:"isWorktree"`
	MainRepo   *string `json:"mainRepo"`
}

func parseClientGitHead(raw json.RawMessage) bool {
	var params struct {
		ClientCapabilities struct {
			Meta map[string]any `json:"_meta"`
		} `json:"clientCapabilities"`
	}
	if json.Unmarshal(raw, &params) != nil {
		return false
	}
	enabled, _ := params.ClientCapabilities.Meta["x.ai/gitHeadChanged"].(bool)
	return enabled
}

func (s *Server) startGitHeadNotifications(current *session) {
	if current == nil || !current.gitHeadEnabled {
		return
	}
	current.mu.Lock()
	if current.gitWatchCancel != nil {
		current.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(current.ctx)
	done := make(chan struct{})
	current.gitWatchCancel, current.gitWatchDone = cancel, done
	current.mu.Unlock()
	s.notifyGitHead(current)
	go func() {
		defer close(done)
		_ = worktrees.WatchHead(ctx, current.cwd, nil, func() { s.notifyGitHead(current) })
	}()
}

func (s *Server) notifyGitHead(current *session) {
	if current == nil || !current.gitHeadEnabled {
		return
	}
	head, err := worktrees.HeadInfo(current.ctx, current.cwd)
	if err != nil {
		return
	}
	var branch, mainRepo *string
	if head.Branch != "" {
		branch = &head.Branch
	}
	if head.IsWorktree {
		display := displayHomePath(head.MainRoot)
		mainRepo = &display
	}
	mainRepoKey := ""
	if mainRepo != nil {
		mainRepoKey = *mainRepo
	}
	key := strings.Join([]string{head.Branch, strconv.FormatBool(head.IsWorktree), mainRepoKey}, "\x00")
	current.mu.Lock()
	if current.closed || current.lastGitHead == key {
		current.mu.Unlock()
		return
	}
	current.lastGitHead = key
	current.mu.Unlock()

	s.write(map[string]any{
		"jsonrpc": "2.0", "method": "x.ai/git_head_changed",
		"params": gitHeadChanged{SessionID: current.id, Branch: branch, IsWorktree: head.IsWorktree, MainRepo: mainRepo},
	})
}

func displayHomePath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	relative, err := filepath.Rel(home, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return path
	}
	if relative == "." {
		return "~"
	}
	return filepath.Join("~", relative)
}

func gitChangingTool(name string) bool {
	switch name {
	case "write_file", "edit_file", "search_replace", "hashline_edit", "shell", "run_terminal_cmd":
		return true
	default:
		return false
	}
}
