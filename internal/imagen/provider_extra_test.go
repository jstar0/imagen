package imagen

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestMultiImageAndHeaders(t *testing.T) {
	img := fixturePNG(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-openai-actor-authorization") != "Bearer wecode" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("headers incorrect")
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["n"] != float64(2) || body["output_compression"] != float64(90) {
			t.Errorf("fields: %v", body)
		}
		json.NewEncoder(w).Encode(map[string]any{"model": "actual", "data": []any{map[string]string{"b64_json": base64.StdEncoding.EncodeToString(img)}, map[string]string{"b64_json": base64.StdEncoding.EncodeToString(img)}}})
	}))
	defer srv.Close()
	p := Profile{BaseURL: srv.URL, Kind: "gpt-image", Model: "gpt-image-2", Headers: map[string]string{"x-openai-actor-authorization": "Bearer wecode"}}
	compression := 90
	r := Request{Prompt: "draw", Count: 2, Format: "webp", Compression: &compression}
	stem := filepath.Join(t.TempDir(), "out")
	got, e := Generate(context.Background(), p, "secret", r, stem)
	if e != nil {
		t.Fatal(e)
	}
	if len(got.Images) != 2 || got.Path != stem+"-1.png" || got.Images[1].Path != stem+"-2.png" || got.ReturnedModel != "actual" || len(got.Warnings) == 0 {
		t.Fatalf("%+v", got)
	}
	p.Headers["authorization"] = "oops"
	if _, e = Generate(context.Background(), p, "secret", r, filepath.Join(t.TempDir(), "other")); e == nil {
		t.Fatal("auth override allowed")
	}
}
func TestMaskValidationAndMultipart(t *testing.T) {
	dir := t.TempDir()
	ref := filepath.Join(dir, "ref.png")
	mask := filepath.Join(dir, "mask.png")
	os.WriteFile(ref, fixturePNG(t), 0600)
	os.WriteFile(mask, fixturePNG(t), 0600)
	p := Profile{Kind: "gpt-image", Model: "gpt-image-2", BaseURL: "https://example.com"}
	r := Request{Prompt: "edit", References: []string{ref}, Mask: mask, Background: "transparent"}
	if e := ValidateRequest(p, r); e != nil {
		t.Fatal(e)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if e := q.ParseMultipartForm(1 << 20); e != nil {
			t.Fatal(e)
		}
		defer q.MultipartForm.RemoveAll()
		if len(q.MultipartForm.File["mask"]) != 1 || q.FormValue("background") != "transparent" {
			t.Error("mask/fields missing")
		}
		replyPNG(w, fixturePNG(t))
	}))
	defer srv.Close()
	p.BaseURL = srv.URL
	if _, e := Generate(context.Background(), p, "secret", r, filepath.Join(dir, "out")); e != nil {
		t.Fatal(e)
	}
	opaque := image.NewRGBA(image.Rect(0, 0, 2, 3))
	for y := 0; y < 3; y++ {
		for x := 0; x < 2; x++ {
			opaque.Set(x, y, color.RGBA{255, 0, 0, 255})
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, opaque)
	os.WriteFile(mask, buf.Bytes(), 0600)
	if e := ValidateRequest(p, r); e == nil {
		t.Fatal("opaque nonalpha PNG mask accepted")
	}
	os.WriteFile(mask, fixturePNG(t), 0600)
	r.InputFidelity = "high"
	if e := ValidateRequest(p, r); e == nil {
		t.Fatal("GPT2 input fidelity accepted")
	}
}
func TestStreamingImagesAndInterrupted(t *testing.T) {
	for _, complete := range []bool{true, false} {
		t.Run(fmt.Sprint(complete), func(t *testing.T) {
			b64 := base64.StdEncoding.EncodeToString(fixturePNG(t))
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Accept") != "text/event-stream" {
					t.Error("accept missing")
				}
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				if body["stream"] != true || body["partial_images"] != float64(1) {
					t.Error("stream fields missing")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "event: image_generation.partial_image\ndata: {\"b64_json\":%q,\"partial_image_index\":0}\n\n", b64)
				if complete {
					fmt.Fprintf(w, "data: {\"type\":\"image_generation.completed\",\"b64_json\":%q,\"usage\":{\"total_tokens\":7}}\n\n", b64)
				}
			}))
			defer srv.Close()
			calls := 0
			got, e := GenerateWithProgress(context.Background(), Profile{Kind: "gpt-image", Model: "gpt-image-2", BaseURL: srv.URL}, "secret", Request{Prompt: "draw", Stream: true, PartialImages: 1}, filepath.Join(t.TempDir(), "out"), func(r Result) error {
				calls++
				if len(r.Previews) != 1 {
					t.Error("preview not persisted")
				}
				return nil
			})
			if len(got.Previews) != 1 {
				t.Fatalf("preview missing: %+v", got)
			}
			if complete {
				if e != nil || len(got.Images) != 1 || calls != 2 {
					t.Fatalf("%+v %v %d", got, e, calls)
				}
			} else {
				var pe *ProviderError
				if !errors.As(e, &pe) || !pe.Unknown || pe.SafeFallback || len(got.Images) != 0 {
					t.Fatalf("unsafe interruption: %+v %v", got, e)
				}
			}
		})
	}
}
func TestSafeFallbackClassification(t *testing.T) {
	for _, tc := range []struct {
		status        int
		body          string
		safe, unknown bool
	}{{401, `{"error":"Missing API key"}`, true, false}, {429, `{"error":"rate limited"}`, true, false}, {400, `{"error":{"code":"model_not_found"}}`, true, false}, {403, `{"error":"safety violation"}`, false, false}, {400, `{"error":"invalid request"}`, false, false}, {502, `{"error":"upstream"}`, false, true}} {
		t.Run(fmt.Sprint(tc.status, tc.body), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "5")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			_, e := Generate(context.Background(), Profile{Kind: "gpt-image", Model: "test", BaseURL: srv.URL}, "secret", Request{Prompt: "draw"}, filepath.Join(t.TempDir(), "out"))
			var pe *ProviderError
			if !errors.As(e, &pe) || pe.SafeFallback != tc.safe || pe.Unknown != tc.unknown || pe.StatusCode != tc.status || pe.RetryAfterSeconds != 5 {
				t.Fatalf("%+v", e)
			}
		})
	}
}
func TestPartialMultiResultPreserved(t *testing.T) {
	img := base64.StdEncoding.EncodeToString(fixturePNG(t))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"data":[{"b64_json":%q},{"b64_json":"invalid"}]}`, img)
	}))
	defer srv.Close()
	got, e := Generate(context.Background(), Profile{Kind: "gpt-image", Model: "test", BaseURL: srv.URL}, "secret", Request{Prompt: "draw", Count: 2}, filepath.Join(t.TempDir(), "out"))
	var pe *ProviderError
	if len(got.Images) != 1 || !errors.As(e, &pe) || !pe.Unknown || pe.SafeFallback {
		t.Fatalf("%+v %v", got, e)
	}
}
