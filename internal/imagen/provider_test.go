package imagen

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func fixturePNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if e := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 3))); e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}
func replyPNG(w http.ResponseWriter, b []byte) {
	w.Header().Set("x-request-id", "test-request")
	json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"b64_json": base64.StdEncoding.EncodeToString(b)}}, "usage": map[string]int{"total_tokens": 3}})
}
func TestGenerateProtocolAndOutput(t *testing.T) {
	for _, kind := range []string{"gpt-image", "grok"} {
		t.Run(kind, func(t *testing.T) {
			img := fixturePNG(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/images/generations" || r.Header.Get("Authorization") != "Bearer secret" {
					t.Errorf("wrong endpoint/auth")
				}
				var v map[string]any
				if e := json.NewDecoder(r.Body).Decode(&v); e != nil {
					t.Error(e)
				}
				if v["model"] != "test-model" || v["prompt"] != "draw" || v["n"] != float64(1) {
					t.Errorf("bad fields: %v", v)
				}
				if kind == "gpt-image" {
					if _, ok := v["response_format"]; ok {
						t.Error("GPT must not set response_format")
					}
					if v["output_format"] != "jpeg" {
						t.Error("missing requested format")
					}
				} else {
					if v["response_format"] != "b64_json" || v["resolution"] != "2k" {
						t.Error("missing Grok fields")
					}
				}
				replyPNG(w, img)
			}))
			defer srv.Close()
			p := Profile{Kind: kind, Model: "test-model", BaseURL: srv.URL, TimeoutSeconds: 5}
			r := Request{Prompt: "draw"}
			if kind == "gpt-image" {
				r.Format = "jpeg"
			} else {
				r.Resolution = "2k"
			}
			stem := filepath.Join(t.TempDir(), "out")
			got, e := Generate(context.Background(), p, "secret", r, stem)
			if e != nil {
				t.Fatal(e)
			}
			if got.Path != stem+".png" || got.Format != "png" || got.Width != 2 || got.Height != 3 || got.RequestID != "test-request" || len(got.Usage) == 0 {
				t.Fatalf("incorrect result: %+v", got)
			}
			if _, e = Generate(context.Background(), p, "secret", r, stem); e == nil {
				t.Fatal("overwrite accepted")
			}
		})
	}
}
func TestEditProtocols(t *testing.T) {
	for _, tc := range []struct {
		kind string
		n    int
	}{{"gpt-image", 2}, {"grok", 1}, {"grok", 2}} {
		t.Run(tc.kind+string(rune('0'+tc.n)), func(t *testing.T) {
			img := fixturePNG(t)
			dir := t.TempDir()
			ref := filepath.Join(dir, "ref.png")
			if e := os.WriteFile(ref, img, 0600); e != nil {
				t.Fatal(e)
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/images/edits" {
					t.Error("wrong edits path")
				}
				if tc.kind == "gpt-image" {
					if e := r.ParseMultipartForm(1 << 20); e != nil {
						t.Fatal(e)
					}
					defer r.MultipartForm.RemoveAll()
					if len(r.MultipartForm.File["image[]"]) != tc.n {
						t.Error("missing repeated image[]")
					}
					for _, part := range r.MultipartForm.File["image[]"] {
						if got := part.Header.Get("Content-Type"); got != "image/png" {
							t.Errorf("reference MIME = %q; want image/png", got)
						}
					}
					if r.FormValue("model") != "test" || r.FormValue("n") != "1" {
						t.Error("missing multipart fields")
					}
				} else {
					var v map[string]any
					if e := json.NewDecoder(r.Body).Decode(&v); e != nil {
						t.Fatal(e)
					}
					if tc.n == 1 {
						if v["image"].(map[string]any)["type"] != "image_url" {
							t.Error("bad single image shape")
						}
						if _, ok := v["images"]; ok {
							t.Error("both image and images")
						}
					} else {
						if len(v["images"].([]any)) != 2 {
							t.Error("bad multiple images shape")
						}
						if _, ok := v["image"]; ok {
							t.Error("both image and images")
						}
					}
				}
				replyPNG(w, img)
			}))
			defer srv.Close()
			refs := []string{ref}
			if tc.n == 2 {
				refs = append(refs, ref)
			}
			_, e := Generate(context.Background(), Profile{Kind: tc.kind, Model: "test", BaseURL: srv.URL + "/v1"}, "secret", Request{Prompt: "edit", References: refs}, filepath.Join(dir, "out"))
			if e != nil {
				t.Fatal(e)
			}
		})
	}
}
func TestUnknownOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		unknown bool
	}{{"rejected", 401, "secret must not leak", false}, {"server", 503, "", true}, {"invalid-json", 200, "oops", true}, {"non-image", 200, `{"data":[{"b64_json":"bm90IGFuIGltYWdl"}]}`, true}} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); w.Write([]byte(tc.body)) }))
			defer srv.Close()
			_, e := Generate(context.Background(), Profile{Kind: "grok", Model: "test", BaseURL: srv.URL}, "secret", Request{Prompt: "draw"}, filepath.Join(t.TempDir(), "out"))
			var pe *ProviderError
			if !errors.As(e, &pe) || pe.Unknown != tc.unknown {
				t.Fatalf("wrong error: %v", e)
			}
			if bytes.Contains([]byte(e.Error()), []byte("secret")) {
				t.Fatal("leaked credential")
			}
		})
	}
	t.Run("timeout", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(100 * time.Millisecond) }))
		defer srv.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		_, e := Generate(ctx, Profile{Kind: "grok", Model: "test", BaseURL: srv.URL}, "secret", Request{Prompt: "draw"}, filepath.Join(t.TempDir(), "out"))
		var pe *ProviderError
		if !errors.As(e, &pe) || !pe.Unknown {
			t.Fatalf("wrong error: %v", e)
		}
	})
}
func TestValidateDoesNotDowngrade(t *testing.T) {
	p := Profile{Kind: "grok", Model: "test", BaseURL: "https://api.x.ai/v1"}
	for _, ratio := range []string{"21:9", "5:2"} {
		if e := ValidateRequest(p, Request{Prompt: "x", AspectRatio: ratio}); e != nil {
			t.Fatal(e)
		}
	}
	for _, r := range []Request{{Prompt: "x", Quality: "high"}, {Prompt: "x", Format: "png"}, {Prompt: "x", Size: "1024x1024"}, {Prompt: "x", Resolution: "4k"}} {
		if ValidateRequest(p, r) == nil {
			t.Fatalf("unsupported request accepted: %+v", r)
		}
	}
	for _, u := range []string{"http://example.com", "https://secret@example.com", "https://example.com?token=secret"} {
		p.BaseURL = u
		if ValidateRequest(p, Request{Prompt: "x"}) == nil {
			t.Fatalf("unsafe URL accepted: %s", u)
		}
	}
}

