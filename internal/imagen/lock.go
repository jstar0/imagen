package imagen

import (
	"errors"
	"os"
)

var errLockBusy = errors.New("file lock is busy")

func lockFile(path string, nonblock bool) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = tryLock(f, nonblock); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
