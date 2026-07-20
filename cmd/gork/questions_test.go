package main

import (
	"bufio"
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/lookcorner/go-cli/internal/tools"
)

func TestTerminalQuestionObserverCollectsAnswers(t *testing.T) {
	var output strings.Builder
	observer := &terminalQuestionObserver{
		input: bufio.NewReader(strings.NewReader("1\ncustom deployment\n")), output: &output, mu: &sync.Mutex{},
	}
	response, err := observer.AskUserQuestion(context.Background(), tools.UserQuestionRequest{Questions: []tools.UserQuestion{
		{Question: "Database?", Options: []tools.UserQuestionOption{{Label: "SQLite", Description: "Local"}}},
		{Question: "Target?", Options: []tools.UserQuestionOption{{Label: "Cloud", Description: "Remote"}}},
	}})
	if err != nil || response.Outcome != "accepted" || response.Answers["Database?"][0] != "SQLite" || response.Answers["Target?"][0] != "Other" || response.Annotations["Target?"].Notes != "custom deployment" {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	if !strings.Contains(output.String(), "Question 1/2") || !strings.Contains(output.String(), "SQLite - Local") {
		t.Fatalf("output=%q", output.String())
	}
}

func TestTerminalQuestionObserverPlanSkipAndEOF(t *testing.T) {
	request := tools.UserQuestionRequest{Mode: "plan", Questions: []tools.UserQuestion{
		{Question: "First?", Options: []tools.UserQuestionOption{{Label: "A"}}},
		{Question: "Second?", Options: []tools.UserQuestionOption{{Label: "B"}}},
	}}
	observer := &terminalQuestionObserver{input: bufio.NewReader(strings.NewReader("1\n/skip\n")), output: &strings.Builder{}, mu: &sync.Mutex{}}
	response, err := observer.AskUserQuestion(context.Background(), request)
	if err != nil || response.Outcome != "skip_interview" || response.PartialAnswers["First?"] != "A" {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	observer = &terminalQuestionObserver{input: bufio.NewReader(strings.NewReader("")), output: &strings.Builder{}, mu: &sync.Mutex{}}
	response, err = observer.AskUserQuestion(context.Background(), request)
	if err != nil || response.Outcome != "cancelled" {
		t.Fatalf("EOF response=%#v err=%v", response, err)
	}
}
