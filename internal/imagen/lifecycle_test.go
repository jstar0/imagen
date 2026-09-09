package imagen

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// These tests run the actual compiled CLI and detached workers against loopback
// HTTP fixtures; no live provider or user credential is involved.
func TestCLILifecycle(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "imagen")
	build := exec.Command("go", "build", "-mod=mod", "-o", bin, "../../cmd/imagen")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, out)
	}
	t.Run("detached_reference_and_config_independent_status", func(t *testing.T) {
		png := fixturePNG(t)
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			if r.Header.Get("Authorization") != "Bearer lifecycle-only-secret" {
				t.Error("wrong credential")
			}
			if r.URL.Path != "/v1/images/edits" {
				t.Error("wrong edit endpoint")
			}
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Error(err)
				http.Error(w, "bad form", 400)
				return
			}
			defer r.MultipartForm.RemoveAll()
			found := false
			for _, files := range r.MultipartForm.File {
				for _, f := range files {
					h, e := f.Open()
					if e != nil {
						t.Error(e)
						continue
					}
					b, _ := io.ReadAll(h)
					h.Close()
					if bytes.Equal(b, png) {
						found = true
					}
				}
			}
			if !found {
				t.Error("reference image bytes missing")
			}
			replyPNG(w, png)
		}))
		defer srv.Close()
		c := newLifecycleCLI(t, bin, srv.URL)
		if err := os.WriteFile(filepath.Join(c.cwd, "ref.png"), png, 0600); err != nil {
			t.Fatal(err)
		}
		j := c.job("generate", "--prompt", "reference lifecycle", "--reference", "ref.png", "--output", "images")
		// Query from a new process and a different directory after generate exited.
		originalCWD := c.cwd
		if resolved, err := filepath.EvalSymlinks(originalCWD); err == nil {
			originalCWD = resolved
		}
		c.cwd = t.TempDir()
		if err := os.Remove(c.config); err != nil {
			t.Fatal(err)
		}
		done := c.job("wait", j.ID, "--timeout", "10")
		if done.Status != "succeeded" || done.Result == nil {
			t.Fatalf("not completed: %+v", done)
		}
		stored, err := (Store{Root: c.root}).load(done.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(stored.Request.References) != 1 || stored.Request.References[0] != filepath.Join(originalCWD, "ref.png") {
			t.Fatal("relative reference not resolved")
		}
		if !strings.HasPrefix(done.Result.Path, filepath.Join(originalCWD, "images")+string(os.PathSeparator)) {
			t.Fatal("relative output moved with querying cwd")
		}
		actual, err := os.ReadFile(done.Result.Path)
		if err != nil || !bytes.Equal(actual, png) {
			t.Fatalf("saved image mismatch: %v", err)
		}
		if calls.Load() != 1 {
			t.Fatalf("calls=%d", calls.Load())
		}
		c.assertNoSecret()
	})
	t.Run("timeout_queue_cancel_and_concurrency", func(t *testing.T) {
		png := fixturePNG(t)
		entered := make(chan struct{}, 8)
		release := make(chan struct{}, 8)
		var calls, active, maximum atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			n := active.Add(1)
			defer active.Add(-1)
			for old := maximum.Load(); n > old; old = maximum.Load() {
				if maximum.CompareAndSwap(old, n) {
					break
				}
			}
			entered <- struct{}{}
			select {
			case <-release:
				replyPNG(w, png)
			case <-r.Context().Done():
			}
		}))
		defer func() { close(release); srv.Close() }()
		c := newLifecycleCLI(t, bin, srv.URL)
		first := c.job("generate", "--prompt", "first")
		awaitLifecycle(t, entered)
		b, code := c.run("wait", first.ID, "--timeout", "0.05")
		var timed struct {
			Job
			WaitTimeout bool `json:"wait_timeout"`
		}
		if err := json.Unmarshal(b, &timed); err != nil || code != 2 || !timed.WaitTimeout || timed.CancelRequested {
			t.Fatalf("timeout changed task: code=%d %s (%v)", code, b, err)
		}
		second := c.job("generate", "--prompt", "cancel queued")
		if second.Status != "queued" {
			t.Fatalf("expected queued: %+v", second)
		}
		c.job("cancel", second.ID)
		b, code = c.run("wait", second.ID, "--timeout", "5")
		var cancelled Job
		_ = json.Unmarshal(b, &cancelled)
		if code != 1 || cancelled.Status != "cancelled" {
			t.Fatalf("queued cancel: code=%d %s", code, b)
		}
		third := c.job("generate", "--prompt", "third")
		if calls.Load() != 1 {
			t.Fatalf("queue sent requests before slot available: %d", calls.Load())
		}
		release <- struct{}{}
		c.job("wait", first.ID, "--timeout", "5")
		awaitLifecycle(t, entered)
		release <- struct{}{}
		c.job("wait", third.ID, "--timeout", "5")
		if maximum.Load() != 1 || calls.Load() != 2 {
			t.Fatalf("maximum=%d requests=%d", maximum.Load(), calls.Load())
		}
	})
	t.Run("killed_worker_is_unknown_without_retry", func(t *testing.T) {
		entered := make(chan struct{}, 1)
		shutdown := make(chan struct{})
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			calls.Add(1)
			entered <- struct{}{}
			select {
			case <-r.Context().Done():
			case <-shutdown:
			}
		}))
		defer func() { close(shutdown); srv.Close() }()
		c := newLifecycleCLI(t, bin, srv.URL)
		j := c.job("generate", "--prompt", "kill in flight")
		awaitLifecycle(t, entered)
		j, err := (Store{Root: c.root}).load(j.ID)
		if err != nil {
			t.Fatal(err)
		}
		if j.PID <= 0 || j.Status != "running" {
			t.Fatalf("worker not running: %+v", j)
		}
		if err := syscall.Kill(j.PID, syscall.SIGKILL); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			j = c.job("status", j.ID)
			if j.Status == "unknown" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("killed worker stayed %s", j.Status)
			}
			time.Sleep(20 * time.Millisecond)
		}
		for i := 0; i < 3; i++ {
			if got := c.job("status", j.ID); got.Status != "unknown" {
				t.Fatalf("unstable stale status: %+v", got)
			}
		}
		b, code := c.run("wait", j.ID, "--timeout", "1")
		if code != 1 {
			t.Fatalf("unknown wait exit=%d %s", code, b)
		}
		if calls.Load() != 1 {
			t.Fatal("status or wait retried provider")
		}
	})
}

