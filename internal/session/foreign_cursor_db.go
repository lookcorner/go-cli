package session

import (
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	cursorMaxDBCandidates = 200
	cursorMaxHeaderBytes  = 64 << 10
)

type cursorHeader struct {
	Name      string `json:"name"`
	Subtitle  string `json:"subtitle"`
	Workspace struct {
		URI struct {
			FSPath string `json:"fsPath"`
			Path   string `json:"path"`
		} `json:"uri"`
	} `json:"workspaceIdentifier"`
	TrackedRepos []struct {
		RepoPath   string `json:"repoPath"`
		BranchName string `json:"branchName"`
	} `json:"trackedGitRepos"`
}

func scanCursorDesktop(cwd string, now time.Time) []ForeignSummary {
	root := cursorUserDataDir()
	path := filepath.Join(root, "globalStorage", "state.vscdb")
	if !safeForeignDir(root) || !safeForeignDir(filepath.Dir(path)) || !safeForeignFile(path) {
		return nil
	}
	databaseURL := (&url.URL{Scheme: "file", Path: path}).String() + "?mode=ro"
	database, err := sql.Open("sqlite", databaseURL)
	if err != nil {
		return nil
	}
	defer database.Close()
	database.SetMaxOpenConns(1)

	columns, ok := cursorComposerColumns(database)
	if !ok {
		return nil
	}
	for _, required := range []string{"composerId", "createdAt", "lastUpdatedAt", "isArchived", "isSubagent", "recency", "value"} {
		if !columns[required] {
			return nil
		}
	}
	oldest, newest := now.Add(-foreignMaxAge).UnixMilli(), now.Add(5*time.Minute).UnixMilli()
	rows, err := database.Query(`SELECT composerId,
		CASE WHEN typeof(lastUpdatedAt) = 'integer' THEN lastUpdatedAt ELSE createdAt END,
		value
		FROM composerHeaders
		WHERE typeof(composerId) = 'text' AND length(CAST(composerId AS blob)) <= 64
		  AND typeof(createdAt) = 'integer'
		  AND (lastUpdatedAt IS NULL OR typeof(lastUpdatedAt) = 'integer')
		  AND typeof(isArchived) = 'integer' AND isArchived = 0
		  AND typeof(isSubagent) = 'integer' AND isSubagent = 0
		  AND typeof(value) = 'text' AND length(CAST(value AS blob)) <= ?
		  AND CASE WHEN json_valid(value) THEN
		        COALESCE(json_extract(value, '$.workspaceIdentifier.uri.fsPath'),
		                 json_extract(value, '$.workspaceIdentifier.uri.path'))
		      ELSE NULL END = ?
		  AND CASE WHEN typeof(lastUpdatedAt) = 'integer' THEN lastUpdatedAt ELSE createdAt END BETWEEN ? AND ?
		ORDER BY CASE WHEN typeof(lastUpdatedAt) = 'integer' THEN lastUpdatedAt ELSE createdAt END DESC,
		         recency DESC, composerId ASC
		LIMIT ?`, cursorMaxHeaderBytes, cwd, oldest, newest, cursorMaxDBCandidates)
	if err != nil {
		return nil
	}
	defer rows.Close()

	summaries := make([]ForeignSummary, 0, foreignMaxItems)
	for rows.Next() {
		var id, raw string
		var updatedMillis int64
		if rows.Scan(&id, &updatedMillis, &raw) != nil || !foreignUUID.MatchString(id) {
			continue
		}
		updatedAt := time.UnixMilli(updatedMillis)
		if !foreignRecent(updatedAt, now) {
			continue
		}
		var header cursorHeader
		if json.Unmarshal([]byte(raw), &header) != nil {
			continue
		}
		storedCWD := firstNonEmpty(header.Workspace.URI.FSPath, header.Workspace.URI.Path)
		if storedCWD != cwd {
			continue
		}
		title := firstNonEmpty(header.Name, header.Subtitle)
		if title == "" {
			continue
		}
		branch := ""
		for _, repo := range header.TrackedRepos {
			if repo.RepoPath == cwd {
				branch = firstNonEmpty(repo.BranchName)
				break
			}
		}
		summaries = append(summaries, ForeignSummary{
			ID: id, Source: "cursor", Title: title, CWD: storedCWD,
			Branch: branch, UpdatedAt: updatedAt.UTC(),
		})
		if len(summaries) == foreignMaxItems {
			break
		}
	}
	if rows.Err() != nil {
		return nil
	}
	return summaries
}

func cursorUserDataDir() string {
	if root := strings.TrimSpace(os.Getenv("CURSOR_USER_DATA_DIR")); root != "" {
		return root
	}
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Cursor", "User")
	case "windows":
		if appData := strings.TrimSpace(os.Getenv("APPDATA")); appData != "" {
			return filepath.Join(appData, "Cursor", "User")
		}
		return filepath.Join(home, "AppData", "Roaming", "Cursor", "User")
	default:
		if configHome := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); configHome != "" {
			return filepath.Join(configHome, "Cursor", "User")
		}
		return filepath.Join(home, ".config", "Cursor", "User")
	}
}

func cursorComposerColumns(database *sql.DB) (map[string]bool, bool) {
	rows, err := database.Query("PRAGMA table_info(composerHeaders)")
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
