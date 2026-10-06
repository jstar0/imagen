//go:build !windows

package imagen

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func tryLock(f *os.File, nonblock bool) error {
	flags := syscall.LOCK_EX
	if nonblock {
		flags |= syscall.LOCK_NB
	}
	err := syscall.Flock(int(f.Fd()), flags)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return errLockBusy
	}
	return err
}

func unlock(f *os.File) {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	_ = f.Close()
}

func workerReady() *os.File { return os.NewFile(3, "ready") }

func quietChild(_ *exec.Cmd) {}

func (s Store) startDetachedWorker(cmd *exec.Cmd, id string) error {
	reader, writer, err := os.Pipe()
	if err != nil {
		return err
	}
	defer reader.Close()
	defer writer.Close()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.ExtraFiles = []*os.File{writer}
	if err = cmd.Start(); err != nil {
		_, _ = s.update(id, func(j *Job) { j.Status = "failed"; j.Error = "could not start worker" })
		return err
	}
	writer.Close()
	go func() { _ = cmd.Wait() }()
	_ = reader.SetReadDeadline(time.Now().Add(5 * time.Second))
	b := make([]byte, 1)
	if _, err = io.ReadFull(reader, b); err != nil || b[0] != 'R' {
		return fmt.Errorf("job %s created but worker startup was not confirmed; inspect status (do not resubmit blindly)", id)
	}
	return nil
}

func checkPrivateFile(path string, info os.FileInfo) error {
	if info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("credential source must be an owner-only regular file: %s", path)
	}
	return nil
}

func replaceFile(from, to string) error { return os.Rename(from, to) }

func readStateFile(path string) ([]byte, error) { return os.ReadFile(path) }
