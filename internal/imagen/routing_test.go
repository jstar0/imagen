package imagen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func routeConfig(t *testing.T) Config {
	t.Helper()
	t.Setenv("IMAGEN_ROUTING_KEY", "routing-secret-value")
	p := Profile{Kind: "gpt-image", BaseURL: "https://example.test/v1", APIKeyEnv: "IMAGEN_ROUTING_KEY", TimeoutSeconds: 60, SupportedModels: []string{"gpt-image-2"}, Capabilities: []string{"generate", "edit"}}
	q := p
	p.Priority = 1
	q.Priority = 2
	return Config{Version: 2, DefaultModel: "gpt-image", Concurrency: 2, Aliases: map[string]string{"gpt-image": "gpt-image-2"}, Providers: map[string]Profile{"duojie": p, "wecode": q}}
}
func TestRoutesExactModelsCapabilitiesAndExplicit(t *testing.T) {
	c := routeConfig(t)
	s := Store{Root: t.TempDir()}
	routes, err := s.Routes(c, "", "", Request{})
	if err != nil || len(routes) != 2 || routes[0].Name != "duojie" || routes[0].Model != "gpt-image-2" {
		t.Fatalf("routes=%+v error=%v", routes, err)
	}
	if _, err = s.Routes(c, "gpt-image-2.5", "", Request{}); err == nil {
		t.Fatal("unknown model accepted")
	}
	q := c.Providers["wecode"]
	q.Capabilities = []string{"generate"}
	c.Providers["wecode"] = q
	if _, err = s.Routes(c, "", "wecode", Request{References: []string{"image.png"}}); err == nil {
		t.Fatal("explicit provider silently switched")
	}
	routes, err = s.Routes(c, "", "", Request{References: []string{"image.png"}})
	if err != nil || len(routes) != 1 || routes[0].Name != "duojie" {
		t.Fatalf("edit routes=%v err=%v", routes, err)
	}
	for _, r := range []Request{{Stream: true}, {Count: 2}, {Mask: "mask.png", References: []string{"image.png"}}} {
		if _, err = s.Routes(c, "", "", r); err == nil {
			t.Fatalf("unsupported capability accepted: %+v", r)
		}
	}
	q.Capabilities = append(q.Capabilities, "stream")
	c.Providers["wecode"] = q
	routes, err = s.Routes(c, "", "", Request{Stream: true})
	if err != nil || len(routes) != 1 || routes[0].Name != "wecode" {
		t.Fatalf("stream routes=%v err=%v", routes, err)
	}
}
func TestRoutesHealthAndCredentialChanges(t *testing.T) {
	c := routeConfig(t)
	s := Store{Root: t.TempDir()}
	r := Request{}
	routes, _ := s.Routes(c, "", "", r)
	p := routes[1]
	if err := s.RecordProvider(p, r, true, 0); err != nil {
		t.Fatal(err)
	}
	routes, _ = s.Routes(c, "", "", r)
	if routes[0].Name != "wecode" {
		t.Fatal("recent success not preferred")
	}
	if err := s.RecordProvider(p, r, false, time.Hour); err != nil {
		t.Fatal(err)
	}
	routes, _ = s.Routes(c, "", "", r)
	if len(routes) != 1 || routes[0].Name != "duojie" {
		t.Fatal("cooldown not applied")
	}
	routes, err := s.Routes(c, "", "wecode", r)
	if err != nil || len(routes) != 1 {
		t.Fatalf("explicit retry must remain possible: %v", err)
	}
	other := c.Providers["duojie"]
	other.Name = "duojie"
	other.Model = "gpt-image-2"
	if err = s.RecordProvider(other, r, false, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Routes(c, "", "", r); err == nil || !strings.Contains(err.Error(), "cooling down") {
		t.Fatalf("all cooling error: %v", err)
	}
	// Operation health is separate: generation failure cannot disable editing.
	if routes, err = s.Routes(c, "", "", Request{References: []string{"x"}}); err != nil || len(routes) != 2 {
		t.Fatalf("operation health leaked: %v", err)
	}
	t.Setenv("IMAGEN_ROUTING_KEY", "rotated-secret-value")
	if routes, err = s.Routes(c, "", "", r); err != nil || len(routes) != 2 {
		t.Fatalf("credential rotation did not clear cooldown: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(s.Root, "health.json"))
	if strings.Contains(string(b), "routing-secret-value") || strings.Contains(string(b), "rotated-secret-value") {
		t.Fatal("secret persisted")
	}
	infos, err := s.ProviderStatus(c)
	if err != nil || len(infos) != 2 || !infos[0].Ready || len(infos[0].Health) != 0 {
		t.Fatalf("status=%+v err=%v", infos, err)
	}
}
func TestRoutingConfigVersionsAndAliases(t *testing.T) {
	c := routeConfig(t)
	path := filepath.Join(t.TempDir(), "config.json")
	if err := atomicJSON(path, c); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(path)
	if err != nil || loaded.Providers["duojie"].Name != "duojie" {
		t.Fatalf("v2 load=%+v %v", loaded, err)
	}
	p := c.Providers["duojie"]
	p.Model = "gpt-image-2"
	legacy := Config{Version: 1, DefaultModel: "gpt-image", Concurrency: 1, Models: map[string]Profile{"gpt-image": p}}
	if err = atomicJSON(path, legacy); err != nil {
		t.Fatal(err)
	}
	loaded, err = LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	routes, err := (Store{Root: t.TempDir()}).Routes(loaded, "", "", Request{})
	if err != nil || len(routes) != 1 || routes[0].Model != "gpt-image-2" {
		t.Fatalf("legacy routes %v %v", routes, err)
	}
	c.Aliases["gpt-image-2"] = "gpt-image"
	if _, err = ResolveModel(c, ""); err == nil {
		t.Fatal("alias cycle accepted")
	}
}
func TestProviderStatusUnavailableCredentialDoesNotLeak(t *testing.T) {
	c := routeConfig(t)
	t.Setenv("IMAGEN_ROUTING_KEY", "")
	s := Store{Root: t.TempDir()}
	infos, err := s.ProviderStatus(c)
	if err != nil || len(infos) != 2 || infos[0].Ready {
		t.Fatalf("status=%v err=%v", infos, err)
	}
	if _, err = s.Routes(c, "", "", Request{}); err == nil || !strings.Contains(err.Error(), "credential unavailable") {
		t.Fatalf("route error=%v", err)
	}
	b, _ := json.Marshal(infos)
	if strings.Contains(string(b), "IMAGEN_ROUTING_KEY") {
		t.Fatal("credential reference unnecessarily exposed")
	}
}
