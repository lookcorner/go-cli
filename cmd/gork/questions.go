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

type terminalReadResult struct {
	line string
	err  error
}

type terminalReadRequest struct {
	interaction bool
	ctx         context.Context
	reply       chan terminalReadResult
}

type terminalReadWaiter struct {
	ctx   context.Context
	reply chan terminalReadResult
}

type terminalInput struct {
	ctx      context.Context
	requests chan terminalReadRequest
}

func newTerminalInput(ctx context.Context, reader *bufio.Reader) *terminalInput {
	input := &terminalInput{ctx: ctx, requests: make(chan terminalReadRequest, 16)}
	raw := make(chan terminalReadResult, 1)
	go func() {
		defer close(raw)
		for {
			line, err := reader.ReadString('\n')
			select {
			case raw <- terminalReadResult{line: line, err: err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	go input.route(raw)
	return input
}

func (i *terminalInput) route(raw <-chan terminalReadResult) {
	var pending []terminalReadResult
	var interactions, prompts []terminalReadWaiter
	closed := false
	register := func(request terminalReadRequest) {
		if closed && len(pending) == 0 {
			if err := request.ctx.Err(); err != nil {
				request.reply <- terminalReadResult{err: err}
			} else {
				request.reply <- terminalReadResult{err: io.EOF}
			}
		} else if request.interaction {
			interactions = append(interactions, terminalReadWaiter{ctx: request.ctx, reply: request.reply})
		} else {
			prompts = append(prompts, terminalReadWaiter{ctx: request.ctx, reply: request.reply})
		}
	}
	pop := func(waiters *[]terminalReadWaiter) (terminalReadWaiter, bool) {
		for len(*waiters) > 0 {
			waiter := (*waiters)[0]
			*waiters = (*waiters)[1:]
			if err := waiter.ctx.Err(); err != nil {
				waiter.reply <- terminalReadResult{err: err}
				continue
			}
			return waiter, true
		}
		return terminalReadWaiter{}, false
	}
	flush := func() {
		for len(pending) > 0 && (len(interactions) > 0 || len(prompts) > 0) {
			waiter, ok := pop(&interactions)
			if !ok {
				waiter, ok = pop(&prompts)
			}
			if !ok {
				break
			}
			waiter.reply <- pending[0]
			pending = pending[1:]
		}
		if closed && len(pending) == 0 {
			for _, waiter := range append(interactions, prompts...) {
				if err := waiter.ctx.Err(); err != nil {
					waiter.reply <- terminalReadResult{err: err}
				} else {
					waiter.reply <- terminalReadResult{err: io.EOF}
				}
			}
			interactions, prompts = nil, nil
		}
	}
	for {
		for draining := true; draining; {
			select {
			case request := <-i.requests:
				register(request)
			default:
				draining = false
			}
		}
		flush()
		select {
		case request := <-i.requests:
			register(request)
		case result, ok := <-raw:
			if !ok {
				raw, closed = nil, true
			} else {
				pending = append(pending, result)
			}
		case <-i.ctx.Done():
			for _, waiter := range append(interactions, prompts...) {
				waiter.reply <- terminalReadResult{err: i.ctx.Err()}
			}
			return
		}
	}
}

func (i *terminalInput) request(ctx context.Context, interaction bool) <-chan terminalReadResult {
	reply := make(chan terminalReadResult, 1)
	select {
	case i.requests <- terminalReadRequest{interaction: interaction, ctx: ctx, reply: reply}:
	case <-ctx.Done():
		reply <- terminalReadResult{err: ctx.Err()}
	case <-i.ctx.Done():
		reply <- terminalReadResult{err: i.ctx.Err()}
	}
	return reply
}

type terminalPrompter struct {
	input  *terminalInput
	output io.Writer
	mu     sync.Mutex
}

func (p *terminalPrompter) Approve(ctx context.Context, action, detail string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	read := p.input.request(ctx, true)
	fmt.Fprintf(p.output, "\nAllow %s?\n  %s\n[y/N] ", action, detail)
	var result terminalReadResult
	select {
	case result = <-read:
	case <-ctx.Done():
		return ctx.Err()
	}
	if result.err != nil && !errors.Is(result.err, io.EOF) {
		return fmt.Errorf("read approval: %w", result.err)
	}
	answer := strings.ToLower(strings.TrimSpace(result.line))
	if answer == "y" || answer == "yes" {
		return nil
	}
	return &tools.PermissionDeniedError{Action: action}
}

func (p *terminalPrompter) AskUserQuestion(ctx context.Context, request tools.UserQuestionRequest) (tools.UserQuestionResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	answers := make(map[string][]string, len(request.Questions))
	annotations := make(map[string]tools.UserQuestionAnnotation)
	partial := make(map[string]string, len(request.Questions))
	for index, question := range request.Questions {
		for {
			read := p.input.request(ctx, true)
			fmt.Fprintf(p.output, "\nQuestion %d/%d: %s\n", index+1, len(request.Questions), question.Question)
			for optionIndex, option := range question.Options {
				fmt.Fprintf(p.output, "  %d. %s - %s\n", optionIndex+1, option.Label, option.Description)
			}
			if question.MultiSelect {
				fmt.Fprint(p.output, "Choose comma-separated option numbers, or type a custom answer: ")
			} else {
				fmt.Fprint(p.output, "Choose an option number, or type a custom answer: ")
			}
			var result terminalReadResult
			select {
			case result = <-read:
			case <-ctx.Done():
				return tools.UserQuestionResponse{}, ctx.Err()
			}
			if result.err != nil && !errors.Is(result.err, io.EOF) {
				return tools.UserQuestionResponse{}, fmt.Errorf("read user question: %w", result.err)
			}
			value := strings.TrimSpace(result.line)
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
				if errors.Is(result.err, io.EOF) {
					return tools.UserQuestionResponse{Outcome: "cancelled"}, nil
				}
				fmt.Fprintln(p.output, "Invalid answer:", parseErr)
				continue
			}
			answers[question.Question] = selected
			partial[question.Question] = strings.Join(selected, ", ")
			if annotation.Preview != "" || annotation.Notes != "" {
				annotations[question.Question] = annotation
			}
			break
		}
	}
	return tools.UserQuestionResponse{Outcome: "accepted", Answers: answers, Annotations: annotations}, nil
}
