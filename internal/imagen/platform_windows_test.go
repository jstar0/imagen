//go:build windows

package imagen

import (
	"os"
	"os/exec"
	"testing"
)

const exeSuffix = ".exe"

func killProcess(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

// makeShared grants Everyone read access, the Windows analogue of mode 0644.
func makeShared(t *testing.T, path string) {
	t.Helper()
	if out, err := exec.Command("icacls", path, "/grant", "*S-1-1-0:R").CombinedOutput(); err != nil {
		t.Fatalf("icacls: %v %s", err, out)
	}
}