type lifecycleCLI struct {
	t                      *testing.T
	bin, root, config, cwd string
}

func newLifecycleCLI(t *testing.T, bin, url string) *lifecycleCLI {
	t.Helper()
	dir := t.TempDir()
	c := &lifecycleCLI{t: t, bin: bin, root: filepath.Join(dir, "state"), config: filepath.Join(dir, "config.json"), cwd: dir}
	cfg := Config{Version: 1, DefaultModel: "fixture", Concurrency: 1, Models: map[string]Profile{"fixture": {Kind: "gpt-image", Model: "fixture-image", BaseURL: url, APIKeyEnv: "IMAGEN_LIFECYCLE_TEST_KEY", TimeoutSeconds: 15}}}
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(c.config, b, 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		jobs, _ := (Store{Root: c.root}).List(100)
		for _, j := range jobs {
			if !terminal(j.Status) && j.PID > 0 {
				_ = syscall.Kill(j.PID, syscall.SIGKILL)
			}
		}
	})
	return c
}
func (c *lifecycleCLI) run(args ...string) ([]byte, int) {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.bin, append([]string{"--json", "--home", c.root, "--config", c.config}, args...)...)
	cmd.Dir = c.cwd
	cmd.Env = append(os.Environ(), "IMAGEN_LIFECYCLE_TEST_KEY=lifecycle-only-secret")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return stdout.Bytes(), 0
	}
	if e, ok := err.(*exec.ExitError); ok {
		return append(stdout.Bytes(), stderr.Bytes()...), e.ExitCode()
	}
	c.t.Fatalf("run CLI: %v %s", err, stderr.Bytes())
	return nil, -1
}
func (c *lifecycleCLI) job(args ...string) Job {
	c.t.Helper()
	b, code := c.run(args...)
	if code != 0 {
		c.t.Fatalf("%v exit=%d: %s", args, code, b)
	}
	var j Job
	if err := json.Unmarshal(b, &j); err != nil {
		c.t.Fatalf("decode job %s: %v", b, err)
	}
	return j
}
func (c *lifecycleCLI) assertNoSecret() {
	c.t.Helper()
	if err := filepath.WalkDir(c.root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		if bytes.Contains(b, []byte("lifecycle-only-secret")) {
			c.t.Errorf("credential persisted in %s", path)
		}
		return nil
	}); err != nil {
		c.t.Fatal(err)
	}
}
func awaitLifecycle(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("provider was not reached")
	}
}

func TestLifecycleConfigValidationAndCredentialSource(t *testing.T) {
	p := Profile{Kind: "grok", Model: "fixture", BaseURL: "https://example.invalid", APIKeyEnv: "IMAGEN_LIFECYCLE_CONFIG_KEY", TimeoutSeconds: 10}
	t.Setenv(p.APIKeyEnv, "inherited-unrelated-key")
	p.EnvFile = filepath.Join(t.TempDir(), "provider.env")
	if err := os.WriteFile(p.EnvFile, []byte("export IMAGEN_LIFECYCLE_CONFIG_KEY='explicit-key'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := Credential(p)
	if err != nil || got != "explicit-key" {
		t.Fatalf("explicit source not respected: %q %v", got, err)
	}
	if err := os.Chmod(p.EnvFile, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = Credential(p); err == nil {
		t.Fatal("world-readable credential accepted")
	}
	p.EnvFile = filepath.Join(t.TempDir(), "missing")
	if _, err = Credential(p); err == nil {
		t.Fatal("missing explicit source fell back to environment")
	}
	cfg := Config{Version: 1, DefaultModel: "fixture", Concurrency: 1, Models: map[string]Profile{"fixture": p}}
	for _, limit := range []int{0, 17} {
		cfg.Concurrency = limit
		b, _ := json.Marshal(cfg)
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(path); err == nil {
			t.Fatalf("invalid concurrency %d accepted", limit)
		}
	}
	for _, id := range []string{"../escape", "a/b", "", strings.Repeat("x", 101)} {
		if _, err := (Store{Root: t.TempDir()}).Status(id); err == nil {
			t.Fatalf("invalid job ID %q accepted", id)
		}
	}
}
