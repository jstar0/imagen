//go:build windows

package imagen

import (
	"os"
	"os/exec"
	"path/filepath"
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

func TestNativeCodexExecutableResolvesShims(t *testing.T) {
	for name, layout := range map[string]string{
		"npm":  `node_modules\@openai\codex\node_modules\@openai\codex-win32-x64\vendor\x86_64-pc-windows-msvc\codex\codex.exe`,
		"pnpm": `global\5\.pnpm\@openai+codex@0.114.0-win32-x64\node_modules\@openai\codex\vendor\x86_64-pc-windows-msvc\codex\codex.exe`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			native := filepath.Join(dir, layout)
			if err := os.MkdirAll(filepath.Dir(native), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(native, nil, 0755); err != nil {
				t.Fatal(err)
			}
			shim := filepath.Join(dir, "codex.cmd")
			if got := nativeCodexExecutable(shim); got != native {
				t.Fatalf("got %s, want %s", got, native)
			}
		})
	}
	exe := `C:\tools\codex.exe`
	if got := nativeCodexExecutable(exe); got != exe {
		t.Fatalf("native executable rewritten: %s", got)
	}
	if got := nativeCodexExecutable(filepath.Join(t.TempDir(), "codex.cmd")); filepath.Ext(got) != ".cmd" {
		t.Fatalf("unresolvable shim should be kept: %s", got)
	}
}

func TestCheckPrivateRejectsBroadAccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte("k"), 0600); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if err := checkPrivate(path, info); err != nil {
		t.Fatalf("profile-private file rejected: %v", err)
	}
	makeShared(t, path)
	if err := checkPrivate(path, info); err == nil {
		t.Fatal("Everyone-readable file accepted")
	}
}
