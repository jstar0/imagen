package imagen

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestEmptySSEKeepalive(t *testing.T) {
	body := "data: \n\nevent: keepalive\ndata:\n\ndata:    \n\nevent: image_generation.completed\ndata: {\"b64_json\":\"" + base64.StdEncoding.EncodeToString(fixturePNG(t)) + "\"}\n\n"
	result, e := consumeImageStream(strings.NewReader(body), Request{Stream: true}, filepath.Join(t.TempDir(), "out"), Result{}, nil)
	if e != nil || len(result.Images) != 1 {
		t.Fatalf("%+v %v", result, e)
	}
}

func TestCapturedImageStream(t *testing.T) {
	path := os.Getenv("IMAGEN_STREAM_FIXTURE")
	if path == "" {
		t.Skip("optional live capture")
	}
	f, e := os.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	result, e := consumeImageStream(f, Request{Prompt: "capture", Count: 1, Stream: true, PartialImages: 1}, filepath.Join(t.TempDir(), "capture"), Result{}, nil)
	if e != nil || len(result.Images) != 1 {
		t.Fatalf("%+v %v", result, e)
	}
}

func TestCLIUpgradeRoutingAndEditing(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "imagen"+exeSuffix)
	if out, e := exec.Command("go", "build", "-o", bin, "../../cmd/imagen").CombinedOutput(); e != nil {
		t.Fatalf("%v %s", e, out)
	}
	t.Run("terminal_error_after_image_does_not_replay", func(t *testing.T) {
		var secondCalls atomic.Int32
		first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			item := responseImageItem{ID: "ig-1", Type: "image_generation_call", Status: "completed", Result: base64.StdEncoding.EncodeToString(fixturePNG(t))}
			b, _ := json.Marshal(map[string]any{"type": "response.output_item.done", "item": item})
			fmt.Fprintf(w, "data: %s\n\ndata: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"internal_server_error\"}}}\n\n", b)
		}))
		defer first.Close()
		second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { secondCalls.Add(1); replyPNG(w, fixturePNG(t)) }))
		defer second.Close()
		c := newLifecycleCLI(t, bin, first.URL)
		profile := func(url string, priority int) Profile {
			return Profile{Kind: "gpt-image", BaseURL: url, SupportedModels: []string{"gpt-image-2"}, Capabilities: []string{"generate"}, APIKeyEnv: "IMAGEN_LIFECYCLE_TEST_KEY", TimeoutSeconds: 5, Priority: priority}
		}
		p := profile(first.URL, 1)
		p.GenerationAPI = "responses"
		p.ResponsesModel = "test-orchestrator"
		cfg := Config{Version: 2, DefaultModel: "gpt-image-2", Concurrency: 1, Providers: map[string]Profile{"first": p, "second": profile(second.URL, 2)}}
		b, _ := json.Marshal(cfg)
		os.WriteFile(c.config, b, 0600)
		b, code := c.run("generate", "--prompt", "draw", "--wait", "--timeout", "8")
		var j Job
		json.Unmarshal(b, &j)
		if code != 1 || secondCalls.Load() != 0 || len(j.Attempts) != 1 || j.Result == nil || len(j.Result.Images) != 1 {
			t.Fatalf("partial replay or lost image: %d %s", code, b)
		}
	})
	for _, tc := range []struct {
		name       string
		status     int
		body       string
		pinned     bool
		wantSecond int32
		wantCode   int
	}{
		{"auth_fails_over", 401, `{"error":"Invalid API key"}`, false, 1, 0},
		{"pinned_does_not", 401, `{"error":"Invalid API key"}`, true, 0, 1},
		{"gateway_fails_over", 502, `{"error":"upstream lost"}`, false, 1, 0},
		{"timeout_fails_over", 504, `<html>gateway timeout</html>`, false, 1, 0},
		{"duojie_eof_fails_over", 200, `{"error":{"code":"internal_server_error","message":"Post upstream: EOF"}}`, false, 1, 0},
		{"pinned_server_does_not", 504, `gateway timeout`, true, 0, 1},
		{"empty_unknown_does_not", 200, `{"data":[]}`, false, 0, 1},
		{"bad_parameter_does_not", 400, `{"error":{"code":"invalid_parameter"}}`, false, 0, 1},
		{"server_safety_does_not", 500, `{"error":{"code":"content_policy_violation"}}`, false, 0, 1},
		{"all_fail_once", 504, `gateway timeout`, false, 1, 1},
		{"moderation_does_not", 400, `{"error":{"code":"moderation_blocked"}}`, false, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var firstCalls atomic.Int32
			var secondCalls atomic.Int32
			first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				firstCalls.Add(1)
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer first.Close()
			second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				secondCalls.Add(1)
				if tc.name == "all_fail_once" {
					w.WriteHeader(503)
					return
				}
				var payload map[string]any
				if r.URL.Path == "/v1/images/generations" {
					json.NewDecoder(r.Body).Decode(&payload)
					if payload["model"] != "gpt-image-2" {
						t.Error("model changed")
					}
				}
				replyPNG(w, fixturePNG(t))
			}))
			defer second.Close()
			c := newLifecycleCLI(t, bin, first.URL)
			profile := func(url string, priority int) Profile {
				return Profile{Kind: "gpt-image", BaseURL: url, SupportedModels: []string{"gpt-image-2"}, Capabilities: []string{"generate", "edit"}, APIKeyEnv: "IMAGEN_LIFECYCLE_TEST_KEY", TimeoutSeconds: 5, Priority: priority}
			}
			cfg := Config{Version: 2, DefaultModel: "gpt-image-2", Concurrency: 1, Providers: map[string]Profile{"first": profile(first.URL, 1), "second": profile(second.URL, 2)}}
			b, _ := json.Marshal(cfg)
			os.WriteFile(c.config, b, 0600)
			args := []string{"generate", "--prompt", "cup", "--wait", "--timeout", "8"}
			if tc.pinned {
				args = append(args, "--provider", "first")
			}
			b, code := c.run(args...)
			if code != tc.wantCode {
				t.Fatalf("code %d want%d: %s", code, tc.wantCode, b)
			}
			if secondCalls.Load() != tc.wantSecond {
				t.Fatalf("second calls %d want%d", secondCalls.Load(), tc.wantSecond)
			}
			if firstCalls.Load() != 1 {
				t.Fatal("first provider replayed")
			}
			var j Job
			if json.Unmarshal(b, &j) != nil {
				t.Fatalf("bad job: %s", b)
			}
			if tc.wantSecond == 1 && (len(j.Attempts) != 2 || !j.Attempts[0].FellBack || j.Attempts[0].Response == nil) {
				t.Fatalf("fallback metadata missing: %s", b)
			}
			if tc.wantCode == 0 && (tc.status >= 500 || tc.name == "duojie_eof_fails_over") && (!j.Attempts[0].BillingUnknown || j.Result == nil || len(j.Result.Warnings) == 0) {
				t.Fatalf("billing warning missing: %s", b)
			}
			if tc.wantCode == 0 {
				if len(j.Attempts) != 2 || j.Attempts[0].Status != "failed" || j.Attempts[1].Status != "succeeded" {
					t.Fatalf("attempt evidence missing: %s", b)
				}
				edited := c.job("edit", "--from", j.ID, "--prompt", "change color", "--provider", "second", "--name", "edited", "--wait", "--timeout", "8")
				if edited.Result == nil || filepath.Base(edited.Result.Path) != "edited.png" {
					t.Fatal("from/name failed")
				}
			}
			c.assertNoSecret()
		})
	}
}

