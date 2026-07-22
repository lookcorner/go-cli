package acp

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
)

type prStatusResponse struct {
	PR                *prStatus `json:"pr"`
	UpdatedSessionIDs []string  `json:"updatedSessionIds"`
}

type prStatus struct {
	URL            string  `json:"url"`
	State          string  `json:"state"`
	IsInMergeQueue bool    `json:"isInMergeQueue"`
	Number         *uint64 `json:"number,omitempty"`
	Title          *string `json:"title,omitempty"`
}

func (s *Server) handlePRStatus(ctx context.Context, incoming message) {
	var req struct {
		CWD    string `json:"cwd"`
		Branch string `json:"branch"`
	}
	if json.Unmarshal(incoming.Params, &req) != nil {
		s.respondError(incoming.ID, -32602, "invalid PR status parameters")
		return
	}
	s.respond(incoming.ID, prStatusResponse{
		PR:                lookupPRStatus(ctx, req.CWD, req.Branch),
		UpdatedSessionIDs: []string{},
	})
}

func lookupPRStatus(ctx context.Context, cwd, branch string) *prStatus {
	var view struct {
		State   string  `json:"state"`
		URL     string  `json:"url"`
		IsDraft bool    `json:"isDraft"`
		Number  *uint64 `json:"number"`
		Title   *string `json:"title"`
	}
	output, err := runGH(ctx, cwd, "pr", "view", branch, "--json", "state,url,isDraft,number,title")
	if err != nil || json.Unmarshal(output, &view) != nil || view.URL == "" {
		return nil
	}
	state := "open"
	switch strings.ToLower(view.State) {
	case "merged":
		state = "merged"
	case "closed":
		state = "closed"
	default:
		if view.IsDraft {
			state = "draft"
		}
	}
	return &prStatus{
		URL: view.URL, State: state, Number: view.Number, Title: view.Title,
		IsInMergeQueue: state == "open" && lookupMergeQueue(ctx, cwd, view.URL),
	}
}

func lookupMergeQueue(ctx context.Context, cwd, url string) bool {
	const query = "query($url: URI!) { resource(url: $url) { ... on PullRequest { isInMergeQueue } } }"
	output, err := runGH(ctx, cwd, "api", "graphql", "-f", "query="+query, "-f", "url="+url)
	if err != nil {
		return false
	}
	var response struct {
		Data *struct {
			Resource *struct {
				IsInMergeQueue bool `json:"isInMergeQueue"`
			} `json:"resource"`
		} `json:"data"`
	}
	return json.Unmarshal(stripANSI(output), &response) == nil && response.Data != nil && response.Data.Resource != nil && response.Data.Resource.IsInMergeQueue
}

func runGH(ctx context.Context, cwd string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "gh", args...)
	command.Dir = cwd
	command.Env = append(os.Environ(), "PAGER=cat", "GH_PAGER=cat", "NO_COLOR=1")
	return command.Output()
}

func stripANSI(input []byte) []byte {
	output := make([]byte, 0, len(input))
	for index := 0; index < len(input); {
		if input[index] == 0x1b && index+1 < len(input) && input[index+1] == '[' {
			index += 2
			for index < len(input) && (input[index] < 0x40 || input[index] > 0x7e) {
				index++
			}
			if index < len(input) {
				index++
			}
			continue
		}
		output = append(output, input[index])
		index++
	}
	return output
}
