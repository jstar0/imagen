package imagen

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// nativeImageRelay invokes one bounded built-in image operation locally. The
// only external request is the requested image API call, sent by the genuine
// Codex Images client. No remote coding turn, copied UA, auth.json or global
// Codex settings are involved. Imagen owns the complete upstream image result.
type nativeImageRelay struct {
	key, prompt string
	references  []string
	transparent bool
	target      *http.Request
	body        []byte
	client      *http.Client
	result      chan nativeImageReply
	turns       atomic.Int32
	images      atomic.Bool
}

type nativeImageReply struct {
	response *http.Response
	err      error
}

func nativeControlSSE(w http.ResponseWriter, id string, item map[string]any) {
	w.Header().Set("Content-Type", "text/event-stream")
	added := make(map[string]any, len(item))
	for key, value := range item {
		added[key] = value
	}
	added["status"] = "in_progress"
	if item["type"] == "function_call" {
		added["arguments"] = ""
	}
	for _, event := range []map[string]any{
		{"type": "response.created", "response": map[string]any{"id": id, "status": "in_progress"}},
		{"type": "response.output_item.added", "output_index": 0, "item": added},
		{"type": "response.output_item.done", "output_index": 0, "item": item},
		{"type": "response.completed", "response": map[string]any{"id": id, "status": "completed", "output": []any{item}, "usage": map[string]int{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}}},
	} {
		b, _ := json.Marshal(event)
		fmt.Fprintf(w, "data: %s\n\n", b)
	}
}

func (relay *nativeImageRelay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer "+relay.key {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	defer r.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, maxImageBytes*2+1))
	if r.URL.Path == "/v1/responses" {
		if relay.turns.Add(1) == 1 {
			args := map[string]any{"prompt": relay.prompt, "transparent_background": relay.transparent}
			if len(relay.references) > 0 {
				// The native executor accepts five references. The target request
				// retains all user-validated references, mask and API parameters.
				args["referenced_image_paths"] = relay.references[:min(5, len(relay.references))]
			}
			arguments, _ := json.Marshal(args)
			nativeControlSSE(w, "imagen-control-1", map[string]any{"type": "function_call", "id": "fc-imagen", "call_id": "imagen-native-call", "namespace": "image_gen", "name": "imagegen", "arguments": string(arguments), "status": "completed"})
		} else {
			nativeControlSSE(w, "imagen-control-2", map[string]any{"type": "message", "id": "msg-imagen", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "Done", "annotations": []any{}}}})
		}
		return
	}
	if r.URL.Path != relay.target.URL.Path || r.Header.Get("X-Codex-Image-Turn-Id") == "" || !relay.images.CompareAndSwap(false, true) {
		http.Error(w, "Unexpected native image request", http.StatusBadRequest)
		return
	}
	upstream := relay.target.Clone(relay.target.Context())
	upstream.Body = io.NopCloser(bytes.NewReader(relay.body))
	upstream.ContentLength = int64(len(relay.body))
	upstream.Header = r.Header.Clone()
	for _, name := range []string{"Cookie", "Connection", "Content-Length", "Proxy-Authorization"} {
		upstream.Header.Del(name)
	}
	// Preserve genuine native client identity; only the image API payload and
	// its content type come from the user's Imagen request.
	for name, values := range relay.target.Header {
		upstream.Header[name] = append([]string(nil), values...)
	}
	response, err := relay.client.Do(upstream)
	if err != nil {
		relay.result <- nativeImageReply{err: providerFailure(true, "native image request transport failed; remote outcome unknown")}
		http.Error(w, "Native image transport failed", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxImageBytes*2+1))
	if err != nil || len(raw) > maxImageBytes*2 {
		relay.result <- nativeImageReply{err: providerFailure(true, "native image response interrupted or too large; remote outcome unknown")}
		http.Error(w, "Native image response unavailable", http.StatusBadGateway)
		return
	}
	for name, values := range response.Header {
		w.Header()[name] = append([]string(nil), values...)
	}
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(raw)
	copy := *response
	copy.Header = response.Header.Clone()
	copy.Body = io.NopCloser(bytes.NewReader(raw))
	relay.result <- nativeImageReply{response: &copy}
}

func nativeCodexBinary() (string, error) {
	if explicit := os.Getenv("IMAGEN_CODEX_BINARY"); explicit != "" {
		if info, err := os.Stat(explicit); err == nil && isExecutableFile(explicit, info) {
			return explicit, nil
		}
		return "", fmt.Errorf("IMAGEN_CODEX_BINARY is not executable")
	}
	for _, candidate := range []string{
		"/Applications/ChatGPT.app/Contents/Resources/codex-cli/CodexCLI.app/Contents/MacOS/codex",
		"/Applications/Codex.app/Contents/Resources/codex",
	} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	path, err := exec.LookPath("codex")
	if err != nil {
		return "", err
	}
	return nativeCodexExecutable(path), nil
}

