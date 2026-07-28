package session

import (
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const cursorCLIMaxSessions = 64

type cursorCLIMeta struct {
	AgentID   string `json:"agentId"`
	Name      string `json:"name"`
	CreatedAt int64  `json:"createdAt"`
}

// scanCursor merges Desktop composerHeaders with Cursor CLI chat stores under
// ~/.cursor/chats/<md5(cwd)>/<uuid>/store.db.
func scanCursor(cwd string, now time.Time) []ForeignSummary {
	desktop := scanCursorDesktop(cwd, now)
	cli := scanCursorCLI(cwd, now)
	if len(desktop) == 0 {
		return cli
	}
	if len(cli) == 0 {
		return desktop
	}
	byID := make(map[string]ForeignSummary, len(desktop)+len(cli))
	order := make([]string, 0, len(desktop)+len(cli))
	add := func(item ForeignSummary) {
		if existing, ok := byID[item.ID]; ok {
			if item.UpdatedAt.After(existing.UpdatedAt) {
				byID[item.ID] = item
			}
			return
		}
		byID[item.ID] = item
		order = append(order, item.ID)
	}
	for _, item := range desktop {
		add(item)
	}
	for _, item := range cli {
		add(item)
	}
	out := make([]ForeignSummary, 0, min(len(order), foreignMaxItems))
	for _, id := range order {
		out = append(out, byID[id])
		if len(out) == foreignMaxItems {
			break
		}
	}
	return out
}

func scanCursorCLI(cwd string, now time.Time) []ForeignSummary {
	root := cursorCLIChatsRoot()
	if !safeForeignDir(root) {
		return nil
	}
	workspaceDir := filepath.Join(root, cursorCLIWorkspaceHash(cwd))
	if !safeForeignDir(workspaceDir) {
		return nil
	}
	entries, err := os.ReadDir(workspaceDir)
	if err != nil {
		return nil
	}
	summaries := make([]ForeignSummary, 0, min(len(entries), foreignMaxItems))
	scanned := 0
	for _, entry := range entries {
		if scanned >= cursorCLIMaxSessions {
			break
		}
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		id := entry.Name()
		if !foreignUUID.MatchString(id) {
			continue
		}
		scanned++
		dbPath := filepath.Join(workspaceDir, id, "store.db")
		if !safeForeignFile(dbPath) {
			continue
		}
		meta, ok := readCursorCLIMeta(dbPath)
		if !ok {
			continue
		}
		agentID := strings.TrimSpace(meta.AgentID)
		if agentID == "" {
			agentID = id
		}
		if !foreignUUID.MatchString(agentID) {
			continue
		}
		updatedAt := time.UnixMilli(meta.CreatedAt)
		if info, err := os.Lstat(dbPath); err == nil {
			if info.ModTime().After(updatedAt) {
				updatedAt = info.ModTime()
			}
		}
		if !foreignRecent(updatedAt, now) {
			continue
		}
		title := firstNonEmpty(meta.Name, "Cursor CLI session")
		summaries = append(summaries, ForeignSummary{
			ID: agentID, Source: "cursor", Title: title, CWD: cwd,
			UpdatedAt: updatedAt.UTC(),
		})
		if len(summaries) == foreignMaxItems {
			break
		}
	}
	return summaries
}

func cursorCLIChatsRoot() string {
	if root := strings.TrimSpace(os.Getenv("CURSOR_HOME")); root != "" {
		return filepath.Join(root, "chats")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cursor", "chats")
}

func cursorCLIWorkspaceHash(cwd string) string {
	sum := md5.Sum([]byte(cwd))
	return hex.EncodeToString(sum[:])
}

func readCursorCLIMeta(dbPath string) (cursorCLIMeta, bool) {
	databaseURL := (&url.URL{Scheme: "file", Path: dbPath}).String() + "?mode=ro"
	database, err := sql.Open("sqlite", databaseURL)
	if err != nil {
		return cursorCLIMeta{}, false
	}
	defer database.Close()
	database.SetMaxOpenConns(1)

	var value string
	err = database.QueryRow(`SELECT value FROM meta WHERE key = '0' LIMIT 1`).Scan(&value)
	if err != nil {
		err = database.QueryRow(`SELECT value FROM meta LIMIT 1`).Scan(&value)
	}
	if err != nil || value == "" {
		return cursorCLIMeta{}, false
	}
	raw, ok := decodeCursorCLIMetaValue(value)
	if !ok {
		return cursorCLIMeta{}, false
	}
	var meta cursorCLIMeta
	if json.Unmarshal(raw, &meta) != nil {
		return cursorCLIMeta{}, false
	}
	return meta, true
}

func decodeCursorCLIMetaValue(value string) ([]byte, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, false
	}
	if len(value)%2 == 0 && isHexString(value) {
		decoded, err := hex.DecodeString(value)
		if err == nil && len(decoded) > 0 {
			return decoded, true
		}
	}
	return []byte(value), true
}

func isHexString(value string) bool {
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}
