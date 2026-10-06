package imagen

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func testBinaryName() string {
	if runtime.GOOS == "windows" {
		return "imagen.exe"
	}
	return "imagen"
}

func killTestProcess(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	defer p.Release()
	return p.Kill()
}

func sameTestPath(a, b string) bool {
	if resolved, err := filepath.EvalSymlinks(a); err == nil {
		a = resolved
	}
	if resolved, err := filepath.EvalSymlinks(b); err == nil {
		b = resolved
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
