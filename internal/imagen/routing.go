package imagen

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func configuredProviders(c Config) map[string]Profile {
	if c.Version == 1 {
		return c.Models
	}
	return c.Providers
}

func ResolveModel(c Config, name string) (string, error) {
	if name == "" {
		name = c.DefaultModel
	}
	seen := map[string]bool{}
	for {
		if seen[name] {
			return "", fmt.Errorf("model alias cycle at %s", name)
		}
		seen[name] = true
		target, ok := c.Aliases[name]
		if !ok {
			break
		}
		name = target
	}
	if c.Version == 1 {
		if p, ok := c.Models[name]; ok {
			return p.Model, nil
		}
	}
	for _, p := range configuredProviders(c) {
		if supportsModel(p, name, c.Version) {
			return name, nil
		}
	}
	return "", fmt.Errorf("model %q is not configured; no model substitution is allowed", name)
}

func supportsModel(p Profile, model string, version int) bool {
	if version == 1 {
		return p.Model == model
	}
	for _, m := range p.SupportedModels {
		if m == model {
			return true
		}
	}
	return false
}
func operation(r Request) string {
	if len(r.References) > 0 {
		return "edit"
	}
	return "generate"
}
func missingCapability(p Profile, r Request) string {
	caps := p.Capabilities
	if len(caps) == 0 {
		caps = []string{"generate", "edit"}
	}
	required := []string{operation(r)}
	if r.Mask != "" {
		required = append(required, "mask")
	}
	if r.Stream {
		required = append(required, "stream")
	}
	if r.Count > 1 {
		required = append(required, "batch")
	}
	if r.Compression != nil {
		required = append(required, "compression")
	}
	if r.Background != "" {
		required = append(required, "background")
	}
	if r.InputFidelity != "" {
		required = append(required, "input_fidelity")
	}
	if r.Moderation != "" {
		required = append(required, "moderation")
	}
	for _, need := range required {
		found := false
		for _, have := range caps {
			if need == have {
				found = true
				break
			}
		}
		if !found {
			return need
		}
	}
	return ""
}

type providerHealth struct {
	Fingerprint   string    `json:"credential_fingerprint"`
	LastSuccess   time.Time `json:"last_success,omitempty"`
	CooldownUntil time.Time `json:"cooldown_until,omitempty"`
}

func healthKey(p Profile, r Request) string { return p.Name + "/" + p.Model + "/" + operation(r) }
func fingerprint(p Profile, key string) string {
	b := sha256.Sum256([]byte(p.BaseURL + "\x00" + p.Model + "\x00" + key))
	return hex.EncodeToString(b[:])
}

func (s Store) readHealth() (map[string]providerHealth, error) {
	result := map[string]providerHealth{}
	b, err := os.ReadFile(filepath.Join(s.Root, "health.json"))
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(b, &result); err != nil {
		return nil, fmt.Errorf("read provider health: %w", err)
	}
	if result == nil {
		result = map[string]providerHealth{}
	}
	return result, nil
}

