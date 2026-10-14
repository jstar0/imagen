//go:build !windows

package imagen

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// lockHandle takes an exclusive flock(2) on f.
func lockHandle(f *os.File, nonblock bool) error {
	flags := syscall.LOCK_EX
	if nonblock {
		flags |= syscall.LOCK_NB
	}
	return syscall.Flock(int(f.Fd()), flags)
}

func unlockHandle(f *os.File) { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }

// isLocked reports a non-blocking lock attempt that found the lock held.
func isLocked(err error) bool { return errors.Is(err, syscall.EWOULDBLOCK) }

// startWorker starts the detached worker in its own session. The readiness pipe
// is inherited as file descriptor 3.
func startWorker(executable string, args []string, log, ready *os.File) error {
	cmd := exec.Command(executable, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.ExtraFiles = []*os.File{ready}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// workerReadyPipe returns the readiness pipe passed by startWorker.
func workerReadyPipe() *os.File { return os.NewFile(3, "ready") }

// replaceFile atomically replaces dst with src.
func replaceFile(src, dst string) error { return os.Rename(src, dst) }

// checkPrivate requires an owner-only regular file (no group/other permissions).
func checkPrivate(path string, info os.FileInfo) error {
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("credential source must be an owner-only regular file: %s", path)
	}
	return nil
}

// isExecutableFile reports whether info describes an executable regular file.
func isExecutableFile(_ string, info os.FileInfo) bool {
	return info.Mode().IsRegular() && info.Mode()&0111 != 0
}

// nativeCodexExecutable resolves the binary to launch for a codex found on PATH.
func nativeCodexExecutable(path string) string { return path }
