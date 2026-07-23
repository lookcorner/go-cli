package acp

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"time"

	"github.com/lookcorner/go-cli/internal/agent"
	sessionlog "github.com/lookcorner/go-cli/internal/session"
)

type reviewCommentRequest struct {
	SessionID   *string `json:"sessionId"`
	PromptIndex *uint32 `json:"promptIndex"`
	Comment     *string `json:"comment"`
	Citation    *struct {
		Path      *string `json:"path"`
		StartLine *uint32 `json:"startLine"`
		EndLine   *uint32 `json:"endLine"`
		Text      *string `json:"text"`
		Side      *string `json:"side"`
	} `json:"citation"`
}

type reviewCommentDeleteRequest struct {
	SessionID *string `json:"sessionId"`
	CommentID *string `json:"commentId"`
}

func (s *Server) handleReview(incoming message) {
	if incoming.Method == "x.ai/review/comment/delete" {
		s.handleReviewDelete(incoming)
		return
	}
	var request reviewCommentRequest
	if json.Unmarshal(incoming.Params, &request) != nil || !validReviewComment(request) {
		s.respondErrorData(incoming.ID, -32602, "Invalid params", "invalid review comment parameters")
		return
	}
	commentID, err := newReviewCommentID(time.Now())
	if err != nil {
		s.respondErrorData(incoming.ID, -32603, "Internal error", err.Error())
		return
	}
	event := sessionlog.ReviewEvent{
		Event: "create", CommentID: commentID, SessionID: *request.SessionID, PromptIndex: request.PromptIndex,
		Citation: &sessionlog.ReviewCitation{
			Path: *request.Citation.Path, StartLine: *request.Citation.StartLine,
			EndLine: *request.Citation.EndLine, Text: *request.Citation.Text, Side: request.Citation.Side,
		},
	}
	if logger := s.reviewLogger(*request.SessionID); logger != nil {
		if err := logger.Append("review_comment", event); err != nil {
			s.respondErrorData(incoming.ID, -32603, "Internal error", "record review comment: "+err.Error())
			return
		}
	}
	s.respond(incoming.ID, map[string]any{"commentId": commentID, "recorded": true})
}

func (s *Server) handleReviewDelete(incoming message) {
	var request reviewCommentDeleteRequest
	if json.Unmarshal(incoming.Params, &request) != nil || request.SessionID == nil || request.CommentID == nil {
		s.respondErrorData(incoming.ID, -32602, "Invalid params", "invalid review comment delete parameters")
		return
	}
	if logger := s.reviewLogger(*request.SessionID); logger != nil {
		if err := logger.Append("review_comment", sessionlog.ReviewEvent{Event: "delete", CommentID: *request.CommentID, SessionID: *request.SessionID}); err != nil {
			s.respondErrorData(incoming.ID, -32603, "Internal error", "delete review comment: "+err.Error())
			return
		}
	}
	s.respond(incoming.ID, map[string]any{"commentId": *request.CommentID, "deleted": true})
}

func validReviewComment(request reviewCommentRequest) bool {
	return request.SessionID != nil && request.PromptIndex != nil && request.Comment != nil && request.Citation != nil &&
		request.Citation.Path != nil && request.Citation.StartLine != nil && request.Citation.EndLine != nil && request.Citation.Text != nil
}

func (s *Server) reviewLogger(sessionID string) agent.EventLogger {
	current := s.lookupSession(sessionID)
	if current == nil {
		return nil
	}
	current.mu.Lock()
	defer current.mu.Unlock()
	if current.runner == nil {
		return nil
	}
	return current.runner.Logger
}

func newReviewCommentID(now time.Time) (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate review comment id: %w", err)
	}
	milliseconds := uint64(now.UnixMilli())
	value[0], value[1], value[2] = byte(milliseconds>>40), byte(milliseconds>>32), byte(milliseconds>>24)
	value[3], value[4], value[5] = byte(milliseconds>>16), byte(milliseconds>>8), byte(milliseconds)
	value[6] = value[6]&0x0f | 0x70
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[:4], value[4:6], value[6:8], value[8:10], value[10:]), nil
}
