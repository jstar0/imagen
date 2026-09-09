package imagen

import "encoding/json"

type Config struct {
	Version      int                `json:"version"`
	DefaultModel string             `json:"default_model"`
	Concurrency  int                `json:"concurrency"`
	Models       map[string]Profile `json:"models"`
	Providers    map[string]Profile `json:"providers,omitempty"`
	Aliases      map[string]string  `json:"aliases,omitempty"`
}

// Profile contains credential references, never credentials.
type Profile struct {
	Name                string            `json:"name,omitempty"`
	SupportedModels     []string          `json:"supported_models,omitempty"`
	Capabilities        []string          `json:"capabilities,omitempty"`
	Priority            int               `json:"priority,omitempty"`
	Headers             map[string]string `json:"headers,omitempty"`
	Notes               []string          `json:"notes,omitempty"`
	EditEncoding        string            `json:"edit_encoding,omitempty"`
	SingleImageRequests bool              `json:"single_image_requests,omitempty"`
	Kind                string            `json:"kind"`
	Model               string            `json:"model"`
	BaseURL             string            `json:"base_url"`
	APIKeyEnv           string            `json:"api_key_env"`
	APIKeyFile          string            `json:"api_key_file,omitempty"`
	EnvFile             string            `json:"env_file,omitempty"`
	TimeoutSeconds      int               `json:"timeout_seconds"`
}

type Request struct {
	Count          int      `json:"count,omitempty"`
	Mask           string   `json:"mask,omitempty"`
	Compression    *int     `json:"compression,omitempty"`
	Background     string   `json:"background,omitempty"`
	InputFidelity  string   `json:"input_fidelity,omitempty"`
	Moderation     string   `json:"moderation,omitempty"`
	Stream         bool     `json:"stream,omitempty"`
	PartialImages  int      `json:"partial_images,omitempty"`
	NegativePrompt string   `json:"negative_prompt,omitempty"`
	Name           string   `json:"name,omitempty"`
	Overwrite      bool     `json:"overwrite,omitempty"`
	Notify         bool     `json:"notify,omitempty"`
	Prompt         string   `json:"prompt"`
	References     []string `json:"references,omitempty"`
	Size           string   `json:"size,omitempty"`
	AspectRatio    string   `json:"aspect_ratio,omitempty"`
	Resolution     string   `json:"resolution,omitempty"`
	Quality        string   `json:"quality,omitempty"`
	Format         string   `json:"format,omitempty"`
	Output         string   `json:"output"`
}

type Result struct {
	Images        []ImageFile       `json:"images,omitempty"`
	Previews      []ImageFile       `json:"previews,omitempty"`
	ReturnedModel string            `json:"returned_model,omitempty"`
	Path          string            `json:"path"`
	Format        string            `json:"format"`
	Width         int               `json:"width"`
	Height        int               `json:"height"`
	RequestID     string            `json:"request_id,omitempty"`
	Usage         json.RawMessage   `json:"usage,omitempty"`
	RequestIDs    []string          `json:"request_ids,omitempty"`
	Usages        []json.RawMessage `json:"usages,omitempty"`
	RequestedSize string            `json:"requested_size,omitempty"`
	Warnings      []string          `json:"warnings,omitempty"`
}

type ImageFile struct {
	Path   string `json:"path"`
	Format string `json:"format"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

type Attempt struct {
	Provider  string `json:"provider"`
	StartedAt string `json:"started_at"`
	Error     string `json:"error,omitempty"`
	Status    string `json:"status"`
}

type Job struct {
	Candidates      []Profile `json:"candidates,omitempty"`
	Attempts        []Attempt `json:"attempts,omitempty"`
	ID              string    `json:"id"`
	Status          string    `json:"status"`
	Alias           string    `json:"alias"`
	Profile         Profile   `json:"profile"`
	Request         Request   `json:"request"`
	CreatedAt       string    `json:"created_at"`
	UpdatedAt       string    `json:"updated_at"`
	PID             int       `json:"pid,omitempty"`
	WorkerToken     string    `json:"worker_token,omitempty"`
	Concurrency     int       `json:"concurrency"`
	CancelRequested bool      `json:"cancel_requested,omitempty"`
	Result          *Result   `json:"result,omitempty"`
	Error           string    `json:"error,omitempty"`
}
