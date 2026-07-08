package state

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
)

func syncParentDir(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}

	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()

	if err := dir.Sync(); err != nil {
		if errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EINVAL) {
			return nil
		}
		return err
	}
	return nil
}
