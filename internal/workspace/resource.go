package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func (w *Workspace) CreateFile(path string, overwrite, ignoreIfExists bool) (bool, error) {
	resolved, err := w.Resolve(path)
	if err != nil {
		return false, err
	}
	info, err := os.Lstat(resolved)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return false, errors.New("workspace create target must be a regular file")
		}
		if !overwrite {
			if ignoreIfExists {
				return false, nil
			}
			return false, errors.New("workspace create target already exists")
		}
		temporary, err := os.CreateTemp(filepath.Dir(resolved), ".gork-lsp-create-*")
		if err != nil {
			return false, err
		}
		tempPath := temporary.Name()
		defer os.Remove(tempPath)
		if err = temporary.Chmod(info.Mode().Perm()); err == nil {
			err = temporary.Sync()
		}
		if closeErr := temporary.Close(); err == nil {
			err = closeErr
		}
		if err == nil {
			err = atomicReplace(tempPath, resolved)
		}
		if err != nil {
			return false, fmt.Errorf("overwrite workspace file: %w", err)
		}
		return true, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	file, err := os.OpenFile(resolved, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return false, fmt.Errorf("create workspace file: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(resolved)
		return false, err
	}
	return true, nil
}

func (w *Workspace) RenameFile(oldPath, newPath string, overwrite, ignoreIfExists bool) (bool, error) {
	source, err := w.Resolve(oldPath)
	if err != nil {
		return false, err
	}
	target, err := w.Resolve(newPath)
	if err != nil {
		return false, err
	}
	if source == target {
		return false, nil
	}
	info, err := os.Lstat(source)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, errors.New("workspace rename source must be a regular file")
	}
	targetInfo, targetErr := os.Lstat(target)
	if targetErr == nil {
		if targetInfo.Mode()&os.ModeSymlink != 0 || !targetInfo.Mode().IsRegular() {
			return false, errors.New("workspace rename target must be a regular file")
		}
		if !overwrite {
			if ignoreIfExists {
				return false, nil
			}
			return false, errors.New("workspace rename target already exists")
		}
		if err := atomicReplace(source, target); err != nil {
			return false, fmt.Errorf("rename workspace file: %w", err)
		}
		return true, nil
	}
	if !errors.Is(targetErr, os.ErrNotExist) {
		return false, targetErr
	}
	if err := os.Link(source, target); err != nil {
		return false, fmt.Errorf("rename workspace file: %w", err)
	}
	if err := os.Remove(source); err != nil {
		_ = os.Remove(target)
		return false, fmt.Errorf("remove workspace rename source: %w", err)
	}
	return true, nil
}

func (w *Workspace) DeleteFile(path string, ignoreIfNotExists bool) (bool, error) {
	resolved, err := w.Resolve(path)
	if err != nil {
		if ignoreIfNotExists && errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	info, err := os.Lstat(resolved)
	if errors.Is(err, os.ErrNotExist) && ignoreIfNotExists {
		return false, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, errors.New("workspace delete target must be a regular file")
	}
	if err := os.Remove(resolved); err != nil {
		return false, fmt.Errorf("delete workspace file: %w", err)
	}
	return true, nil
}
