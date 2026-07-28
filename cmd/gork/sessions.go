package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	sessionlog "github.com/lookcorner/go-cli/internal/session"
	worktrees "github.com/lookcorner/go-cli/internal/worktree"
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
		items, err := listWorkspaceSessions(dir, cwd)
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
		result, err := searchWorkspaceSessions(dir, cwd, query, limit)
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
	groups := make(map[string][]sessionlog.Info)
	for _, item := range items {
		groups[item.WorktreeLabel] = append(groups[item.WorktreeLabel], item)
	}
	labels := make([]string, 0, len(groups))
	for label := range groups {
		if label != "" {
			labels = append(labels, label)
		}
	}
	sort.Strings(labels)
	if _, ok := groups[""]; ok {
		labels = append(labels, "")
	}
	for _, label := range labels {
		if label == "" {
			fmt.Fprintln(output, "\n(no label)")
		} else {
			fmt.Fprintf(output, "\nLabel: %s\n", cleanCLIText(label))
		}
		fmt.Fprintf(output, "%-36s  %-10s  %-10s  %-10s  %s\n", "SESSION ID", "CREATED", "UPDATED", "STATUS", "SUMMARY")
		for _, item := range groups[label] {
			title := sessionLine(item.Title)
			if title == "" {
				title = "(no summary)"
			}
			status := item.Source
			if status == "" {
				status = "local"
			}
			fmt.Fprintf(output, "%-36s  %-10s  %-10s  %-10s  %s\n",
				cleanCLIText(item.SessionID),
				sessionDate(item.CreatedAt),
				sessionDate(item.UpdatedAt),
				cleanCLIText(status),
				truncateCLIText(title, 50),
			)
		}
	}
}

func listWorkspaceSessions(dir, cwd string) ([]sessionlog.Info, error) {
	all, err := sessionlog.List(dir, "")
	if err != nil {
		return nil, err
	}
	paths, labels := workspaceSessionScope(dir, cwd)
	items := all[:0]
	for _, item := range all {
		path := cleanSessionPath(item.CWD)
		if !paths[path] {
			continue
		}
		if item.WorktreeLabel == "" {
			item.WorktreeLabel = labels[path]
		}
		items = append(items, item)
	}
	return items, nil
}

func searchWorkspaceSessions(dir, cwd, query string, limit int) (sessionlog.SearchResult, error) {
	paths, _ := workspaceSessionScope(dir, cwd)
	cwds := make([]string, 0, len(paths))
	for path := range paths {
		cwds = append(cwds, path)
	}
	sort.Strings(cwds)
	byID := make(map[string]sessionlog.SearchHit)
	for _, path := range cwds {
		result, err := sessionlog.Search(dir, sessionlog.SearchRequest{
			Query: query, CWD: path, Limit: 100, IncludeContent: true,
		})
		if err != nil {
			return sessionlog.SearchResult{}, err
		}
		for _, hit := range result.Results {
			byID[hit.SessionID] = hit
		}
	}
	hits := make([]sessionlog.SearchHit, 0, len(byID))
	for _, hit := range byID {
		hits = append(hits, hit)
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		if hits[i].UpdatedAt != hits[j].UpdatedAt {
			return hits[i].UpdatedAt > hits[j].UpdatedAt
		}
		return hits[i].SessionID < hits[j].SessionID
	})
	total := len(hits)
	if total > limit {
		hits = hits[:limit]
	}
	return sessionlog.SearchResult{Results: hits, TotalEstimate: &total}, nil
}

func workspaceSessionScope(dir, cwd string) (map[string]bool, map[string]string) {
	current := cleanSessionPath(cwd)
	paths := map[string]bool{current: true}
	labels := make(map[string]string)
	manager, err := worktrees.NewManager(dir)
	if err != nil {
		return paths, labels
	}
	records := manager.List("", nil, false)
	repo := ""
	for _, record := range records {
		path := cleanSessionPath(record.Path)
		source := cleanSessionPath(record.SourceRepo)
		if current == path || current == source {
			repo = source
			break
		}
	}
	if repo == "" {
		return paths, labels
	}
	paths[repo] = true
	for _, record := range records {
		if cleanSessionPath(record.SourceRepo) != repo {
			continue
		}
		path := cleanSessionPath(record.Path)
		paths[path] = true
		labels[path] = strings.TrimSpace(record.Label)
	}
	return paths, labels
}

func cleanSessionPath(path string) string {
	cleaned, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(cleaned); resolveErr == nil {
		cleaned = resolved
	}
	return filepath.Clean(cleaned)
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
