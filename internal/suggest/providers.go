package suggest

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var executableCache struct {
	sync.Mutex
	path    string
	expires time.Time
	names   []string
}

const (
	maxFiles = 50
	scanCap  = 1000
)

var fileCommands = map[string]bool{
	"awk": true, "bat": true, "cat": true, "cd": true, "chmod": true, "chown": true,
	"code": true, "cp": true, "diff": true, "file": true, "find": true, "grep": true,
	"head": true, "less": true, "ln": true, "ls": true, "mkdir": true, "mv": true,
	"nano": true, "nvim": true, "rm": true, "sed": true, "sort": true, "source": true,
	"stat": true, "tail": true, "touch": true, "vi": true, "vim": true, "wc": true,
}

func pathSuggestions(prefix, text string) []Completion {
	tok := parseToken(prefix)
	if tok.tokensBefore != 0 || tok.afterRedirect || tok.value == "" || strings.HasPrefix(tok.value, "-") {
		return nil
	}
	names := executables()
	start := sort.SearchStrings(names, tok.value)
	var matches []string
	for _, name := range names[start:] {
		if !strings.HasPrefix(name, tok.value) {
			break
		}
		matches = append(matches, name)
	}
	truncated := len(matches) > maxPath
	matches = matches[:min(len(matches), maxPath)]
	rows := make([]Completion, 0, len(matches))
	for _, name := range matches {
		token := buildToken(tok, "", name, false)
		rows = append(rows, tokenCompletion(name, "", token, "path", 0, truncated, text, tok.start, len(prefix)))
	}
	return rows
}

func executables() []string {
	pathValue := os.Getenv("PATH")
	executableCache.Lock()
	defer executableCache.Unlock()
	if executableCache.path == pathValue && time.Now().Before(executableCache.expires) {
		return executableCache.names
	}
	seen := map[string]bool{}
	var names []string
	for _, dir := range filepath.SplitList(pathValue) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if seen[entry.Name()] {
				continue
			}
			info, err := os.Stat(filepath.Join(dir, entry.Name()))
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
				continue
			}
			seen[entry.Name()] = true
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	executableCache.path, executableCache.expires, executableCache.names = pathValue, time.Now().Add(time.Minute), names
	return names
}

type fileEntry struct {
	name        string
	directory   bool
	tier, score int
}

func fileSuggestions(prefix, text, cwd string) []Completion {
	tok := parseToken(prefix)
	if strings.HasPrefix(tok.value, "-") || tok.tokensBefore == 0 && !tok.afterRedirect && !strings.Contains(tok.value, "/") && tok.value != "~" {
		return nil
	}
	splitAt := tok.dirValueLen
	if splitAt < 0 {
		splitAt = 0
	}
	dirValue, matchPrefix := tok.value[:splitAt], tok.value[splitAt:]
	rawDir := text[tok.start:tok.dirRawEnd]
	if tok.value == "~" && len(tok.plain) > 0 && tok.plain[0] {
		dirValue, matchPrefix, rawDir = "~/", "", "~/"
	}
	plain := tok.plain
	if splitAt < len(plain) {
		plain = plain[:splitAt]
	}
	listDir := expandDir(dirValue, plain)
	if !filepath.IsAbs(listDir) {
		listDir = filepath.Join(cwd, listDir)
	}
	directory, err := os.Open(listDir)
	if err != nil {
		return nil
	}
	defer directory.Close()
	entries, err := directory.ReadDir(scanCap + 1)
	if err != nil && len(entries) == 0 {
		return nil
	}
	showHidden := strings.HasPrefix(matchPrefix, ".")
	rows := make([]fileEntry, 0, min(len(entries), scanCap))
	truncated := len(entries) > scanCap
	symlinkStats := 0
	for _, entry := range entries[:min(len(entries), scanCap)] {
		name := entry.Name()
		if strings.HasPrefix(name, ".") && !showHidden {
			continue
		}
		tier, score, ok := matchFile(name, matchPrefix)
		if !ok {
			continue
		}
		isDir := entry.IsDir()
		if entry.Type()&os.ModeSymlink != 0 && symlinkStats < 64 {
			symlinkStats++
			if info, statErr := os.Stat(filepath.Join(listDir, name)); statErr == nil {
				isDir = info.IsDir()
			}
		}
		rows = append(rows, fileEntry{name: name, directory: isDir, tier: tier, score: score})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].tier != rows[j].tier {
			return rows[i].tier < rows[j].tier
		}
		if rows[i].score != rows[j].score {
			return rows[i].score > rows[j].score
		}
		if rows[i].directory != rows[j].directory {
			return rows[i].directory
		}
		return rows[i].name < rows[j].name
	})
	truncated = truncated || len(rows) > maxFiles
	rows = rows[:min(len(rows), maxFiles)]
	priority := 0
	if fileCommands[filepath.Base(tok.command)] {
		priority = 2
	}
	result := make([]Completion, 0, len(rows))
	for _, entry := range rows {
		display, description := entry.name, ""
		if entry.directory {
			display, description = display+"/", "directory"
		}
		token := buildToken(tok, rawDir, entry.name, entry.directory)
		result = append(result, tokenCompletion(display, description, token, "file", priority, truncated, text, tok.start, len(prefix)))
	}
	return result
}

func expandDir(value string, plain []bool) string {
	if strings.HasPrefix(value, "~/") && len(plain) > 0 && plain[0] {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, expandVariables(strings.TrimPrefix(value, "~/"), plain[min(2, len(plain)):]))
		}
	}
	return expandVariables(value, plain)
}

func expandVariables(value string, plain []bool) string {
	var out strings.Builder
	for index := 0; index < len(value); {
		if value[index] != '$' || index >= len(plain) || !plain[index] {
			out.WriteByte(value[index])
			index++
			continue
		}
		end, name := index+1, ""
		if end < len(value) && value[end] == '{' {
			if close := strings.IndexByte(value[end+1:], '}'); close >= 0 {
				close += end + 1
				name, end = value[end+1:close], close+1
			}
		} else {
			for end < len(value) && (value[end] == '_' || value[end] >= 'a' && value[end] <= 'z' || value[end] >= 'A' && value[end] <= 'Z' || end > index+1 && value[end] >= '0' && value[end] <= '9') {
				end++
			}
			name = value[index+1 : end]
		}
		if name == "" {
			out.WriteByte(value[index])
			index++
			continue
		}
		if found, ok := os.LookupEnv(name); ok {
			out.WriteString(found)
		} else {
			out.WriteString(value[index:end])
		}
		index = end
	}
	return out.String()
}

func matchFile(name, prefix string) (tier, score int, ok bool) {
	if prefix == "" || strings.HasPrefix(name, prefix) {
		return 0, len(prefix), true
	}
	if strings.HasPrefix(strings.ToLower(name), strings.ToLower(prefix)) {
		return 1, len(prefix), true
	}
	needle, candidate := []rune(strings.ToLower(prefix)), []rune(strings.ToLower(name))
	index, first := 0, -1
	for position, r := range candidate {
		if index < len(needle) && r == needle[index] {
			if first < 0 {
				first = position
			}
			index++
		}
	}
	if index != len(needle) {
		return 0, 0, false
	}
	return 2, len(needle)*10 - first, true
}

func tokenCompletion(display, description, token, source string, priority int, truncated bool, text string, start, end int) Completion {
	return Completion{
		Display: display, Description: description,
		InsertText: text[:start] + token + text[end:], Source: source, Priority: priority,
		ReplaceRange: []int{start, end}, TokenText: token, Truncated: truncated,
	}
}
