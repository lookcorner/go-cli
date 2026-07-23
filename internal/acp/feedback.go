package acp

import (
	"fmt"

	"github.com/lookcorner/go-cli/internal/agent"
)

func (s *Server) handleFeedbackSlashPrompt(incoming message, current *session, lifecycle promptLifecycle, text string) {
	output := "Usage: /feedback <text>"
	if text != "" {
		if err := current.runner.SubmitFeedback(text); err != nil {
			output = fmt.Sprintf("Feedback could not be saved locally: %v", err)
		} else {
			output = "Feedback saved locally; no feedback server is configured for this session."
		}
	}
	s.sendCommandOutput(current.id, output)
	s.finishPrompt(incoming, current, lifecycle, "end_turn", agent.Result{}, nil, "")
}
