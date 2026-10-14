//go:build windows

package imagen

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// readyHandleEnv carries the inherited readiness pipe handle to the worker;
// Windows cannot pass extra file descriptors by position.
const readyHandleEnv = "IMAGEN_READY_HANDLE"

// lockHandle takes an exclusive LockFileEx lock on the first byte of f. The lock
// is released when the handle is closed, matching flock(2) lifetime.
func lockHandle(f *os.File, nonblock bool) error {
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK)
	if nonblock {
		flags |= windows.LOCKFILE_FAIL_IMMEDIATELY
	}
	return windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, new(windows.Overlapped))
}

func unlockHandle(f *os.File) {
	_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, new(windows.Overlapped))
}

// isLocked reports a non-blocking lock attempt that found the lock held.
func isLocked(err error) bool { return errors.Is(err, windows.ERROR_LOCK_VIOLATION) }

// startWorker starts the worker without a console, in its own process group and,
// when the caller's job object permits it, outside that job, so that the worker
// outlives a terminal or agent sandbox that tears down the CLI's process tree.
// The readiness pipe is inherited explicitly and named through readyHandleEnv.
func startWorker(executable string, args []string, log, ready *os.File) error {
	handle := syscall.Handle(ready.Fd())
	if err := syscall.SetHandleInformation(handle, syscall.HANDLE_FLAG_INHERIT, syscall.HANDLE_FLAG_INHERIT); err != nil {
		return err
	}
	start := func(flags uint32) error {
		cmd := exec.Command(executable, args...)
		cmd.SysProcAttr = &syscall.SysProcAttr{
			CreationFlags:              flags,
			HideWindow:                 true,
			AdditionalInheritedHandles: []syscall.Handle{handle},
		}
		cmd.Env = append(os.Environ(), readyHandleEnv+"="+strconv.FormatUint(uint64(handle), 10))
		cmd.Stdout = log
		cmd.Stderr = log
		if err := cmd.Start(); err != nil {
			return err
		}
		go func() { _ = cmd.Wait() }()
		return nil
	}
	base := uint32(windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS)
	err := start(base | windows.CREATE_BREAKAWAY_FROM_JOB)
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		// The enclosing job forbids breakaway; start inside it instead.
		err = start(base)
	}
	return err
}

// workerReadyPipe returns the readiness pipe passed by startWorker, if any.
func workerReadyPipe() *os.File {
	value := os.Getenv(readyHandleEnv)
	_ = os.Unsetenv(readyHandleEnv)
	handle, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return nil
	}
	return os.NewFile(uintptr(handle), "ready")
}

// replaceFile atomically replaces dst with src. A concurrent reader holding dst
// open makes MoveFileEx fail transiently on Windows, so retry briefly.
func replaceFile(src, dst string) error {
	var err error
	for attempt := 0; attempt < 100; attempt++ {
		if err = os.Rename(src, dst); err == nil {
			return nil
		}
		if !errors.Is(err, windows.ERROR_ACCESS_DENIED) && !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
	return err
}

// broadSIDs are principals whose access makes a credential file shared.
var broadSIDs = []windows.WELL_KNOWN_SID_TYPE{
	windows.WinWorldSid,
	windows.WinAuthenticatedUserSid,
	windows.WinBuiltinUsersSid,
	windows.WinBuiltinGuestsSid,
	windows.WinAnonymousSid,
	windows.WinInteractiveSid,
	windows.WinNetworkSid,
}

// checkPrivate is the Windows counterpart of the POSIX 0600 requirement: POSIX
// permission bits are not meaningful here, so the file's DACL must not grant
// access to Everyone, Users, Authenticated Users or similar broad principals.
// Files created under the user profile inherit an owner/SYSTEM/Administrators DACL.
func checkPrivate(path string, info os.FileInfo) error {
	reject := fmt.Errorf("credential source must be an owner-only regular file: %s", path)
	if !info.Mode().IsRegular() {
		return reject
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("credential source permissions unreadable: %w", err)
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return reject // a missing or NULL DACL grants everyone full access
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return reject
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		for _, broad := range broadSIDs {
			if sid.IsWellKnown(broad) {
				return reject
			}
		}
	}
	return nil
}

// isExecutableFile reports whether path names a regular file with an
// executable extension; Windows has no POSIX execute permission bit.
func isExecutableFile(path string, info os.FileInfo) bool {
	if !info.Mode().IsRegular() {
		return false
	}
	ext := strings.ToLower(filepath.Ext(path))
	pathext := os.Getenv("PATHEXT")
	if pathext == "" {
		pathext = ".COM;.EXE;.BAT;.CMD"
	}
	for _, candidate := range strings.Split(strings.ToLower(pathext), ";") {
		if candidate != "" && candidate == ext {
			return true
		}
	}
	return false
}

// nativeCodexExecutable resolves an npm/pnpm `codex` shim (codex.cmd/.ps1) to the
// native codex.exe bundled in the package. Running the shim would place cmd.exe
// and node between imagen and the client, and cancelling it would orphan them.
func nativeCodexExecutable(path string) string {
	if strings.EqualFold(filepath.Ext(path), ".exe") {
		return path
	}
	dir := filepath.Dir(path)
	patterns := []string{
		// npm global: <prefix>/node_modules/@openai/codex/node_modules/@openai/codex-win32-<arch>/vendor/<triple>/codex/codex.exe
		filepath.Join(dir, "node_modules", "@openai", "codex", "node_modules", "@openai", "codex-win32-*", "vendor", "*", "codex", "codex.exe"),
		filepath.Join(dir, "node_modules", "@openai", "codex", "vendor", "*", "codex", "codex.exe"),
		// pnpm global: <pnpm home>/global/<n>/node_modules/...
		filepath.Join(dir, "global", "*", "node_modules", "@openai", "codex", "node_modules", "@openai", "codex-win32-*", "vendor", "*", "codex", "codex.exe"),
		filepath.Join(dir, "global", "*", "node_modules", "@openai", "codex-win32-*", "vendor", "*", "codex", "codex.exe"),
	}
	for _, pattern := range patterns {
		if matches, _ := filepath.Glob(pattern); len(matches) > 0 {
			return matches[0]
		}
	}
	return path
}
