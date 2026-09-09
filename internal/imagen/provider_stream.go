package imagen

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type imagePayload struct {
	Data []struct {
		B64 string `json:"b64_json"`
		URL string `json:"url"`
	} `json:"data"`
	Usage json.RawMessage `json:"usage"`
	Model string          `json:"model"`
}

func imageCount(r Request) int {
	if r.Count == 0 {
		return 1
	}
	return r.Count
}
func imageStem(stem string, i, n int) string {
	if n == 1 {
		return stem
	}
	return fmt.Sprintf("%s-%d", stem, i+1)
}
func setFirstImage(r *Result) {
	if len(r.Images) > 0 {
		f := r.Images[0]
		r.Path = f.Path
		r.Width = f.Width
		r.Height = f.Height
		r.Format = f.Format
	}
}
func addResultWarnings(result *Result, r Request) {
	if len(result.Images) != imageCount(r) {
		result.Warnings = append(result.Warnings, fmt.Sprintf("requested %d images, received %d", imageCount(r), len(result.Images)))
	}
	for _, f := range result.Images {
		if r.Format != "" && r.Format != "auto" && r.Format != f.Format {
			result.Warnings = append(result.Warnings, fmt.Sprintf("requested %s, received %s; original encoding preserved", r.Format, f.Format))
		}
	}
}
func saveImage(data []byte, stem string, overwrite bool) (ImageFile, error) {
	var out ImageFile
	format, w, h, e := inspectImageBytes(data)
	if e != nil {
		return out, providerFailure(true, "provider returned invalid or unsupported image bytes")
	}
	path := stem + "." + format
	if overwrite {
		f, err := os.CreateTemp(filepath.Dir(path), ".imagen-*")
		if err != nil {
			return out, providerFailure(true, "cannot create output file")
		}
		tmp := f.Name()
		defer os.Remove(tmp)
		_, e = f.Write(data)
		if e == nil {
			e = f.Sync()
		}
		closeErr := f.Close()
		if e == nil {
			e = closeErr
		}
		if e == nil {
			e = os.Rename(tmp, path)
		}
	} else {
		e = publishImage(path, data)
	}
	if e != nil {
		return out, providerFailure(true, "image received but output could not be saved (existing file or filesystem error)")
	}
	return ImageFile{Path: path, Format: format, Width: w, Height: h}, nil
}
func validateMask(mask, reference string) error {
	data, format, e := readReference(mask)
	if e != nil {
		return e
	}
	if len(data) >= 4<<20 {
		return fmt.Errorf("mask must be smaller than 4 MiB")
	}
	if format != "png" {
		return fmt.Errorf("mask must be PNG with an alpha channel")
	}
	// PNG IHDR color types 4 and 6 have alpha; indexed PNG uses tRNS.
	hasAlpha := len(data) > 25 && (data[25] == 4 || data[25] == 6)
	for off := 8; off+12 <= len(data); {
		n := int(data[off])<<24 | int(data[off+1])<<16 | int(data[off+2])<<8 | int(data[off+3])
		if n < 0 || off+n+12 > len(data) {
			break
		}
		if string(data[off+4:off+8]) == "tRNS" {
			hasAlpha = true
		}
		off += n + 12
	}
	if !hasAlpha {
		return fmt.Errorf("mask PNG must contain an alpha channel")
	}
	ref, _, e := readReference(reference)
	if e != nil {
		return e
	}
	mc, _, e := image.DecodeConfig(bytes.NewReader(data))
	if e != nil {
		return e
	}
	rc, _, e := image.DecodeConfig(bytes.NewReader(ref))
	if e != nil {
		return e
	}
	if mc.Width != rc.Width || mc.Height != rc.Height {
		return fmt.Errorf("mask dimensions must equal first reference dimensions")
	}
	return nil
}
func safeRejection(status int, body []byte) bool {
	if status >= 500 {
		return false
	}
	text := strings.ToLower(string(body))
	for _, word := range []string{"moderation", "safety", "content_policy", "content policy", "cyber", "blocked_prompt"} {
		if strings.Contains(text, word) {
			return false
		}
	}
	if status == 401 || status == 429 {
		return true
	}
	if status != 400 && status != 403 && status != 404 {
		return false
	}
	for _, code := range []string{"insufficient_quota", "spend_limit_exceeded", "invalid_api_key", "model_not_found", "unsupported_model", "model_not_supported", "unknown model", "unsupported model", "model is not supported"} {
		if strings.Contains(text, code) {
			return true
		}
	}
	return false
}

