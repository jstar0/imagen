package imagen

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Responses generation keeps the requested image model separate from the
// mainline orchestrator model. Edits retain the provider's Images API encoding.
func responsesBody(p Profile, r Request) ([]byte, error) {
	tool := map[string]any{"type": "image_generation", "model": p.Model, "action": "generate"}
	for k, v := range map[string]string{"size": r.Size, "quality": r.Quality, "output_format": r.Format, "background": r.Background} {
		if v != "" && !(k == "output_format" && v == "auto") {
			tool[k] = v
		}
	}
	if r.Compression != nil {
		tool["output_compression"] = *r.Compression
	}
	if r.PartialImages > 0 {
		tool["partial_images"] = r.PartialImages
	}
	prompt := r.Prompt
	if r.NegativePrompt != "" {
		prompt += "\n\nAvoid: " + r.NegativePrompt
	}
	return json.Marshal(map[string]any{"model": p.ResponsesModel, "instructions": "Generate the requested image. Preserve the user's specified labels and scientific relationships.", "input": prompt, "tools": []any{tool}, "tool_choice": map[string]string{"type": "image_generation"}, "stream": true, "store": false})
}

type responseImageItem struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Status string `json:"status"`
	Result string `json:"result"`
	Model  string `json:"model"`
}

func consumeResponsesStream(reader io.Reader, r Request, stem string, result Result, progress func(Result) error) (Result, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), maxImageBytes)
	data := []string{}
	event := ""
	completed := false
	seen := map[string]bool{}
	save := func(item responseImageItem) error {
		if item.Type != "image_generation_call" || item.Status != "completed" {
			return nil
		}
		if seen[item.ID] && item.ID != "" {
			return nil
		}
		if len(result.Images) >= imageCount(r) {
			return providerFailure(true, "Responses returned more images than requested; saved results retained")
		}
		bytes, err := base64.StdEncoding.DecodeString(item.Result)
		if err != nil || len(bytes) == 0 || len(bytes) > maxImageBytes {
			return providerFailure(true, "Responses completed image has invalid bytes")
		}
		file, err := saveImage(bytes, imageStem(stem, len(result.Images), imageCount(r)), r.Overwrite)
		if err != nil {
			return err
		}
		result.Images = append(result.Images, file)
		setFirstImage(&result)
		seen[item.ID] = true
		if item.Model != "" {
			result.ReturnedModel = item.Model
		}
		if progress != nil {
			return progress(result)
		}
		return nil
	}
	dispatch := func() error {
		raw := strings.TrimSpace(strings.Join(data, "\n"))
		data = nil
		if raw == "" || raw == "[DONE]" {
			return nil
		}
		var v struct {
			Type     string            `json:"type"`
			Item     responseImageItem `json:"item"`
			Response struct {
				Status string              `json:"status"`
				Output []responseImageItem `json:"output"`
				Usage  json.RawMessage     `json:"usage"`
			} `json:"response"`
			Partial string `json:"partial_image_b64"`
		}
		if json.Unmarshal([]byte(raw), &v) != nil {
			return providerFailure(true, "invalid Responses stream JSON; remote outcome unknown")
		}
		if v.Type == "" {
			v.Type = event
		}
		switch v.Type {
		case "error", "response.failed", "response.incomplete":
			return &ProviderError{Unknown: true, Message: "Responses stream failed or incomplete; remote outcome unknown", RetryableFailure: terminalChannelFailure(200, []byte(raw))}
		case "response.output_item.done":
			return save(v.Item)
		case "response.image_generation_call.partial_image":
			if r.PartialImages == 0 {
				return nil
			}
			if len(result.Previews) >= 3 {
				return providerFailure(true, "too many Responses previews")
			}
			b, err := base64.StdEncoding.DecodeString(v.Partial)
			if err != nil {
				return providerFailure(true, "invalid Responses preview")
			}
			file, err := saveImage(b, fmt.Sprintf("%s-preview-%d", stem, len(result.Previews)+1), r.Overwrite)
			if err != nil {
				return err
			}
			result.Previews = append(result.Previews, file)
			if progress != nil {
				return progress(result)
			}
		case "response.completed":
			if v.Response.Status != "completed" {
				return providerFailure(true, "Responses completion has non-completed status")
			}
			for _, item := range v.Response.Output {
				if err := save(item); err != nil {
					return err
				}
			}
			result.Usage = v.Response.Usage
			completed = true
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := dispatch(); err != nil {
				return result, err
			}
			event = ""
			if completed {
				break
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		}
	}
	if scanner.Err() != nil {
		return result, providerFailure(true, "Responses stream interrupted; remote outcome unknown")
	}
	if !completed {
		if err := dispatch(); err != nil {
			return result, err
		}
	}
	if !completed || len(result.Images) == 0 {
		return result, providerFailure(true, "Responses ended without a completed image; remote outcome unknown")
	}
	addResultWarnings(&result, r)
	return result, nil
}
