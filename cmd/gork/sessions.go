package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	sessionlog "github.com/lookcorner/go-cli/internal/session"
)

func runSessions(args []string, stdout, stderr io.Writer) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	return runSessionsCommand("", cwd, args, stdout, stderr)
}

func runSessionsCommand(dir, cwd string, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		sessionsUsage(stderr)
		return errors.New("sessions command is required")
	}
	switch args[0] {
	case "list":
		limit, query, err := parseSessionsLimit(args[1:])
		if err != nil {
			return err
		}
		if query != "" {
			return fmt.Errorf("unexpected sessions list argument %q", cleanCLIText(query))
		}
		items, err := sessionlog.List(dir, cwd)
		if err != nil {
			return err
		}
		if limit < len(items) {
			items = items[:limit]
		}
		printSessions(stdout, items)
		return nil
	case "search":
		limit, query, err := parseSessionsLimit(args[1:])
		if err != nil {
			return err
		}
		if query == "" {
			return errors.New("usage: gork sessions search <query> [-n|--limit count]")
		}
		if limit == 0 {
			fmt.Fprintln(stdout, "\nTotal: 0")
			return nil
		}
		result, err := sessionlog.Search(dir, sessionlog.SearchRequest{
			Query: query, CWD: cwd, Limit: limit, IncludeContent: true,
		})
		if err != nil {
			return err
		}
		printSessionSearch(stdout, result.Results)
		return nil
	case "delete":
		if len(args) != 2 {
			return errors.New("usage: gork sessions delete <session-id>")
		}
		id := args[1]
		if err := sessionlog.Delete(dir, id); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				fmt.Fprintf(stdout, "No session found with id %s.\n", cleanCLIText(id))
				return nil
			}
			return err
		}
		fmt.Fprintf(stdout, "Deleted session %s\n", cleanCLIText(id))
		return nil
	default:
		sessionsUsage(stderr)
		return fmt.Errorf("unknown sessions command %q", cleanCLIText(args[0]))
	}
}

func sessionsUsage(output io.Writer) {
	fmt.Fprintln(output, "Usage: gork sessions list [-n|--limit count]")
	fmt.Fprintln(output, "       gork sessions search <query> [-n|--limit count]")
	fmt.Fprintln(output, "       gork sessions delete <session-id>")
}

func parseSessionsLimit(args []string) (int, string, error) {
	limit := 20
	query := ""
	for index := 0; index < len(args); index++ {
		arg := args[index]
		var raw string
		var rawSet bool
		switch {
		case arg == "--":
			remaining := args[index+1:]
			if len(remaining) > 1 || len(remaining) == 1 && query != "" {
				return 0, "", errors.New("sessions accepts at most one positional argument")
			}
			if len(remaining) == 1 {
				query = remaining[0]
			}
			return limit, query, nil
		case arg == "-n" || arg == "--limit":
			index++
			if index == len(args) {
				return 0, "", fmt.Errorf("%s requires a count", arg)
			}
			raw = args[index]
			rawSet = true
		case strings.HasPrefix(arg, "--limit="):
			raw = strings.TrimPrefix(arg, "--limit=")
			rawSet = true
		case strings.HasPrefix(arg, "-n="):
			raw = strings.TrimPrefix(arg, "-n=")
			rawSet = true
		case strings.HasPrefix(arg, "-"):
			return 0, "", fmt.Errorf("unknown sessions option %q", cleanCLIText(arg))
		default:
			if query != "" {
				return 0, "", fmt.Errorf("unexpected sessions argument %q", cleanCLIText(arg))
			}
			query = arg
		}
		if rawSet {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 0 {
				return 0, "", errors.New("session limit must be a non-negative integer")
			}
			limit = value
		}
	}
	return limit, query, nil
}

func printSessions(output io.Writer, items []sessionlog.Info) {
	if len(items) == 0 {
		fmt.Fprintln(output, "No sessions found.")
		return
	}
	fmt.Fprintf(output, "%-36s  %-10s  %-10s  %-10s  %s\n", "SESSION ID", "CREATED", "UPDATED", "STATUS", "SUMMARY")
	for _, item := range items {
		title := sessionLine(item.Title)
		if title == "" {
			title = "(no summary)"
		}
		fmt.Fprintf(output, "%-36s  %-10s  %-10s  %-10s  %s\n",
			cleanCLIText(item.SessionID),
			sessionDate(item.CreatedAt),
			sessionDate(item.UpdatedAt),
			"local",
			truncateCLIText(title, 50),
		)
	}
}

func printSessionSearch(output io.Writer, hits []sessionlog.SearchHit) {
	for _, hit := range hits {
		title := sessionLine(hit.Summary)
		if title == "" {
			title = "(untitled)"
		}
		updated, _ := time.Parse(time.RFC3339, hit.UpdatedAt)
		fmt.Fprintf(output, "%s (score: %.2f)  %s\n  %s\n  %s\n",
			cleanCLIText(hit.SessionID),
			hit.Score,
			updated.Local().Format("Jan 02,  3:04pm"),
			title,
			sessionLine(valueOrEmpty(hit.Snippet)),
		)
	}
	fmt.Fprintf(output, "\nTotal: %d\n", len(hits))
}

func sessionDate(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Local().Format("2006-01-02")
}

func sessionLine(value string) string {
	return cleanCLIText(strings.Join(strings.Fields(value), " "))
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
