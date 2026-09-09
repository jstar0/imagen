package imagen

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

func (s Store) Submit(alias string, p Profile, r Request, concurrency int) (Job, error) {
	return s.SubmitRoutes(alias, []Profile{p}, r, concurrency)
}

func (s Store) SubmitRoutes(alias string, candidates []Profile, r Request, concurrency int) (Job, error) {
	if len(candidates) == 0 {
		return Job{}, fmt.Errorf("no available provider")
	}
	for _, p := range candidates {
		if err := ValidateRequest(p, r); err != nil {
			return Job{}, err
		}
	}
	if _, err := Credential(candidates[0]); err != nil {
		return Job{}, err
	}
	var err error
	s.Root, err = filepath.Abs(expandPath(s.Root))
	if err != nil {
		return Job{}, err
	}
	r.Output, err = filepath.Abs(expandPath(r.Output))
	if err != nil {
		return Job{}, err
	}
	if err = os.MkdirAll(r.Output, 0755); err != nil {
		return Job{}, err
	}
	for i, path := range r.References {
		r.References[i], err = filepath.Abs(expandPath(path))
		if err != nil {
			return Job{}, err
		}
	}
	if r.Mask != "" {
		r.Mask, err = filepath.Abs(expandPath(r.Mask))
		if err != nil {
			return Job{}, err
		}
	}
	if r.Name != "" && (filepath.Base(r.Name) != r.Name || r.Name == "." || r.Name == ".." || strings.ContainsAny(r.Name, "/\\\x00")) {
		return Job{}, fmt.Errorf("name must be a filename stem, not a path")
	}
	j, err := s.create(Job{Alias: alias, Profile: candidates[0], Candidates: candidates, Request: r, Concurrency: concurrency})
	if err != nil {
		return j, err
	}
	executable, err := os.Executable()
	if err != nil {
		return j, err
	}
	d, _ := s.dir(j.ID)
	log, err := os.OpenFile(filepath.Join(d, "worker.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return j, err
	}
	defer log.Close()
	reader, writer, err := os.Pipe()
	if err != nil {
		return j, err
	}
	defer reader.Close()
	cmd := exec.Command(executable, "_worker", "--home", s.Root, "--id", j.ID, "--token", j.WorkerToken)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.ExtraFiles = []*os.File{writer}
	if err = cmd.Start(); err != nil {
		writer.Close()
		_, _ = s.update(j.ID, func(j *Job) { j.Status = "failed"; j.Error = "could not start worker" })
		return j, err
	}
	writer.Close()
	go func() { _ = cmd.Wait() }()
	_ = reader.SetReadDeadline(time.Now().Add(5 * time.Second))
	b := make([]byte, 1)
	_, err = io.ReadFull(reader, b)
	if err != nil || b[0] != 'R' {
		return j, fmt.Errorf("job %s created but worker startup was not confirmed; inspect status (do not resubmit blindly)", j.ID)
	}
	return s.Status(j.ID)
}

func (s Store) slot(ctx context.Context, limit int) (*os.File, error) {
	d := filepath.Join(s.Root, "slots")
	if err := os.MkdirAll(d, 0700); err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for i := 0; i < limit; i++ {
			path := filepath.Join(d, fmt.Sprintf("%d.lock", i))
			f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
			if err != nil {
				return nil, err
			}
			err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
			if err == nil {
				return f, nil
			}
			f.Close()
			if !errors.Is(err, syscall.EWOULDBLOCK) {
				return nil, err
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (s Store) Worker(id, workerToken string, ready io.Writer) error {
	d, err := s.dir(id)
	if err != nil {
		return err
	}
	lock, err := lockFile(filepath.Join(d, "worker.lock"), true)
	if err != nil {
		return err
	}
	defer unlock(lock)
	j, err := s.load(id)
	if err != nil {
		return err
	}
	if j.WorkerToken != workerToken || j.Status != "starting" {
		return fmt.Errorf("worker is not eligible to execute this job")
	}
	defer func() {
		if j.Request.Notify {
			last, e := s.load(id)
			if e == nil && terminal(last.Status) {
				notifyFinished(last)
			}
		}
	}()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				current, e := s.load(id)
				if e == nil && current.CancelRequested {
					cancel()
					return
				}
			}
		}
	}()
	j, err = s.update(id, func(j *Job) { j.Status = "queued"; j.PID = os.Getpid() })
	if err != nil {
		return err
	}
	if ready != nil {
		_, _ = ready.Write([]byte("R"))
	}
	f, err := s.slot(ctx, j.Concurrency)
	if err != nil {
		return s.finishError(id, ctx, "", err, false)
	}
	defer unlock(f)
	current, err := s.load(id)
	if err != nil {
		return err
	}
	if current.CancelRequested {
		cancel()
	}
	if ctx.Err() != nil {
		return s.finishError(id, ctx, "", ctx.Err(), false)
	}
	profiles := j.Candidates
	if len(profiles) == 0 {
		profiles = []Profile{j.Profile}
	}
	stem := j.Request.Name
	if stem == "" {
		stem = j.ID
	}
	for index, p := range profiles {
		if ctx.Err() != nil {
			return s.finishError(id, ctx, "", ctx.Err(), false)
		}
		key, keyErr := Credential(p)
		if keyErr != nil {
			_, _ = s.update(id, func(j *Job) {
				j.Attempts = append(j.Attempts, Attempt{Provider: p.Name, StartedAt: now(), Status: "unavailable", Error: keyErr.Error()})
			})
			if index+1 < len(profiles) {
				continue
			}
			return s.finishError(id, ctx, "", keyErr, false)
		}
		_, err = s.update(id, func(j *Job) {
			j.Status = "running"
			j.Profile = p
			j.Result = nil
			j.Attempts = append(j.Attempts, Attempt{Provider: p.Name, StartedAt: now(), Status: "running"})
		})
		if err != nil {
			return err
		}
		callCtx, timeout := context.WithTimeout(ctx, time.Duration(p.TimeoutSeconds)*time.Second)
		result, callErr := GenerateWithProgress(callCtx, p, key, j.Request, filepath.Join(j.Request.Output, stem), func(result Result) error { _, e := s.update(id, func(j *Job) { j.Result = &result }); return e })
		timeout()
		if callErr != nil && (len(result.Images) > 0 || len(result.Previews) > 0 || result.Path != "") {
			_, _ = s.update(id, func(j *Job) { j.Result = &result })
		}
		if callErr == nil {
			result.Warnings = append(result.Warnings, p.Notes...)
			if e := s.RecordProvider(p, j.Request, true, 0); e != nil {
				result.Warnings = append(result.Warnings, "Provider health could not be recorded")
			}
			_, err = s.update(id, func(j *Job) {
				j.Status = "succeeded"
				j.Result = &result
				j.Error = ""
				j.PID = 0
				j.Attempts[len(j.Attempts)-1].Status = "succeeded"
			})
			return err
		}
		message := strings.ReplaceAll(callErr.Error(), key, "[REDACTED]")
		_, _ = s.update(id, func(j *Job) {
			j.Attempts[len(j.Attempts)-1].Status = "failed"
			j.Attempts[len(j.Attempts)-1].Error = message
		})
		var pe *ProviderError
		if errors.As(callErr, &pe) {
			cooldown := time.Minute
			if pe.StatusCode == 401 || pe.StatusCode == 403 {
				cooldown = 5 * time.Minute
			}
			if pe.RetryAfterSeconds > 0 {
				cooldown = time.Duration(pe.RetryAfterSeconds) * time.Second
			}
			_ = s.RecordProvider(p, j.Request, false, cooldown)
			if pe.SafeFallback && !pe.Unknown && ctx.Err() == nil && len(result.Images) == 0 && len(result.Previews) == 0 && result.Path == "" && index+1 < len(profiles) {
				continue
			}
		}
		return s.finishError(id, ctx, key, callErr, true)
	}
	return s.finishError(id, ctx, "", fmt.Errorf("no available provider"), false)
}

func (s Store) finishError(id string, ctx context.Context, key string, err error, sent bool) error {
	message := err.Error()
	if key != "" {
		message = strings.ReplaceAll(message, key, "[REDACTED]")
	}
	status := "failed"
	var pe *ProviderError
	if errors.As(err, &pe) && pe.Unknown {
		status = "unknown"
	}
	if pe != nil && pe.Partial && !pe.Unknown {
		status = "partial"
	}
	if ctx.Err() != nil {
		status = "cancelled"
		if sent {
			message = "local request cancelled; the provider may still complete and bill it"
		} else {
			message = "cancelled before sending to provider"
		}
	}
	_, saveErr := s.update(id, func(j *Job) { j.Status = status; j.Error = message; j.PID = 0 })
	return saveErr
}

func notifyFinished(j Job) {
	if runtime.GOOS != "darwin" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	message := j.ID + ": " + j.Status
	if j.Result != nil && j.Result.Path != "" {
		message += " — " + filepath.Base(j.Result.Path)
	}
	_ = exec.CommandContext(ctx, "/usr/bin/osascript", "-e", "on run argv\ndisplay notification (item 1 of argv) with title \"Imagen\"\nend run", message).Run()
}
