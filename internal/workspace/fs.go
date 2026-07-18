package workspace

import (
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bmatcuk/doublestar"
)

const (
	maxFSListEntries   = 50_000
	defaultFSReadBytes = 1 << 20
	maxFSReadBytes     = 4 << 20
)

type FSListOptions struct {
	Depth            int
	IncludeHidden    bool
	Limit            int
	Offset           int
	FollowSymlinks   bool
	RespectGitIgnore bool
	IncludeGlobs     []string
	ExcludeGlobs     []string
}

type FSNode struct {
	Name       string  `json:"name"`
	Path       string  `json:"path"`
	Type       string  `json:"type"`
	IsSymlink  *bool   `json:"isSymlink,omitempty"`
	Size       *uint64 `json:"size,omitempty"`
	ModifiedAt string  `json:"modifiedAt,omitempty"`
	isDir      bool
}

type FSListResult struct {
	Nodes     []FSNode `json:"nodes"`
	Truncated bool     `json:"truncated"`
}

type FSReadResult struct {
	Content       string  `json:"content"`
	ContentBase64 *string `json:"contentBase64,omitempty"`
	Size          uint64  `json:"size"`
	LineCount     *uint64 `json:"lineCount,omitempty"`
	Type          string  `json:"type"`
}

func (w *Workspace) List(path string, options FSListOptions) (FSListResult, error) {
	root, err := w.Resolve(path)
	if err != nil {
		return FSListResult{}, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return FSListResult{}, err
	}
	if !info.IsDir() {
		return FSListResult{}, errors.New("list path must be a directory")
	}
	if options.Depth <= 0 {
		options.Depth = 1
	}
	if options.Limit <= 0 {
		options.Limit = 1000
	}
	var nodes []FSNode
	hitCap := false
	visited := make(map[string]bool)
	var walk func(string, string, int) error
	walk = func(logical, real string, depth int) error {
		if depth > options.Depth {
			return nil
		}
		entries, err := os.ReadDir(real)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if len(nodes) >= maxFSListEntries {
				hitCap = true
				return nil
			}
			name := entry.Name()
			if !options.IncludeHidden && strings.HasPrefix(name, ".") {
				continue
			}
			logicalPath := filepath.Join(logical, name)
			realPath := filepath.Join(real, name)
			rel, _ := filepath.Rel(root, logicalPath)
			rel = filepath.ToSlash(rel)
			if options.RespectGitIgnore && w.gitIgnored(logicalPath) {
				continue
			}
			lstat, err := os.Lstat(realPath)
			if err != nil {
				continue
			}
			isSymlink := lstat.Mode()&os.ModeSymlink != 0
			stat := lstat
			resolved := realPath
			if isSymlink {
				candidate, resolveErr := filepath.EvalSymlinks(realPath)
				if resolveErr != nil || !pathWithin(w.root, candidate) {
					continue
				}
				if options.FollowSymlinks {
					if target, statErr := os.Stat(candidate); statErr == nil {
						resolved, stat = candidate, target
					}
				}
			}
			isDir := stat.IsDir()
			if matchFSGlobs(rel, options.IncludeGlobs, options.ExcludeGlobs) {
				node := FSNode{Name: name, Path: logicalPath, Type: "file", ModifiedAt: stat.ModTime().UTC().Format(time.RFC3339)}
				if isDir {
					node.Type, node.isDir = "directory", true
				} else {
					size := uint64(stat.Size())
					node.Size = &size
				}
				if isSymlink {
					value := true
					node.IsSymlink = &value
				}
				nodes = append(nodes, node)
			}
			if isDir && depth < options.Depth {
				key := resolved
				if canonical, canonicalErr := filepath.EvalSymlinks(resolved); canonicalErr == nil {
					key = canonical
				}
				if !visited[key] {
					visited[key] = true
					if err := walk(logicalPath, resolved, depth+1); err != nil {
						return err
					}
					if hitCap {
						return nil
					}
				}
			}
		}
		return nil
	}
	visited[root] = true
	if err := walk(root, root, 1); err != nil {
		return FSListResult{}, err
	}
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].isDir != nodes[j].isDir {
			return nodes[i].isDir
		}
		left, right := strings.ToLower(nodes[i].Name), strings.ToLower(nodes[j].Name)
		if left != right {
			return left < right
		}
		return nodes[i].Name < nodes[j].Name
	})
	start := options.Offset
	if start < 0 {
		start = 0
	}
	if start > len(nodes) {
		start = len(nodes)
	}
	end := start + options.Limit
	if end > len(nodes) {
		end = len(nodes)
	}
	return FSListResult{Nodes: nodes[start:end], Truncated: end < len(nodes) || (hitCap && start < len(nodes))}, nil
}

