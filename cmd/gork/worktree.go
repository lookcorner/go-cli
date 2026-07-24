package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	worktrees "github.com/lookcorner/go-cli/internal/worktree"
)

func runWorktree(args []string, stdout, stderr io.Writer) error {
	manager, err := worktrees.NewManager("")
	if err != nil {
		return err
	}
	return runWorktreeCommand(context.Background(), manager, args, stdout, stderr)
}

func runWorktreeCommand(ctx context.Context, manager *worktrees.Manager, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		worktreeUsage(stderr)
		return errors.New("worktree command is required")
	}
	switch args[0] {
	case "list", "ls":
		return runWorktreeList(manager, args[1:], stdout, stderr)
	case "show":
		if len(args) != 2 {
			return errors.New("usage: gork worktree show <id-or-path>")
		}
		record, ok := manager.Show(args[1])
		if !ok {
			return fmt.Errorf("worktree not found: %s", cleanWorktreeText(args[1]))
		}
		printWorktree(stdout, record)
		return nil
	case "rm":
		return runWorktreeRemove(ctx, manager, args[1:], stdout, stderr)
	case "gc", "prune":
		return runWorktreeGC(ctx, manager, args[1:], stdout)
	case "db":
		return runWorktreeDB(ctx, manager, args[1:], stdout)
	default:
		worktreeUsage(stderr)
		return fmt.Errorf("unknown worktree command %q", cleanWorktreeText(args[0]))
	}
}

func worktreeUsage(output io.Writer) {
	fmt.Fprintln(output, "Usage: gork worktree list [--repo name] [--type kind[,kind]] [--json] [--all]")
	fmt.Fprintln(output, "       gork worktree show <id-or-path>")
	fmt.Fprintln(output, "       gork worktree rm <id-or-path>... [-f|--force] [--dry-run]")
	fmt.Fprintln(output, "       gork worktree gc [--dry-run] [--max-age 7d] [-f|--force]")
	fmt.Fprintln(output, "       gork worktree db <stats|path|rebuild>")
}

type worktreeTypes []string

func (f *worktreeTypes) String() string { return strings.Join(*f, ",") }
func (f *worktreeTypes) Set(value string) error {
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			*f = append(*f, item)
		}
	}
	return nil
}

func runWorktreeList(manager *worktrees.Manager, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("gork worktree list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var repo string
	var types worktreeTypes
	var asJSON, all bool
	flags.StringVar(&repo, "repo", "", "filter by repository path or name")
	flags.Var(&types, "type", "filter by worktree kind; repeat or comma-separate")
	flags.BoolVar(&asJSON, "json", false, "print JSON")
	flags.BoolVar(&all, "all", false, "include dead records")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected worktree list argument %q", cleanWorktreeText(flags.Arg(0)))
	}
	records := manager.List(repo, types, all)
	if asJSON {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(records)
	}
	printWorktreeTable(stdout, records)
	return nil
}

func runWorktreeRemove(ctx context.Context, manager *worktrees.Manager, args []string, stdout, stderr io.Writer) error {
	var ids []string
	var force, dryRun bool
	positional := false
	for _, arg := range args {
		if positional {
			ids = append(ids, arg)
			continue
		}
		switch arg {
		case "--":
			positional = true
		case "-f", "--force":
			force = true
		case "--dry-run":
			dryRun = true
		default:
			if strings.HasPrefix(arg, "-") {
				return fmt.Errorf("unknown worktree rm option %q", cleanWorktreeText(arg))
			}
			ids = append(ids, arg)
		}
	}
	if len(ids) == 0 {
		return errors.New("usage: gork worktree rm <id-or-path>... [-f|--force] [--dry-run]")
	}
	for _, id := range ids {
		removed, path, err := manager.Remove(ctx, worktrees.RemoveRequest{IDOrPath: id, Force: force, DryRun: dryRun})
		if err != nil {
			fmt.Fprintf(stderr, "  error removing %s: %s\n", cleanWorktreeText(id), cleanWorktreeText(err.Error()))
			continue
		}
		path = cleanWorktreeText(path)
		if dryRun {
			fmt.Fprintf(stdout, "  would remove: %s\n", path)
		} else if removed {
			fmt.Fprintf(stdout, "  removed: %s\n", path)
		}
	}
	return nil
}