func TestSerialBatchAndJSONEdit(t *testing.T) {
	t.Setenv("UPGRADE_KEY", "secret")
	img := fixturePNG(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var v map[string]any
		if e := json.NewDecoder(r.Body).Decode(&v); e != nil {
			t.Error(e)
		}
		if v["n"] != float64(1) {
			t.Error("single image mode must send n=1")
		}
		if v["prompt"] != "cup\n\nAvoid: logo" {
			t.Error("negative prompt lost")
		}
		imgs := v["images"].([]any)
		data := imgs[0].(map[string]any)["image_url"].(string)
		if data != "data:image/png;base64,"+base64.StdEncoding.EncodeToString(img) {
			t.Error("JSON edit bytes differ")
		}
		replyPNG(w, img)
	}))
	defer server.Close()
	dir := t.TempDir()
	ref := filepath.Join(dir, "ref.png")
	os.WriteFile(ref, img, 0600)
	p := Profile{Kind: "gpt-image", Model: "gpt-image-2", BaseURL: server.URL, EditEncoding: "json", SingleImageRequests: true}
	result, e := Generate(context.Background(), p, "secret", Request{Prompt: "cup", NegativePrompt: "logo", Count: 2, References: []string{ref}}, filepath.Join(dir, "pair"))
	if e != nil || len(result.Images) != 2 || calls.Load() != 2 {
		t.Fatalf("%+v %v calls%d", result, e, calls.Load())
	}
	for _, f := range result.Images {
		b, _ := os.ReadFile(f.Path)
		if !bytes.Equal(img, b) {
			t.Error("saved bytes differ")
		}
	}
	_, e = Generate(context.Background(), p, "secret", Request{Prompt: "cup", NegativePrompt: "logo", Count: 2, References: []string{ref}}, filepath.Join(dir, "pair"))
	if e == nil || calls.Load() != 2 {
		t.Fatal("collision sent paid requests")
	}
}

func TestPresetNormalization(t *testing.T) {
	base := Profile{Kind: "gpt-image", Model: "gpt-image-2.5", BaseURL: "https://example.test"}
	if e := ValidateRequest(base, Request{Prompt: "test", Count: 1, Quality: "max"}); e != nil {
		t.Fatalf("exact GPT Image 2.5 rejected max quality: %v", e)
	}
	base.Model = "gpt-image-2"
	if e := ValidateRequest(base, Request{Prompt: "test", Count: 1, Quality: "max"}); e == nil {
		t.Fatal("GPT Image 2 accepted unsupported max quality")
	}
	for _, tc := range []struct {
		model, size, ratio, wantSize, wantResolution string
		fail                                         bool
	}{
		{"gpt-image-2", "2K", "16:9", "2560x1440", "", false},
		{"gpt-image-2", "4K", "1:1", "2880x2880", "", false},
		{"grok-imagine-image-2.0", "1K", "3:2", "", "1k", false},
		{"grok-imagine-image-2.0", "4K", "3:2", "", "", true},
		{"gpt-image-2", "1024x1024", "16:9", "", "", true},
	} {
		r, e := NormalizeRequest(tc.model, Request{Count: 1, Size: tc.size, AspectRatio: tc.ratio})
		if (e != nil) != tc.fail {
			t.Fatalf("%+v: %v", tc, e)
		}
		if !tc.fail && (r.Size != tc.wantSize || r.Resolution != tc.wantResolution) {
			t.Fatalf("%+v got %+v", tc, r)
		}
	}
}