func TestSafeProviderDetail(t *testing.T) {
	if got := safeProviderDetail([]byte(`{"error":"Missing API key"}`), "secret"); got != "Missing API key" {
		t.Fatalf("string error lost: %q", got)
	}
	if got := safeProviderDetail([]byte(`{"error":"secret at https://api.example/?signature=private"}`), "secret"); got != "[REDACTED] at [URL REDACTED]" {
		t.Fatalf("unsafe string error: %q", got)
	}
	detail := safeProviderDetail([]byte(`{"error":{"code":"invalid_key","message":"Key secret failed at https://api.example/image?signature=private"}}`), "secret")
	if !bytes.Contains([]byte(detail), []byte("invalid_key")) || !bytes.Contains([]byte(detail), []byte("[REDACTED]")) {
		t.Fatalf("missing diagnostics: %s", detail)
	}
	for _, forbidden := range []string{"secret", "signature", "private", "api.example"} {
		if bytes.Contains([]byte(detail), []byte(forbidden)) {
			t.Fatalf("leaked %s", forbidden)
		}
	}
	if safeProviderDetail([]byte(`{"unknown":"secret"}`), "secret") != "" {
		t.Fatal("unknown body echoed")
	}
	large, _ := json.Marshal(map[string]any{"error": map[string]string{"message": string(bytes.Repeat([]byte("x"), 1000))}})
	if len([]rune(safeProviderDetail(large, ""))) > 500 {
		t.Fatal("unbounded error")
	}
}
func TestPublishImageNoOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.png")
	if e := publishImage(path, []byte("original")); e != nil {
		t.Fatal(e)
	}
	if e := publishImage(path, []byte("replacement")); e == nil {
		t.Fatal("overwrite accepted")
	}
	b, _ := os.ReadFile(path)
	if string(b) != "original" {
		t.Fatal("original changed")
	}
}
func TestDownloadDoesNotSendCredentials(t *testing.T) {
	img := fixturePNG(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("authorization leaked")
		}
		w.Write(img)
	}))
	defer srv.Close()
	got, e := downloadImage(context.Background(), srv.URL+"/image?signature=private")
	if e != nil || !bytes.Equal(got, img) {
		t.Fatalf("download: %v", e)
	}
}