func runWorktreeGC(ctx context.Context, manager *worktrees.Manager, args []string, stdout io.Writer) error {
	var dryRun, force bool
	var maxAgeText string
	var maxAgeSet bool
	for index := 0; index < len(args); index++ {
		switch arg := args[index]; {
		case arg == "--dry-run":
			dryRun = true
		case arg == "-f" || arg == "--force":
			force = true
		case arg == "--max-age":
			index++
			if index == len(args) {
				return errors.New("--max-age requires a duration")
			}
			maxAgeText = args[index]
			maxAgeSet = true
		case strings.HasPrefix(arg, "--max-age="):
			maxAgeText = strings.TrimPrefix(arg, "--max-age=")
			maxAgeSet = true
		default:
			return fmt.Errorf("unknown worktree gc option %q", cleanWorktreeText(arg))
		}
	}
	var maxAge *time.Duration
	if maxAgeSet {
		value, err := parseWorktreeAge(maxAgeText)
		if err != nil {
			return err
		}
		maxAge = &value
	}
	report, err := manager.GC(ctx, dryRun, maxAge, force)
	if err != nil {
		return err
	}
	if dryRun {
		fmt.Fprintln(stdout, "Dry run — no changes made.")
	}
	fmt.Fprintln(stdout, "GC report:")
	fmt.Fprintf(stdout, "  Dead records removed:      %d\n", report.DeadRemoved)
	fmt.Fprintf(stdout, "  Expired worktrees removed: %d\n", report.ExpiredRemoved)
	fmt.Fprintf(stdout, "  Skipped (alive process):   %d\n", report.SkippedAlive)
	if report.RemoveFailed > 0 {
		fmt.Fprintf(stdout, "  Removal failures:          %d\n", report.RemoveFailed)
	}
	return nil
}

func parseWorktreeAge(value string) (time.Duration, error) {
	duration, err := time.ParseDuration(value)
	if err != nil && strings.HasSuffix(value, "d") {
		days, dayErr := time.ParseDuration(strings.TrimSuffix(value, "d") + "h")
		if dayErr == nil && days <= time.Duration(1<<63-1)/24 && days >= time.Duration(-1<<63)/24 {
			duration, err = days*24, nil
		}
	}
	if err != nil {
		return 0, errors.New("invalid max age; expected e.g. 7d, 24h, 30m, or 60s")
	}
	return duration, nil
}

func runWorktreeDB(ctx context.Context, manager *worktrees.Manager, args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: gork worktree db <stats|path|rebuild>")
	}
	switch args[0] {
	case "stats":
		stats := manager.Stats()
		fmt.Fprintln(stdout, "Worktree DB Statistics")
		fmt.Fprintln(stdout, "======================")
		fmt.Fprintf(stdout, "  Total records: %d\n", stats.TotalRecords)
		fmt.Fprintf(stdout, "  Alive:         %d\n", stats.AliveCount)
		fmt.Fprintf(stdout, "  Dead:          %d\n", stats.DeadCount)
		fmt.Fprintf(stdout, "  DB size:       %s\n", formatWorktreeBytes(stats.DBFileBytes))
	case "path":
		fmt.Fprintln(stdout, manager.StatePath())
	case "rebuild":
		report, err := manager.Rebuild(ctx)
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, "Rebuild report:")
		fmt.Fprintf(stdout, "  Discovered:      %d\n", report.Discovered)
		fmt.Fprintf(stdout, "  Registered:      %d\n", report.Registered)
		fmt.Fprintf(stdout, "  Already tracked: %d\n", report.AlreadyTracked)
	default:
		return fmt.Errorf("unknown worktree db command %q", cleanWorktreeText(args[0]))
	}
	return nil
}

