package imagen

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestResponsesGenerationProtocol(t *testing.T) {
	png := fixturePNG(t)
	item := responseImageItem{ID: "ig-1", Type: "image_generation_call", Status: "completed", Result: base64.StdEncoding.EncodeToString(png)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("path: %s", r.URL.Path)
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		tool := body["tools"].([]any)[0].(map[string]any)
		if body["model"] != "orchestrator" || body["stream"] != true || body["store"] != false || tool["model"] != "gpt-image-2" || tool["quality"] != "high" || tool["size"] != "1536x1024" {
			t.Errorf("wrong body: %+v", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		b, _ := json.Marshal(map[string]any{"type": "response.output_item.done", "item": item})
		fmt.Fprintf(w, "data: %s\n\n", b)
		b, _ = json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{item}, "usage": map[string]int{"total_tokens": 10}}})
		fmt.Fprintf(w, "data: %s\n\n", b)
	}))
	defer srv.Close()
	p := Profile{Kind: "gpt-image", Model: "gpt-image-2", GenerationAPI: "responses", ResponsesModel: "orchestrator", BaseURL: srv.URL}
	result, err := Generate(context.Background(), p, "secret", Request{Prompt: "draw", Quality: "high", Size: "1536x1024"}, filepath.Join(t.TempDir(), "out"))
	if err != nil || len(result.Images) != 1 || result.TransportModel != "orchestrator" {
		t.Fatalf("%+v %v", result, err)
	}
	if result.Images[0].Width != 2 || result.Images[0].Height != 3 {
		t.Fatal("image not decoded")
	}
}

func TestResponsesIncompleteRetainsImage(t *testing.T) {
	item := responseImageItem{ID: "ig-1", Type: "image_generation_call", Status: "completed", Result: base64.StdEncoding.EncodeToString(fixturePNG(t))}
	b, _ := json.Marshal(map[string]any{"type": "response.output_item.done", "item": item})
	stream := fmt.Sprintf("data: %s\n\ndata: {\"type\":\"response.failed\"}\n\n", b)
	result, err := consumeResponsesStream(strings.NewReader(stream), Request{}, filepath.Join(t.TempDir(), "out"), Result{}, nil)
	if err == nil || len(result.Images) != 1 {
		t.Fatalf("lost partial evidence: %+v %v", result, err)
	}
}

func TestResponsesRequiresFinalImage(t *testing.T) {
	for _, stream := range []string{"data: [DONE]\n\n", `data: {"type":"response.completed","response":{"status":"completed","output":[]}}` + "\n\n"} {
		_, err := consumeResponsesStream(strings.NewReader(stream), Request{}, filepath.Join(t.TempDir(), "out"), Result{}, nil)
		if err == nil {
			t.Fatal("accepted empty response")
		}
	}
}