// Routes is observational: no API probes, retries or credential writes.
func (s Store) Routes(c Config, modelName, providerName string, r Request) ([]Profile, error) {
	model, err := ResolveModel(c, modelName)
	if err != nil {
		return nil, err
	}
	providers := configuredProviders(c)
	if providerName != "" {
		if _, ok := providers[providerName]; !ok {
			return nil, fmt.Errorf("provider %q is not configured", providerName)
		}
	}
	health, err := s.readHealth()
	if err != nil {
		return nil, err
	}
	type candidate struct {
		p Profile
		h providerHealth
	}
	candidates := []candidate{}
	reasons := []string{}
	for name, p := range providers {
		if providerName != "" && name != providerName {
			continue
		}
		p.Name = name
		if !supportsModel(p, model, c.Version) {
			reasons = append(reasons, name+": model unsupported")
			continue
		}
		p.Model = model
		if cap := missingCapability(p, r); cap != "" {
			reasons = append(reasons, name+": "+cap+" unsupported")
			continue
		}
		key, e := Credential(p)
		if e != nil {
			reasons = append(reasons, name+": credential unavailable")
			continue
		}
		h := health[healthKey(p, r)]
		if h.Fingerprint != fingerprint(p, key) {
			h = providerHealth{}
		}
		if h.CooldownUntil.After(time.Now()) && providerName == "" {
			reasons = append(reasons, name+": cooling down until "+h.CooldownUntil.UTC().Format(time.RFC3339))
			continue
		}
		candidates = append(candidates, candidate{p, h})
	}
	if len(candidates) == 0 {
		sort.Strings(reasons)
		return nil, fmt.Errorf("no usable provider for %s %s (%s)", model, operation(r), strings.Join(reasons, "; "))
	}
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if !a.h.LastSuccess.Equal(b.h.LastSuccess) {
			return a.h.LastSuccess.After(b.h.LastSuccess)
		}
		if a.p.Priority != b.p.Priority {
			return a.p.Priority < b.p.Priority
		}
		return a.p.Name < b.p.Name
	})
	result := make([]Profile, len(candidates))
	for i, p := range candidates {
		result[i] = p.p
	}
	return result, nil
}

// RecordProvider records only local observations, scoped to provider/model/operation.
func (s Store) RecordProvider(p Profile, r Request, success bool, cooldown time.Duration) error {
	key, err := Credential(p)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(s.Root, 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.Root, "health.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	f.Close()
	lock, err := lockFile(filepath.Join(s.Root, "health.lock"), false)
	if err != nil {
		return err
	}
	defer unlock(lock)
	health, err := s.readHealth()
	if err != nil {
		return err
	}
	id := healthKey(p, r)
	h := health[id]
	fp := fingerprint(p, key)
	if h.Fingerprint != fp {
		h = providerHealth{Fingerprint: fp}
	}
	if success {
		h.LastSuccess = time.Now().UTC()
		h.CooldownUntil = time.Time{}
	} else {
		h.CooldownUntil = time.Now().UTC().Add(cooldown)
	}
	health[id] = h
	return atomicJSON(filepath.Join(s.Root, "health.json"), health)
}

type ProviderInfo struct {
	Name         string                        `json:"name"`
	Kind         string                        `json:"kind"`
	Models       []string                      `json:"models"`
	Capabilities []string                      `json:"capabilities"`
	Priority     int                           `json:"priority"`
	Ready        bool                          `json:"credential_ready"`
	Notes        []string                      `json:"notes,omitempty"`
	Health       map[string]ProviderHealthInfo `json:"health,omitempty"`
}
type ProviderHealthInfo struct {
	LastSuccess   time.Time `json:"last_success,omitempty"`
	CooldownUntil time.Time `json:"cooldown_until,omitempty"`
}

func (s Store) ProviderStatus(c Config) ([]ProviderInfo, error) {
	health, err := s.readHealth()
	if err != nil {
		return nil, err
	}
	out := []ProviderInfo{}
	for name, p := range configuredProviders(c) {
		p.Name = name
		key, e := Credential(p)
		models := p.SupportedModels
		if c.Version == 1 {
			models = []string{p.Model}
		}
		caps := p.Capabilities
		if len(caps) == 0 {
			caps = []string{"generate", "edit"}
		}
		info := ProviderInfo{Name: name, Kind: p.Kind, Models: models, Capabilities: caps, Priority: p.Priority, Ready: e == nil, Notes: p.Notes, Health: map[string]ProviderHealthInfo{}}
		for _, model := range models {
			p.Model = model
			for _, r := range []Request{{}, {References: []string{"reference"}}} {
				h := health[healthKey(p, r)]
				if e == nil && h.Fingerprint == fingerprint(p, key) {
					info.Health[model+"/"+operation(r)] = ProviderHealthInfo{h.LastSuccess, h.CooldownUntil}
				}
			}
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
