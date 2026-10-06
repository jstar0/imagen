package imagen

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestNativeImageRelayPreservesRequestAndNativeIdentity(t *testing.T) {
	var calls atomic.Int32
	requested := []byte(`{"model":"gpt-image-2.5","prompt":"edit","n":2,"mask":"original-mask","image":["reference-1","reference-2"],"output_format":"webp","output_compression":72}`)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		if !bytes.Equal(body, requested) || r.Header.Get("Authorization") != "Bearer original-key" || r.Header.Get("User-Agent") != "native-client-from-unit-fixture" || r.Header.Get("X-Codex-Image-Turn-Id") != "unit-turn" || r.Header.Get("Cookie") != "" || r.Header.Get("X-Openai-Actor-Authorization") != "Bearer wecode" {
			t.Error("image parameters, native identity or original credential were changed")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Wecode-Request-Id", "fixture-request")
		w.WriteHeader(403)
		io.WriteString(w, `{"error":{"code":"non_codex_client"}}`)
	}))
	defer upstream.Close()
	target, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, upstream.URL+"/v1/images/edits", bytes.NewReader(requested))
	target.Header.Set("Content-Type", "application/json")
	target.Header.Set("X-Openai-Actor-Authorization", "Bearer wecode")
	relay := &nativeImageRelay{key: "original-key", target: target, body: requested, client: upstream.Client(), result: make(chan nativeImageReply, 1)}
	request := httptest.NewRequest(http.MethodPost, "/v1/images/edits", strings.NewReader("native executor payload"))
	request.Header.Set("Authorization", "Bearer original-key")
	request.Header.Set("User-Agent", "native-client-from-unit-fixture")
	request.Header.Set("X-Codex-Image-Turn-Id", "unit-turn")
	request.Header.Set("Cookie", "private-cookie")
	w := httptest.NewRecorder()
	relay.ServeHTTP(w, request)
	reply := <-relay.result
	if reply.err != nil || reply.response.StatusCode != 403 || reply.response.Header.Get("X-Wecode-Request-Id") != "fixture-request" || w.Code != 403 {
		t.Fatalf("upstream diagnostic was not retained: %v", reply.err)
	}
	raw, _ := io.ReadAll(reply.response.Body)
	if !strings.Contains(string(raw), "non_codex_client") {
		t.Fatal("upstream error was replaced with local success")
	}
	second := request.Clone(context.Background())
	second.Body = io.NopCloser(strings.NewReader("retry"))
	w = httptest.NewRecorder()
	relay.ServeHTTP(w, second)
	if w.Code != 400 || calls.Load() != 1 {
		t.Fatal("duplicate image submission was billed")
	}
}

func TestNativeImageRelayRejectsUnprovenImageCalls(t *testing.T) {
	target, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1/v1/images/generations", nil)
	for _, key := range []string{"", "wrong-key", "original-key"} {
		relay := &nativeImageRelay{key: "original-key", target: target, result: make(chan nativeImageReply, 1)}
		r := httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
		r.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		relay.ServeHTTP(w, r)
		if w.Code < 400 || relay.images.Load() {
			t.Fatal("accepted a call without the original key and native image turn")
		}
	}
}

func TestNativeImageControlHasOnlyRequestedImageOperation(t *testing.T) {
	relay := &nativeImageRelay{key: "original-key", prompt: "requested visual brief", references: []string{"1", "2", "3", "4", "5", "6"}, transparent: true}
	r := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	r.Header.Set("Authorization", "Bearer original-key")
	w := httptest.NewRecorder()
	relay.ServeHTTP(w, r)
	var completed struct {
		Response struct {
			Output []struct{ Namespace, Name, Arguments string }
		}
	}
	for _, line := range strings.Split(w.Body.String(), "\n") {
		if strings.HasPrefix(line, "data: ") && strings.Contains(line, `"type":"response.completed"`) {
			_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &completed)
		}
	}
	if len(completed.Response.Output) != 1 || completed.Response.Output[0].Namespace != "image_gen" || completed.Response.Output[0].Name != "imagegen" {
		t.Fatal("local control flow invokes tools other than native image generation")
	}
	var args struct {
		Prompt      string   `json:"prompt"`
		References  []string `json:"referenced_image_paths"`
		Transparent bool     `json:"transparent_background"`
	}
	_ = json.Unmarshal([]byte(completed.Response.Output[0].Arguments), &args)
	if args.Prompt != relay.prompt || len(args.References) != 5 || !args.Transparent {
		t.Fatal("local native operation does not match the requested visual brief")
	}
}

func TestNativeImageConfigIsIsolatedAndHealthChanges(t *testing.T) {
	p := Profile{Kind: "gpt-image", Model: "gpt-image-2.5", BaseURL: "https://api.wecodemaster.com/v1", GenerationAPI: "images", ResponsesModel: "gpt-5.6-luna", Headers: map[string]string{"x-openai-actor-authorization": "Bearer wecode"}}
	old := fingerprint(p, "original-key")
	p.NativeCodex = true
	if old == fingerprint(p, "original-key") {
		t.Fatal("fixed transport retains the old failed-channel cooldown")
	}
	config := nativeCodexConfig(p, "127.0.0.1:1234")
	if strings.Contains(config, "original-key") || !strings.Contains(config, "env_key = \"IMAGEN_NATIVE_KEY\"") || !strings.Contains(config, "request_max_retries = 0") || !strings.Contains(config, "shell_tool = false") {
		t.Fatal("native transport writes a key or allows hidden API retries/shell tools")
	}
	if err := ValidateRequest(p, Request{Prompt: "draw"}); err != nil {
		t.Fatal(err)
	}
	p.GenerationAPI = "responses"
	if err := ValidateRequest(p, Request{Prompt: "draw"}); err == nil {
		t.Fatal("native Images transport accepted incompatible Responses encoding")
	}
}
