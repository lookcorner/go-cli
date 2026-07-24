package trace

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/lookcorner/go-cli/internal/session"
	"github.com/lookcorner/go-cli/internal/version"
)

const (
	maxArchiveBytes   = 128 << 20
	maxArchiveEntries = 2048
)

type Service struct {
	SessionDir string
	ExportRoot string
	Now        func() time.Time
}

type Result struct {
	SessionID string
	Path      string
	Size      int64
}

func (s Service) Export(sessionID, output string) (Result, error) {
	sessionPath, err := session.PathForID(s.SessionDir, sessionID)
	if err != nil {
		return Result{}, err
	}
	if err := regularFile(sessionPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Result{}, fmt.Errorf("session %q not found", sessionID)
		}
		return Result{}, fmt.Errorf("read session %q: %w", sessionID, err)
	}
	if output == "" {
		root, err := s.exportRoot()
		if err != nil {
			return Result{}, err
		}
		output = filepath.Join(root, sessionID+".tar.gz")
	}
	output, err = filepath.Abs(output)
	if err != nil {
		return Result{}, fmt.Errorf("resolve trace output: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o700); err != nil {
		return Result{}, fmt.Errorf("create trace export directory: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(output), ".gork-trace-*.tar.gz")
	if err != nil {
		return Result{}, fmt.Errorf("create trace export: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)

	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	gzipWriter := gzip.NewWriter(temp)
	writer := &archiveWriter{tar: tar.NewWriter(gzipWriter), gzip: gzipWriter, now: now}
	err = writer.addFile(filepath.ToSlash(filepath.Join(sessionID, filepath.Base(sessionPath))), sessionPath)
	if err == nil {
		err = writer.addRelated(sessionID, sessionPath)
	}
	if err == nil {
		err = writer.addJSON(filepath.ToSlash(filepath.Join(sessionID, "trace_config.json")), map[string]any{
			"trace_upload_enabled":   false,
			"telemetry_trace_upload": false,
			"privacy_hard_off":       true,
		})
	}
	if err == nil {
		err = writer.addJSON(filepath.ToSlash(filepath.Join(sessionID, "export_metadata.json")), map[string]string{
			"session_id": sessionID, "grok_version": version.Current, "os": runtime.GOOS,
			"arch": runtime.GOARCH, "exported_at": now.Format(time.RFC3339),
		})
	}
	closeErr := writer.close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = temp.Sync()
	}
	if closeFileErr := temp.Close(); err == nil {
		err = closeFileErr
	}
	if err != nil {
		return Result{}, err
	}
	if err := os.Chmod(tempPath, 0o600); err != nil {
		return Result{}, fmt.Errorf("secure trace export: %w", err)
	}
	if err := replaceFile(tempPath, output); err != nil {
		return Result{}, fmt.Errorf("write trace export: %w", err)
	}
	info, err := os.Stat(output)
	if err != nil {
		return Result{}, err
	}
	return Result{SessionID: sessionID, Path: output, Size: info.Size()}, nil
}

func (s Service) exportRoot() (string, error) {
	if s.ExportRoot != "" {
		return s.ExportRoot, nil
	}
	if home := strings.TrimSpace(os.Getenv("GROK_HOME")); home != "" {
		return filepath.Join(home, "trace-exports"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve trace export directory: %w", err)
	}
	return filepath.Join(home, ".grok", "trace-exports"), nil
}

type archiveWriter struct {
	tar     *tar.Writer
	now     time.Time
	bytes   int64
	entries int
	gzip    *gzip.Writer
}

func (w *archiveWriter) close() error {
	if err := w.tar.Close(); err != nil {
		return fmt.Errorf("finalize trace archive: %w", err)
	}
	if err := w.gzip.Close(); err != nil {
		return fmt.Errorf("compress trace archive: %w", err)
	}
	return nil
}

func (w *archiveWriter) addRelated(sessionID, sessionPath string) error {
	base := filepath.Dir(sessionPath)
	artifactDir, err := session.ArtifactDir(sessionPath)
	if err != nil {
		return err
	}
	if err := w.addTree(filepath.ToSlash(filepath.Join(sessionID, "artifacts")), artifactDir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	rewind := filepath.Join(base, "rewind", sessionID+".jsonl")
	if _, err := os.Lstat(rewind); err == nil {
		if err := safeDir(filepath.Dir(rewind)); err != nil {
			return err
		}
		if err := w.addFile(filepath.ToSlash(filepath.Join(sessionID, "rewind.jsonl")), rewind); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	assets, err := referencedAssets(sessionPath)
	if err != nil {
		return err
	}
	if len(assets) > 0 {
		if err := safeDir(filepath.Join(base, "assets")); err != nil {
			return err
		}
	}
	for _, name := range assets {
		source := filepath.Join(base, "assets", name)
		if err := w.addFile(filepath.ToSlash(filepath.Join(sessionID, "assets", name)), source); err != nil {
			return fmt.Errorf("add session asset %q: %w", name, err)
		}
	}
	return nil
}

func (w *archiveWriter) addTree(prefix, root string) error {
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("trace source directory must be a non-symlink directory")
	}
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("trace source contains symlink: %s", path)
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		return w.addFile(filepath.ToSlash(filepath.Join(prefix, relative)), path)
	})
}

func (w *archiveWriter) addFile(name, path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("trace source must be a regular non-symlink file")
	}
	if err := w.reserve(info.Size()); err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	header := &tar.Header{Name: name, Mode: 0o600, Size: info.Size(), ModTime: w.now}
	if err := w.tar.WriteHeader(header); err != nil {
		return err
	}
	if _, err := io.Copy(w.tar, file); err != nil {
		return err
	}
	return nil
}

func (w *archiveWriter) addJSON(name string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := w.reserve(int64(len(data))); err != nil {
		return err
	}
	header := &tar.Header{Name: name, Mode: 0o600, Size: int64(len(data)), ModTime: w.now}
	if err := w.tar.WriteHeader(header); err != nil {
		return err
	}
	_, err = w.tar.Write(data)
	return err
}

func (w *archiveWriter) reserve(size int64) error {
	if size < 0 || w.bytes+size > maxArchiveBytes {
		return errors.New("trace archive exceeds 128 MB")
	}
	if w.entries >= maxArchiveEntries {
		return errors.New("trace archive contains too many files")
	}
	w.bytes += size
	w.entries++
	return nil
}

func regularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("session log must be a regular non-symlink file")
	}
	return nil
}

func safeDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("trace source directory must be a non-symlink directory")
	}
	return nil
}

func referencedAssets(sessionPath string) ([]string, error) {
	file, err := os.Open(sessionPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	found := map[string]bool{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	for scanner.Scan() {
		var value any
		if json.Unmarshal(scanner.Bytes(), &value) == nil {
			findAssets(value, found)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	result := make([]string, 0, len(found))
	for name := range found {
		result = append(result, name)
	}
	sort.Strings(result)
	return result, nil
}

func findAssets(value any, found map[string]bool) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if key == "uri" {
				if uri, ok := child.(string); ok {
					clean := filepath.Clean(filepath.FromSlash(uri))
					if !filepath.IsAbs(clean) && filepath.Dir(clean) == "assets" {
						name := filepath.Base(clean)
						if name != "." && name != ".." {
							found[name] = true
						}
					}
				}
			}
			findAssets(child, found)
		}
	case []any:
		for _, child := range typed {
			findAssets(child, found)
		}
	}
}
