package session

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	codexMaxStateGeneration = 128
	codexMaxDBCandidates    = 200
	codexMinEpochMillis     = int64(1_577_836_800_000)
)

func scanCodexDatabases(home, cwd string, now time.Time) []ForeignSummary {
	if !safeForeignDir(home) {
		return nil
	}
	for generation := codexMaxStateGeneration; generation >= 0; generation-- {
		path := filepath.Join(home, fmt.Sprintf("state_%d.sqlite", generation))
		if !safeForeignFile(path) {
			continue
		}
		if summaries, ok := scanCodexDatabase(home, path, cwd, now); ok && len(summaries) > 0 {
			return summaries
		}
	}
	return nil
}

func scanCodexDatabase(home, path, cwd string, now time.Time) ([]ForeignSummary, bool) {
	databaseURL := (&url.URL{Scheme: "file", Path: path}).String() + "?mode=ro"
	database, err := sql.Open("sqlite", databaseURL)
	if err != nil {
		return nil, false
	}
	defer database.Close()
	database.SetMaxOpenConns(1)

	columns, ok := codexThreadColumns(database)
	if !ok {
		return nil, false
	}
	updated := "updated_at"
	if columns["updated_at_ms"] {
		updated = "updated_at_ms"
	} else if !columns[updated] {
		return nil, false
	}
	for _, required := range []string{"id", "rollout_path", "source", "cwd", "archived"} {
		if !columns[required] {
			return nil, false
		}
	}
	textColumn := func(name string) string {
		if columns[name] {
			return "CASE WHEN typeof(" + name + ") = 'text' AND length(CAST(" + name + " AS blob)) <= 65536 THEN " + name + " ELSE '' END"
		}
		return "''"
	}
	branch := "NULL"
	if columns["git_branch"] {
		branch = "CASE WHEN typeof(git_branch) = 'text' AND length(CAST(git_branch AS blob)) <= 4096 THEN git_branch ELSE NULL END"
	}
	query := fmt.Sprintf(`SELECT id, rollout_path, %s, source, cwd, %s, %s, %s
		FROM threads
		WHERE typeof(id) = 'text' AND typeof(rollout_path) = 'text'
		  AND typeof(%s) = 'integer' AND typeof(archived) = 'integer'
		  AND archived = 0 AND cwd = ?
		  AND source IN ('cli', 'vscode', '{"custom":"atlas"}', '{"custom":"chatgpt"}')
		  AND length(CAST(id AS blob)) <= 64 AND length(CAST(rollout_path AS blob)) <= 16384
		  AND CASE WHEN %s < %d THEN %s * 1000 ELSE %s END BETWEEN ? AND ?
		ORDER BY CASE WHEN %s < %d THEN %s * 1000 ELSE %s END DESC, id ASC
		LIMIT %d`, updated, textColumn("title"), textColumn("first_user_message"), branch,
		updated, updated, codexMinEpochMillis, updated, updated,
		updated, codexMinEpochMillis, updated, updated, codexMaxDBCandidates)
	oldest := now.Add(-foreignMaxAge).UnixMilli()
	newest := now.Add(5 * time.Minute).UnixMilli()
	rows, err := database.Query(query, cwd, oldest, newest)
	if err != nil {
		return nil, false
	}
	defer rows.Close()

	summaries := make([]ForeignSummary, 0, foreignMaxItems)
	for rows.Next() {
		var id, rollout, source, storedCWD, title, first string
		var updatedValue int64
		var branch sql.NullString
		if rows.Scan(&id, &rollout, &updatedValue, &source, &storedCWD, &title, &first, &branch) != nil {
			continue
		}
		if !foreignUUID.MatchString(id) || storedCWD != cwd || !codexDatabaseSource(source) || !validCodexRollout(home, rollout, id) {
			continue
		}
		updatedAt := codexDatabaseTime(updatedValue)
		if !foreignRecent(updatedAt, now) {
			continue
		}
		title = firstNonEmpty(title, first)
		if title == "" {
			continue
		}
		summaries = append(summaries, ForeignSummary{
			ID: id, Source: "codex", Title: title, CWD: storedCWD,
			Branch: normalizeForeignTitle(branch.String), UpdatedAt: updatedAt.UTC(),
		})
		if len(summaries) == foreignMaxItems {
			break
		}
	}
	return summaries, rows.Err() == nil
}

func codexThreadColumns(database *sql.DB) (map[string]bool, bool) {
	rows, err := database.Query("PRAGMA table_info(threads)")
	if err != nil {
		return nil, false
	}
	defer rows.Close()
	columns := make(map[string]bool)
	for rows.Next() {
		var index int
		var name, dataType string
		var notNull, primaryKey int
		var defaultValue any
		if rows.Scan(&index, &name, &dataType, &notNull, &defaultValue, &primaryKey) != nil {
			return nil, false
		}
		columns[name] = true
	}
	return columns, rows.Err() == nil && len(columns) > 0
}

func codexDatabaseTime(value int64) time.Time {
	if value < codexMinEpochMillis {
		value *= 1000
	}
	return time.UnixMilli(value)
}

func codexDatabaseSource(value string) bool {
	return value == "cli" || value == "vscode" || value == `{"custom":"atlas"}` || value == `{"custom":"chatgpt"}`
}

func validCodexRollout(home, value, id string) bool {
	if strings.TrimSpace(value) == "" {
		return false
	}
	path := filepath.Clean(value)
	if !filepath.IsAbs(path) {
		path = filepath.Join(home, path)
	}
	for _, candidate := range []string{path, path + ".zst"} {
		if codexRolloutID(strings.TrimSuffix(filepath.Base(candidate), ".zst")) != id || !safeForeignFile(candidate) {
			continue
		}
		real, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			continue
		}
		for _, directory := range []string{"sessions", "archived_sessions"} {
			rootPath := filepath.Join(home, directory)
			if !safeForeignDir(rootPath) {
				continue
			}
			root, err := filepath.EvalSymlinks(rootPath)
			if err != nil {
				continue
			}
			relative, err := filepath.Rel(root, real)
			if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
				return true
			}
		}
	}
	return false
}
