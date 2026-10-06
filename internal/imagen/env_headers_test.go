package imagen

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnvironmentHeadersRemainReferences(t *testing.T) {
	t.Setenv("IMAGEN_FIXTURE_ACTOR", "Bearer fixture-actor-secret")
	p := Profile{Kind: "gpt-image", Model: "gpt-image-2.5", ResponsesModel: "fixture-bootstrap", EnvHeaders: map[string]string{"x-openai-actor-authorization": "IMAGEN_FIXTURE_ACTOR"}}
	encoded, err := json.Marshal(p)
	if err != nil || strings.Contains(string(encoded), "fixture-actor-secret") {
		t.Fatal("credential persisted in profile", err)
	}
	config := nativeCodexConfig(p, "127.0.0.1:1234")
	if strings.Contains(config, "fixture-actor-secret") || !strings.Contains(config, `env_http_headers = { "x-openai-actor-authorization" = "IMAGEN_FIXTURE_ACTOR" }`) || !strings.Contains(config, `"x-openai-actor-authorization" = "Bearer wecode"`) {
		t.Fatal("native config lost the environment reference or wrote the actor credential")
	}
	old := fingerprint(p, "fixture-key")
	t.Setenv("IMAGEN_FIXTURE_ACTOR", "Bearer rotated-fixture")
	if old == fingerprint(p, "fixture-key") {
		t.Fatal("rotating actor credential did not reset channel health")
	}
}

func TestEnvironmentHeadersTransmitAndRedact(t *testing.T) {
	t.Setenv("IMAGEN_FIXTURE_ACTOR", "Bearer fixture-actor-secret")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-openai-actor-authorization") != "Bearer fixture-actor-secret" {
			t.Error("environment header not transmitted")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(403)
		w.Write([]byte(`{"error":{"message":"rejected Bearer fixture-actor-secret and fixture-actor-secret"}}`))
	}))
	defer srv.Close()
	p := Profile{Kind: "gpt-image", Model: "gpt-image-2.5", BaseURL: srv.URL, EnvHeaders: map[string]string{"x-openai-actor-authorization": "IMAGEN_FIXTURE_ACTOR"}}
	_, err := Generate(context.Background(), p, "fixture-key", Request{Prompt: "fixture"}, filepath.Join(t.TempDir(), "image"))
	if err == nil || strings.Contains(err.Error(), "fixture-actor-secret") {
		t.Fatal("credential leaked in provider diagnostic", err)
	}
	t.Setenv("IMAGEN_FIXTURE_ACTOR", "")
	if _, err := Credential(p); err == nil {
		t.Fatal("missing actor credential accepted")
	}
}
