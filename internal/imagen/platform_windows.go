package imagen

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func tryLock(f *os.File, nonblock bool) error {
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK)
	if nonblock {
		flags |= windows.LOCKFILE_FAIL_IMMEDIATELY
	}
	// 锁定第一个字节；即使锁文件为空，Windows 仍支持字节范围锁。
	err := windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, &windows.Overlapped{})
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return errLockBusy
	}
	return err
}

func unlock(f *os.File) {
	_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &windows.Overlapped{})
	_ = f.Close()
}

// Windows 不支持 exec.Cmd.ExtraFiles。启动确认使用已受锁保护的任务状态。
func workerReady() *os.File { return nil }

func quietChild(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}

func (s Store) startDetachedWorker(cmd *exec.Cmd, id string) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
	}
	if err := cmd.Start(); err != nil {
		_, _ = s.update(id, func(j *Job) { j.Status = "failed"; j.Error = "could not start worker" })
		return err
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		j, err := s.load(id)
		if err == nil && j.Status != "starting" {
			return nil
		}
		if time.Now().After(deadline) {
			break
		}
		select {
		case <-done:
			// 子进程可能刚写入最终状态，再读一次避免误报快速完成的任务。
			if j, err = s.load(id); err == nil && j.Status != "starting" {
				return nil
			}
			return fmt.Errorf("job %s created but worker exited before startup was confirmed; inspect status (do not resubmit blindly)", id)
		case <-time.After(20 * time.Millisecond):
		}
	}
	return fmt.Errorf("job %s created but worker startup was not confirmed; inspect status (do not resubmit blindly)", id)
}

func checkPrivateFile(path string, _ os.FileInfo) error {
	// Windows 的 mode 0600 不控制访问权限；必须检查实际 DACL，不能跳过凭据保护。
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read credential file ACL: %w", err)
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return fmt.Errorf("credential source requires a private Windows DACL: %s", path)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return err
		}
		// INHERIT_ONLY 不授予本文件权限；拒绝 ACE 不扩大读取权限。
		if ace.Header.AceFlags&0x08 != 0 || ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return fmt.Errorf("credential source has an unsupported Windows ACE: %s", path)
		}
		if ace.Mask&(windows.FILE_READ_DATA|windows.FILE_WRITE_DATA|windows.FILE_APPEND_DATA|windows.DELETE|windows.WRITE_DAC|windows.WRITE_OWNER|windows.GENERIC_READ|windows.GENERIC_WRITE|windows.GENERIC_ALL) == 0 {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.Equals(user.User.Sid) && !sid.IsWellKnown(windows.WinLocalSystemSid) && !sid.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
			return fmt.Errorf("credential source must restrict reading to the current user, SYSTEM and Administrators: %s", path)
		}
	}
	return nil
}

func replaceFile(from, to string) error {
	// Go 的普通文件读取未共享删除权限。状态轮询短暂打开目标时，替换会被拒绝。
	// 保留原文件并重试原子替换；不能先删除目标，否则会丢失旧状态。
	source, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	target, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		err = windows.MoveFileEx(source, target, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
		if err == nil || (!errors.Is(err, windows.ERROR_SHARING_VIOLATION) && !errors.Is(err, windows.ERROR_ACCESS_DENIED)) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func readStateFile(path string) ([]byte, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		// 共享删除权限允许状态文件原子替换；打开的读者继续读取同一完整快照。
		handle, err := windows.CreateFile(name, windows.GENERIC_READ,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
			nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
		if err == nil {
			file := os.NewFile(uintptr(handle), path)
			defer file.Close()
			return io.ReadAll(file)
		}
		if (!errors.Is(err, windows.ERROR_SHARING_VIOLATION) && !errors.Is(err, windows.ERROR_ACCESS_DENIED)) || time.Now().After(deadline) {
			return nil, &os.PathError{Op: "open", Path: path, Err: err}
		}
		time.Sleep(10 * time.Millisecond)
	}
}
