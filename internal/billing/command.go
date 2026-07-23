package billing

import (
	"fmt"
	"strings"
)

const ManageURL = "https://grok.com/?_s=usage"

type CommandAction uint8

const (
	ShowUsage CommandAction = iota + 1
	ManageUsage
	InvalidUsage
)

type Command struct {
	Action  CommandAction
	Message string
}

func ParseCommand(prompt string) (Command, bool) {
	fields := strings.Fields(prompt)
	if len(fields) == 0 || fields[0] != "/usage" && fields[0] != "/cost" {
		return Command{}, false
	}
	arg := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(prompt), fields[0]))
	switch arg {
	case "", "show":
		return Command{Action: ShowUsage}, true
	case "manage":
		return Command{Action: ManageUsage}, true
	default:
		return Command{Action: InvalidUsage, Message: fmt.Sprintf("Unknown argument: %s. Use /usage show or /usage manage", arg)}, true
	}
}