func printWorktreeTable(output io.Writer, records []worktrees.Record) {
	if len(records) == 0 {
		fmt.Fprintln(output, "No worktrees found.")
		return
	}
	idWidth, labelWidth := 16, 5
	counts := make(map[string]int)
	for _, record := range records {
		idWidth = max(idWidth, len([]rune(record.ID)))
		labelWidth = min(24, max(labelWidth, len([]rune(record.Label))))
		counts[record.Kind]++
	}
	fmt.Fprintf(output, "  %-*s %-8s %-6s %-*s %-20s %-10s PATH\n", idWidth, "ID", "TYPE", "REPO", labelWidth, "LABEL", "BRANCH", "AGE")
	for _, record := range records {
		branch := record.GitRef
		if branch == "" {
			branch = "(detached)"
		}
		fmt.Fprintf(output, "  %-*s %-8s %-6s %-*s %-20s %-10s %s\n",
			idWidth, cleanWorktreeText(record.ID),
			truncateWorktreeText(cleanWorktreeText(record.Kind), 8),
			truncateWorktreeText(cleanWorktreeText(record.RepoName), 6),
			labelWidth, truncateWorktreeText(cleanWorktreeText(record.Label), labelWidth),
			truncateWorktreeText(cleanWorktreeText(branch), 20),
			formatWorktreeAge(record.CreatedAt),
			abbreviateWorktreePath(record.Path),
		)
	}
	kinds := make([]string, 0, len(counts))
	for kind := range counts {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	parts := make([]string, len(kinds))
	for index, kind := range kinds {
		parts[index] = fmt.Sprintf("%d %s", counts[kind], cleanWorktreeText(kind))
	}
	fmt.Fprintf(output, "  %d worktrees (%s)\n", len(records), strings.Join(parts, ", "))
}

func printWorktree(output io.Writer, record worktrees.Record) {
	fmt.Fprintf(output, "  Path:           %s\n", cleanWorktreeText(record.Path))
	fmt.Fprintf(output, "  ID:             %s\n", cleanWorktreeText(record.ID))
	fmt.Fprintf(output, "  Type:           %s\n", cleanWorktreeText(record.Kind))
	fmt.Fprintf(output, "  Source Repo:    %s\n", cleanWorktreeText(record.SourceRepo))
	fmt.Fprintf(output, "  Creation Mode:  %s\n", cleanWorktreeText(record.CreationMode))
	if record.GitRef != "" {
		fmt.Fprintf(output, "  Git Ref:        %s\n", cleanWorktreeText(record.GitRef))
	}
	if record.HeadCommit != "" {
		commit := []rune(cleanWorktreeText(record.HeadCommit))
		if len(commit) > 12 {
			commit = commit[:12]
		}
		fmt.Fprintf(output, "  HEAD:           %s\n", string(commit))
	}
	fmt.Fprintf(output, "  Created:        %s\n", record.CreatedAt.UTC().Format("2006-01-02 15:04:05 UTC"))
	fmt.Fprintf(output, "  Last Accessed:  %s\n", record.LastAccessedAt.UTC().Format("2006-01-02 15:04:05 UTC"))
	if record.SessionID != "" {
		fmt.Fprintf(output, "  Session ID:     %s\n", cleanWorktreeText(record.SessionID))
	}
	if record.CreatorPID != 0 {
		fmt.Fprintf(output, "  Creator PID:    %d\n", record.CreatorPID)
	}
	fmt.Fprintf(output, "  Status:         %s\n", cleanWorktreeText(record.Status))
	if record.Label != "" {
		fmt.Fprintf(output, "  Label:          %s\n", cleanWorktreeText(record.Label))
	}
	if size, err := worktreeDirSize(record.Path); err == nil {
		fmt.Fprintf(output, "  Disk Usage:     %s\n", formatWorktreeBytes(size))
	}
}

func worktreeDirSize(root string) (uint64, error) {
	var total uint64
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += uint64(info.Size())
		return nil
	})
	return total, err
}

func formatWorktreeAge(created time.Time) string {
	seconds := max(int64(0), int64(time.Since(created).Seconds()))
	switch {
	case seconds < 60:
		return fmt.Sprintf("%ds ago", seconds)
	case seconds < 3600:
		return fmt.Sprintf("%dm ago", seconds/60)
	case seconds < 86400:
		return fmt.Sprintf("%dh ago", seconds/3600)
	default:
		return fmt.Sprintf("%dd ago", seconds/86400)
	}
}

func formatWorktreeBytes(bytes uint64) string {
	if bytes == 0 {
		return "0 B"
	}
	value := float64(bytes)
	for _, unit := range []string{"B", "KB", "MB", "GB"} {
		if value < 1024 {
			return fmt.Sprintf("%.1f %s", value, unit)
		}
		value /= 1024
	}
	return fmt.Sprintf("%.1f TB", value)
}

func truncateWorktreeText(value string, width int) string {
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	if width < 2 {
		return string(runes[:max(width, 0)])
	}
	return string(runes[:width-1]) + "…"
}

func abbreviateWorktreePath(path string) string {
	path = cleanWorktreeText(path)
	home, err := os.UserHomeDir()
	if err == nil && (path == home || strings.HasPrefix(path, home+string(filepath.Separator))) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}

func cleanWorktreeText(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value)
}
