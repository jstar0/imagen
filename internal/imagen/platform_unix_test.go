//go:build !windows

package imagen

import (
	"os"
	"syscall"
	"testing"
)

const exeSuffix = ""

func killProcess(pid int) error { return syscall.Kill(pid, syscall.SIGKILL) }

// makeShared makes a credential file readable by other users.
func makeShared(t *testing.T, path string) {
	t.Helper()
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
}
