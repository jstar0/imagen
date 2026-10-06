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

func TestResponseDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name, body, kind, message string
		code                      int
	}{
		{"html", "<html>secret https://private.example/token</html>", "markup", "not an Images API JSON", 200},
		{"empty_data", `{"data":[],"secret":"secret"}`, "json", "no entries", 200},
		{"gateway", `{"error":{"message":"secret at https://private.example/token"}}`, "json", "HTTP 504", 504},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Wecode-Request-Id", "request-123")
				w.WriteHeader(tc.code)
				w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			result, err := Generate(context.Background(), Profile{Kind: "gpt-image", Model: "test", BaseURL: srv.URL}, "secret", Request{Prompt: "draw"}, filepath.Join(t.TempDir(), "image"))
			if err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("error: %v", err)
			}
			if result.Response == nil || result.Response.HTTPStatus != tc.code || result.Response.BodyKind != tc.kind || result.RequestID != "request-123" {
				t.Fatalf("diagnostics: %+v", result)
			}
			raw, _ := json.Marshal(result)
			if strings.Contains(string(raw)+err.Error(), "secret") || strings.Contains(string(raw)+err.Error(), "private.example") {
				t.Fatal("secret leaked")
			}
		})
	}
}
