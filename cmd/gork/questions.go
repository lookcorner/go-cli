package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/lookcorner/go-cli/internal/tools"
)

type serializedApprover struct {
	mu   *sync.Mutex
	base tools.Approver
}

func (a serializedApprover) Approve(ctx context.Context, action, detail string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.base.Approve(ctx, action, detail)
}

type terminalQuestionObserver struct {
	input  *bufio.Reader
	output io.Writer
	mu     *sync.Mutex
}

func (o *terminalQuestionObserver) AskUserQuestion(ctx context.Context, request tools.UserQuestionRequest) (tools.UserQuestionResponse, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	answers := make(map[string][]string, len(request.Questions))
	annotations := make(map[string]tools.UserQuestionAnnotation)
	partial := make(map[string]string, len(request.Questions))
	for index, question := range request.Questions {
		for {
			fmt.Fprintf(o.output, "\nQuestion %d/%d: %s\n", index+1, len(request.Questions), question.Question)
			for optionIndex, option := range question.Options {
				fmt.Fprintf(o.output, "  %d. %s - %s\n", optionIndex+1, option.Label, option.Description)
			}
			if question.MultiSelect {
				fmt.Fprint(o.output, "Choose comma-separated option numbers, or type a custom answer: ")
			} else {
				fmt.Fprint(o.output, "Choose an option number, or type a custom answer: ")
			}
			line, err := o.input.ReadString('\n')
			if err != nil && !errors.Is(err, io.EOF) {
				return tools.UserQuestionResponse{}, fmt.Errorf("read user question: %w", err)
			}
			value := strings.TrimSpace(line)
			switch value {
			case "/cancel":
				return tools.UserQuestionResponse{Outcome: "cancelled"}, nil
			case "/clarify":
				if request.Mode == "plan" {
					return tools.UserQuestionResponse{Outcome: "chat_about_this", PartialAnswers: partial}, nil
				}
			case "/skip":
				if request.Mode == "plan" {
					return tools.UserQuestionResponse{Outcome: "skip_interview", PartialAnswers: partial}, nil
				}
			}
			selected, annotation, parseErr := tools.ParseUserQuestionAnswer(question, value)
			if parseErr != nil {
				if errors.Is(err, io.EOF) {
					return tools.UserQuestionResponse{Outcome: "cancelled"}, nil
				}
				fmt.Fprintln(o.output, "Invalid answer:", parseErr)
				continue
			}
			answers[question.Question] = selected
			partial[question.Question] = strings.Join(selected, ", ")
			if annotation.Preview != "" || annotation.Notes != "" {
				annotations[question.Question] = annotation
			}
			break
		}
		select {
		case <-ctx.Done():
			return tools.UserQuestionResponse{}, ctx.Err()
		default:
		}
	}
	return tools.UserQuestionResponse{Outcome: "accepted", Answers: answers, Annotations: annotations}, nil
}