// consumeImageStream requires a completed event, never treating a preview or
// an EOF as completion. It consumes Images API events, not Responses events.
func consumeImageStream(reader io.Reader, r Request, stem string, result Result, progress func(Result) error) (Result, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), maxImageBytes)
	var event string
	var data []string
	completed := false
	dispatch := func() error {
		if len(data) == 0 {
			return nil
		}
		raw := strings.TrimSpace(strings.Join(data, "\n"))
		data = nil
		if raw == "" || raw == "[DONE]" || event == "keepalive" || event == "ping" {
			return nil
		}
		var v struct {
			Type  string          `json:"type"`
			B64   string          `json:"b64_json"`
			Index int             `json:"partial_image_index"`
			Usage json.RawMessage `json:"usage"`
			Model string          `json:"model"`
		}
		if e := json.Unmarshal([]byte(raw), &v); e != nil {
			return providerFailure(true, "malformed image stream event; remote outcome unknown: "+e.Error())
		}
		kind := v.Type
		if kind == "" {
			kind = event
		}
		partial := strings.HasSuffix(kind, ".partial_image")
		final := strings.HasSuffix(kind, ".completed")
		if strings.Contains(kind, "error") || strings.HasSuffix(kind, ".failed") {
			return providerFailure(true, "image stream failed; remote outcome unknown")
		}
		if !partial && !final {
			return nil
		}
		if partial && len(result.Previews) >= 3 {
			return providerFailure(true, "image stream exceeded supported preview count")
		}
		if completed {
			return providerFailure(true, "image stream emitted output after completion")
		}
		if v.B64 == "" {
			return providerFailure(true, "image stream output missing image bytes")
		}
		decoded, e := base64.StdEncoding.DecodeString(v.B64)
		if e != nil || len(decoded) > maxImageBytes {
			return providerFailure(true, "invalid image stream bytes")
		}
		target := stem
		if partial {
			target = fmt.Sprintf("%s-preview-%d", stem, len(result.Previews)+1)
		}
		file, e := saveImage(decoded, target, r.Overwrite)
		if e != nil {
			return e
		}
		if partial {
			result.Previews = append(result.Previews, file)
		} else {
			result.Images = append(result.Images, file)
			setFirstImage(&result)
			result.Usage = v.Usage
			result.ReturnedModel = v.Model
			completed = true
		}
		if progress != nil {
			if e := progress(result); e != nil {
				return providerFailure(true, "image progress could not be persisted")
			}
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if e := dispatch(); e != nil {
				return result, e
			}
			if completed {
				addResultWarnings(&result, r)
				return result, nil
			}
			event = ""
			continue
		}
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if e := scanner.Err(); e != nil {
		return result, providerFailure(true, "image stream interrupted; remote outcome unknown")
	}
	if e := dispatch(); e != nil {
		return result, e
	}
	if !completed {
		return result, providerFailure(true, "image stream ended without completed image; remote outcome unknown")
	}
	addResultWarnings(&result, r)
	return result, nil
}

// These are the user's requested distinct images, not retries of an uncertain request.
func generateSerialImages(ctx context.Context, p Profile, key string, r Request, stem string, progress func(Result) error) (Result, error) {
	var all Result
	n := imageCount(r)
	// Fail before generating anything if a later output is already occupied.
	if !r.Overwrite {
		for i := 0; i < n; i++ {
			for _, ext := range []string{"png", "jpeg", "webp"} {
				_, e := os.Lstat(imageStem(stem, i, n) + "." + ext)
				if e == nil {
					return all, fmt.Errorf("output already exists")
				}
				if !os.IsNotExist(e) {
					return all, e
				}
			}
		}
	}
	one := r
	one.Count = 1
	for i := 0; i < n; i++ {
		if e := ctx.Err(); e != nil {
			return all, &ProviderError{Partial: len(all.Images) > 0, Message: "batch stopped before next image", Unknown: false}
		}
		result, e := GenerateWithProgress(ctx, p, key, one, imageStem(stem, i, n), nil)
		all.Images = append(all.Images, result.Images...)
		all.Previews = append(all.Previews, result.Previews...)
		all.Warnings = append(all.Warnings, result.Warnings...)
		setFirstImage(&all)
		if result.RequestID != "" {
			all.RequestIDs = append(all.RequestIDs, result.RequestID)
			all.RequestID = result.RequestID
		}
		if len(result.Usage) > 0 {
			all.Usages = append(all.Usages, result.Usage)
		}
		if result.ReturnedModel != "" {
			all.ReturnedModel = result.ReturnedModel
		}
		if progress != nil {
			if pe := progress(all); pe != nil {
				return all, providerFailure(true, "cannot save batch progress")
			}
		}
		if e != nil {
			if len(all.Images) > 0 {
				var pe *ProviderError
				if errors.As(e, &pe) {
					copy := *pe
					copy.Partial = true
					copy.SafeFallback = false
					return all, &copy
				}
				return all, &ProviderError{Partial: true, Message: e.Error()}
			}
			return all, e
		}
	}
	return all, nil
}