func nativeCodexConfig(p Profile, address string) string {
	keys := make([]string, 0, len(p.Headers))
	for key := range p.Headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var headers []string
	for _, key := range keys {
		headers = append(headers, strconv.Quote(key)+" = "+strconv.Quote(p.Headers[key]))
	}
	return fmt.Sprintf("model_provider = \"imagen\"\nmodel = %q\n[features]\nimage_generation = true\nshell_tool = false\n[model_providers.imagen]\nname = \"OpenAI\"\nbase_url = %q\nwire_api = \"responses\"\nrequires_openai_auth = false\nenv_key = \"IMAGEN_NATIVE_KEY\"\nsupports_websockets = false\nrequest_max_retries = 0\nstream_max_retries = 0\nhttp_headers = { %s }\n", p.ResponsesModel, "http://"+address+"/v1", strings.Join(headers, ", "))
}

func codexNativeImageRequest(ctx context.Context, p Profile, key string, r Request, target *http.Request, client *http.Client) (*http.Response, error) {
	binary, err := nativeCodexBinary()
	if err != nil {
		return nil, providerFailure(false, "native Codex image transport requires an installed Codex client")
	}
	versionBytes, err := exec.CommandContext(ctx, binary, "--version").Output()
	versionFields := strings.Fields(string(versionBytes))
	if err != nil || len(versionFields) < 2 {
		return nil, providerFailure(false, "cannot identify the installed native Codex client")
	}
	version := versionFields[len(versionFields)-1]
	body, err := io.ReadAll(target.Body)
	_ = target.Body.Close()
	if err != nil {
		return nil, providerFailure(false, "cannot prepare native image request")
	}
	home, err := os.MkdirTemp("", "imagen-codex-")
	if err != nil {
		return nil, providerFailure(false, "cannot prepare isolated native image client")
	}
	defer os.RemoveAll(home)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, providerFailure(false, "cannot start loopback native image relay")
	}
	prompt := r.Prompt
	if r.NegativePrompt != "" {
		prompt += "\n\nAvoid: " + r.NegativePrompt
	}
	relay := &nativeImageRelay{key: key, prompt: prompt, references: r.References, transparent: r.Background == "transparent", target: target, body: body, client: client, result: make(chan nativeImageReply, 1)}
	server := &http.Server{Handler: relay, ReadHeaderTimeout: 10 * time.Second}
	go server.Serve(listener)
	defer server.Close()
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(nativeCodexConfig(p, listener.Addr().String())), 0600); err != nil {
		return nil, providerFailure(false, "cannot configure isolated native image client")
	}
	nativeCtx, stop := context.WithCancel(ctx)
	defer stop()
	cmd := exec.CommandContext(nativeCtx, binary, "app-server", "--stdio")
	for _, value := range os.Environ() {
		name, _, _ := strings.Cut(value, "=")
		if !oneOf(name, "CODEX_HOME", "IMAGEN_NATIVE_KEY", "OPENAI_API_KEY", "CODEX_API_KEY", "CODEX_ACCESS_TOKEN") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Env = append(cmd.Env, "CODEX_HOME="+home, "IMAGEN_NATIVE_KEY="+key)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, providerFailure(false, "cannot start native image client")
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, providerFailure(false, "cannot start native image client")
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, providerFailure(false, "cannot start native image client")
	}
	var writeMu sync.Mutex
	send := func(v any) {
		writeMu.Lock()
		defer writeMu.Unlock()
		b, _ := json.Marshal(v)
		_, _ = stdin.Write(append(b, '\n'))
	}
	send(map[string]any{"id": 1, "method": "initialize", "params": map[string]any{"clientInfo": map[string]any{"name": "codex_cli_rs", "version": version}, "capabilities": map[string]bool{"experimentalApi": true}}})
	rpcDone := make(chan struct{})
	go func() {
		defer close(rpcDone)
		decoder := json.NewDecoder(stdout)
		for {
			var message struct {
				ID     int             `json:"id"`
				Method string          `json:"method"`
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
			}
			if decoder.Decode(&message) != nil || (len(message.Error) > 0 && string(message.Error) != "null") {
				return
			}
			switch message.ID {
			case 1:
				send(map[string]any{"method": "initialized"})
				send(map[string]any{"id": 2, "method": "thread/start", "params": map[string]any{"cwd": home, "model": p.ResponsesModel, "modelProvider": "imagen", "approvalPolicy": "never", "sandbox": "read-only", "ephemeral": true}})
			case 2:
				var result struct{ Thread struct{ ID string } }
				if json.Unmarshal(message.Result, &result) != nil || result.Thread.ID == "" {
					return
				}
				send(map[string]any{"id": 3, "method": "turn/start", "params": map[string]any{"threadId": result.Thread.ID, "input": []any{map[string]string{"type": "text", "text": "Execute the requested image operation."}}, "effort": "low"}})
			}
			if message.Method == "turn/completed" {
				return
			}
		}
	}()
	defer func() { stop(); _ = stdin.Close(); _ = cmd.Wait() }()
	startup := time.NewTimer(30 * time.Second)
	defer startup.Stop()
	for {
		select {
		case reply := <-relay.result:
			return reply.response, reply.err
		case <-rpcDone:
			select {
			case reply := <-relay.result:
				return reply.response, reply.err
			default:
				return nil, providerFailure(relay.images.Load(), "native Codex image operation did not return an image API response")
			}
		case <-startup.C:
			if !relay.images.Load() {
				return nil, providerFailure(false, "native Codex did not start the image operation")
			}
		case <-ctx.Done():
			return nil, providerFailure(relay.images.Load(), "native image operation timed out or was cancelled; remote outcome may be unknown")
		}
	}
}
