package imagen

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

func expandPath(s string) string {
	if strings.HasPrefix(s, "~/") {
		h, _ := os.UserHomeDir()
		return filepath.Join(h, s[2:])
	}
	return s
}

func ConfigPath() string {
	if p := os.Getenv("IMAGEN_CONFIG"); p != "" {
		return expandPath(p)
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".config", "imagen", "config.json")
}

func StateRoot() string {
	if p := os.Getenv("IMAGEN_HOME"); p != "" {
		a, _ := filepath.Abs(expandPath(p))
		return a
	}
	if p := os.Getenv("XDG_STATE_HOME"); p != "" {
		return filepath.Join(p, "imagen")
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".local", "state", "imagen")
}

func LoadConfig(path string) (Config, error) {
	var c Config
	f, err := os.Open(expandPath(path))
	if err != nil {
		return c, fmt.Errorf("read config: %w", err)
	}
	defer f.Close()
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	if err = d.Decode(&c); err != nil {
		return c, fmt.Errorf("decode config: %w", err)
	}
	if d.Decode(new(any)) != io.EOF {
		return c, fmt.Errorf("config must contain exactly one JSON object")
	}
	if c.Version != 1 && c.Version != 2 {
		return c, fmt.Errorf("config version must be 1 or 2")
	}
	if c.Concurrency < 1 || c.Concurrency > 16 {
		return c, fmt.Errorf("concurrency must be 1..16")
	}
	profiles := c.Providers
	if c.Version == 1 {
		profiles = c.Models
	}
	if len(profiles) == 0 {
		return c, fmt.Errorf("no providers configured")
	}
	for name, p := range profiles {
		if !oneOf(p.EditEncoding, "", "multipart", "json") {
			return c, fmt.Errorf("provider %s: unknown edit_encoding", name)
		}
		p.Name = name
		if p.Kind != "gpt-image" && p.Kind != "grok" {
			return c, fmt.Errorf("model %s: unsupported kind", name)
		}
		if (c.Version == 1 && p.Model == "") || p.BaseURL == "" || (p.APIKeyEnv == "" && p.APIKeyFile == "") {
			return c, fmt.Errorf("model %s: model, base_url and credential reference required", name)
		}
		if p.TimeoutSeconds < 1 || p.TimeoutSeconds > 3600 {
			return c, fmt.Errorf("model %s: timeout_seconds must be 1..3600", name)
		}
		u, err := url.Parse(p.BaseURL)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return c, fmt.Errorf("provider %s: base_url must be an HTTP(S) URL without credentials, query or fragment", name)
		}
		if c.Version == 2 && len(p.SupportedModels) == 0 {
			return c, fmt.Errorf("provider %s: supported_models required", name)
		}
		for _, m := range p.SupportedModels {
			if strings.TrimSpace(m) == "" || strings.Contains(m, "*") {
				return c, fmt.Errorf("provider %s: supported_models must be exact model names", name)
			}
		}
		for k := range p.Headers {
			if strings.EqualFold(k, "Authorization") || strings.EqualFold(k, "x-api-key") || strings.EqualFold(k, "api-key") {
				return c, fmt.Errorf("provider %s: authentication headers must use credential references", name)
			}
		}
		for _, cap := range p.Capabilities {
			switch cap {
			case "generate", "edit", "mask", "stream", "batch", "compression", "background", "input_fidelity", "moderation":
			default:
				return c, fmt.Errorf("provider %s: unknown capability %s", name, cap)
			}
		}
		profiles[name] = p
	}
	if _, err := ResolveModel(c, ""); err != nil {
		return c, err
	}
	for alias := range c.Aliases {
		if _, err := ResolveModel(c, alias); err != nil {
			return c, err
		}
	}
	return c, nil
}

// Credential reads the explicit source first. It never executes shell code or
// silently selects an unrelated credential inherited from another client.
func Credential(p Profile) (string, error) {
	var key string
	if p.APIKeyFile != "" {
		b, err := readPrivate(expandPath(p.APIKeyFile))
		if err != nil {
			return "", err
		}
		key = strings.TrimSpace(string(b))
	} else if p.EnvFile != "" {
		b, err := readPrivate(expandPath(p.EnvFile))
		if err != nil {
			return "", err
		}
		s := bufio.NewScanner(strings.NewReader(string(b)))
		for s.Scan() {
			line := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s.Text()), "export "))
			k, v, ok := strings.Cut(line, "=")
			if !ok || strings.TrimSpace(k) != p.APIKeyEnv {
				continue
			}
			v = strings.TrimSpace(v)
			if len(v) >= 2 && (v[0] == '\'' && v[len(v)-1] == '\'' || v[0] == '"' && v[len(v)-1] == '"') {
				v = v[1 : len(v)-1]
			}
			key = v
		}
		if err := s.Err(); err != nil {
			return "", err
		}
	} else {
		key = os.Getenv(p.APIKeyEnv)
	}
	if key == "" {
		return "", fmt.Errorf("credential unavailable for %s (check configured credential source)", p.Model)
	}
	if strings.ContainsAny(key, "\r\n") {
		return "", fmt.Errorf("credential contains newline")
	}
	return key, nil
}

func readPrivate(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("credential source unavailable: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("credential source must be an owner-only regular file: %s", path)
	}
	return os.ReadFile(path)
}