func (w *Workspace) Exists(path string) bool {
	resolved, err := w.ResolveEntry(path)
	if err != nil {
		return false
	}
	_, err = os.Lstat(resolved)
	return err == nil
}

func (w *Workspace) Read(path string, offset, length uint64, maxBytes int, encoding string, ranged bool) (FSReadResult, error) {
	resolved, err := w.Resolve(path)
	if err != nil {
		return FSReadResult{}, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return FSReadResult{}, err
	}
	if !info.Mode().IsRegular() {
		return FSReadResult{}, errors.New("read path must be a regular file")
	}
	if maxBytes <= 0 {
		maxBytes = defaultFSReadBytes
	} else if maxBytes > maxFSReadBytes {
		maxBytes = maxFSReadBytes
	}
	file, err := os.Open(resolved)
	if err != nil {
		return FSReadResult{}, err
	}
	defer file.Close()
	if offset > uint64(info.Size()) {
		offset = uint64(info.Size())
	}
	if _, err := file.Seek(int64(offset), 0); err != nil {
		return FSReadResult{}, err
	}
	want := uint64(info.Size()) - offset
	if ranged {
		want = uint64(maxBytes)
		if length < want {
			want = length
		}
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(want)))
	if err != nil {
		return FSReadResult{}, err
	}
	validUTF8 := utf8.Valid(data)
	result := FSReadResult{Size: uint64(info.Size()), Type: fsContentType(data, validUTF8)}
	if encoding == "base64" || !validUTF8 {
		encoded := base64.StdEncoding.EncodeToString(data)
		result.ContentBase64 = &encoded
	} else {
		result.Content = string(data)
		if !ranged {
			lines := uint64(strings.Count(result.Content, "\n"))
			if result.Content != "" && !strings.HasSuffix(result.Content, "\n") {
				lines++
			}
			result.LineCount = &lines
		}
	}
	return result, nil
}

func fsContentType(data []byte, text bool) string {
	if text {
		return "text/plain"
	}
	contentType := http.DetectContentType(data)
	if index := strings.IndexByte(contentType, ';'); index >= 0 {
		contentType = contentType[:index]
	}
	return contentType
}

func (w *Workspace) Write(path, content string, createDirs bool) error {
	resolved, err := w.resolveWritePath(path, createDirs)
	if err != nil {
		return err
	}
	if createDirs {
		if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
			return err
		}
	}
	mode := os.FileMode(0o644)
	if info, statErr := os.Stat(resolved); statErr == nil {
		if !info.Mode().IsRegular() {
			return errors.New("write path must be a regular file")
		}
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	temp, err := os.CreateTemp(filepath.Dir(resolved), ".gork-fs-write-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.WriteString(content); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(name, resolved)
}

func (w *Workspace) resolveWritePath(path string, createDirs bool) (string, error) {
	resolved, err := w.Resolve(path)
	if err == nil || !createDirs {
		return resolved, err
	}
	candidate := path
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(w.root, candidate)
	}
	candidate = filepath.Clean(candidate)
	ancestor := filepath.Dir(candidate)
	for {
		if _, statErr := os.Lstat(ancestor); statErr == nil {
			break
		} else if !os.IsNotExist(statErr) {
			return "", statErr
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", err
		}
		ancestor = parent
	}
	realAncestor, resolveErr := filepath.EvalSymlinks(ancestor)
	if resolveErr != nil || !pathWithin(w.root, realAncestor) {
		return "", err
	}
	rel, relErr := filepath.Rel(ancestor, candidate)
	if relErr != nil {
		return "", relErr
	}
	return filepath.Join(realAncestor, rel), nil
}

func (w *Workspace) Delete(path string) error {
	resolved, err := w.ResolveEntry(path)
	if err != nil {
		return err
	}
	info, err := os.Lstat(resolved)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return errors.New("delete_file does not delete directories")
	}
	return os.Remove(resolved)
}

func (w *Workspace) gitIgnored(path string) bool {
	rel, err := filepath.Rel(w.root, path)
	if err != nil || rel == "." {
		return false
	}
	command := exec.Command("git", "-C", w.root, "check-ignore", "-q", "--", filepath.ToSlash(rel))
	return command.Run() == nil
}

func matchFSGlobs(path string, includes, excludes []string) bool {
	for _, pattern := range excludes {
		if matched, _ := doublestar.Match(filepath.ToSlash(pattern), path); matched {
			return false
		}
	}
	if len(includes) == 0 {
		return true
	}
	for _, pattern := range includes {
		if matched, _ := doublestar.Match(filepath.ToSlash(pattern), path); matched {
			return true
		}
	}
	return false
}
