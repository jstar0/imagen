package imagen

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"syscall"
	"time"
)

var validID = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

type Store struct{ Root string }

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }
func token() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func (s Store) dir(id string) (string, error) {
	if !validID.MatchString(id) || len(id) > 100 {
		return "", fmt.Errorf("invalid job id")
	}
	return filepath.Join(s.Root, "jobs", id), nil
}

func atomicJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".state-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func (s Store) create(j Job) (Job, error) {
	j.ID = time.Now().UTC().Format("20060102T150405") + "-" + token()[:12]
	j.WorkerToken = token()
	j.Status = "starting"
	j.CreatedAt = now()
	j.UpdatedAt = j.CreatedAt
	d, _ := s.dir(j.ID)
	if err := os.MkdirAll(d, 0700); err != nil {
		return j, err
	}
	for _, name := range []string{"worker.lock", "state.lock"} {
		f, err := os.OpenFile(filepath.Join(d, name), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if err != nil {
			return j, err
		}
		f.Close()
	}
	return j, atomicJSON(filepath.Join(d, "job.json"), j)
}

func (s Store) load(id string) (Job, error) {
	var j Job
	d, err := s.dir(id)
	if err != nil {
		return j, err
	}
	b, err := os.ReadFile(filepath.Join(d, "job.json"))
	if err != nil {
		return j, err
	}
	err = json.Unmarshal(b, &j)
	return j, err
}

func lockFile(path string, nonblock bool) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	flags := syscall.LOCK_EX
	if nonblock {
		flags |= syscall.LOCK_NB
	}
	if err = syscall.Flock(int(f.Fd()), flags); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
func unlock(f *os.File) { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }

func (s Store) update(id string, fn func(*Job)) (Job, error) {
	d, err := s.dir(id)
	if err != nil {
		return Job{}, err
	}
	f, err := lockFile(filepath.Join(d, "state.lock"), false)
	if err != nil {
		return Job{}, err
	}
	defer unlock(f)
	j, err := s.load(id)
	if err != nil {
		return j, err
	}
	fn(&j)
	j.UpdatedAt = now()
	return j, atomicJSON(filepath.Join(d, "job.json"), j)
}

func terminal(status string) bool {
	switch status {
	case "succeeded", "failed", "unknown", "cancelled", "interrupted", "partial":
		return true
	}
	return false
}

// Status is observational: a stale worker is never restarted by a read.
func (s Store) Status(id string) (Job, error) {
	j, err := s.load(id)
	if err == nil && j.Status == "succeeded" && j.Result != nil {
		count := len(j.Result.Images)
		if count == 0 && j.Result.Path != "" {
			count = 1
		}
		if count != imageCount(j.Request) {
			j.Status = "partial"
			j.Error = "provider returned fewer images than requested; saved outputs retained"
		}
	}
	if err != nil || terminal(j.Status) {
		return j, err
	}
	d, _ := s.dir(id)
	f, err := lockFile(filepath.Join(d, "worker.lock"), true)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return j, nil
	}
	if err != nil {
		return j, err
	}
	defer unlock(f)
	// Re-read while holding the worker lock to avoid reporting a just-finished job as lost.
	j, err = s.load(id)
	if err != nil || terminal(j.Status) {
		return j, err
	}
	created, _ := time.Parse(time.RFC3339Nano, j.CreatedAt)
	if j.Status == "starting" && time.Since(created) < 10*time.Second {
		return j, nil
	}
	if j.Status == "running" {
		j.Status = "unknown"
		j.Error = "worker stopped; the provider may have completed and billed the request; no automatic retry"
	} else {
		j.Status = "interrupted"
		j.Error = "worker stopped before an API request was recorded; no automatic restart"
	}
	return j, nil
}

func (s Store) List(limit int) ([]Job, error) {
	entries, err := os.ReadDir(filepath.Join(s.Root, "jobs"))
	if os.IsNotExist(err) {
		return []Job{}, nil
	}
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() > entries[j].Name() })
	jobs := []Job{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		j, err := s.Status(e.Name())
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
		if len(jobs) >= limit {
			break
		}
	}
	return jobs, nil
}

func (s Store) Cancel(id string) (Job, error) {
	j, err := s.Status(id)
	if err != nil || terminal(j.Status) {
		return j, err
	}
	return s.update(id, func(j *Job) {
		if !terminal(j.Status) {
			j.CancelRequested = true
		}
	})
}
