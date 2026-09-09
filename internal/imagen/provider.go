package imagen

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	_ "golang.org/x/image/webp"
)

const maxImageBytes = 128 << 20

// ProviderError distinguishes definite rejection from a request whose remote
// outcome is unknown. Neither kind authorizes an automatic paid retry.
type ProviderError struct {
	Unknown           bool
	Partial           bool
	Message           string
	SafeFallback      bool
	StatusCode        int
	RetryAfterSeconds int
}

func (e *ProviderError) Error() string { return e.Message }
func providerFailure(unknown bool, message string) error {
	return &ProviderError{Unknown: unknown, Message: message}
}

func safeURL(raw string) (*url.URL, error) {
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("invalid endpoint URL (credentials, query and fragment are forbidden)")
	}
	local := u.Hostname() == "localhost"
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		local = ip.IsLoopback()
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && local) {
		return nil, fmt.Errorf("endpoint requires HTTPS (HTTP is allowed only on loopback)")
	}
	return u, nil
}

func ValidateRequest(p Profile, r Request) error {
	if r.Count < 0 || r.Count > 10 {
		return fmt.Errorf("count must be 1 through 10")
	}
	if r.PartialImages < 0 || r.PartialImages > 3 || (r.PartialImages != 0 && !r.Stream) {
		return fmt.Errorf("partial-images requires stream and must be 0 through 3")
	}
	if r.Stream && r.Count > 1 {
		return fmt.Errorf("streaming currently supports one image per task")
	}
	if r.Mask != "" {
		if len(r.References) == 0 {
			return fmt.Errorf("mask requires a reference image")
		}
		if e := validateMask(r.Mask, r.References[0]); e != nil {
			return e
		}
	}
	if _, e := safeURL(p.BaseURL); e != nil {
		return e
	}
	if strings.TrimSpace(p.Model) == "" || strings.TrimSpace(r.Prompt) == "" {
		return fmt.Errorf("model and prompt are required")
	}
	if p.Kind != "gpt-image" && p.Kind != "grok" {
		return fmt.Errorf("unsupported provider kind %q", p.Kind)
	}
	if p.Kind == "grok" {
		if r.Mask != "" || r.Compression != nil || r.Background != "" || r.InputFidelity != "" || r.Moderation != "" || r.Stream {
			return fmt.Errorf("Grok does not support requested GPT-specific image parameters")
		}
		if r.Size != "" || (r.Format != "" && r.Format != "auto") {
			return fmt.Errorf("Grok does not accept size or output format; use resolution and aspect-ratio")
		}
		if !oneOf(r.Quality, "", "auto", "low", "medium") {
			return fmt.Errorf("Grok quality must be auto, low or medium")
		}
		if !oneOf(r.Resolution, "", "1k", "2k") {
			return fmt.Errorf("Grok resolution must be 1k or 2k")
		}
		if !oneOf(r.AspectRatio, "", "auto", "1:1", "16:9", "9:16", "4:3", "3:4", "3:2", "2:3", "2:1", "1:2", "19.5:9", "9:19.5", "20:9", "9:20", "21:9", "5:2") {
			return fmt.Errorf("unsupported Grok aspect ratio")
		}
		if len(r.References) > 5 {
			return fmt.Errorf("Grok accepts at most five references")
		}
	} else {
		if r.Compression != nil && (*r.Compression < 0 || *r.Compression > 100 || !oneOf(r.Format, "jpeg", "webp")) {
			return fmt.Errorf("compression requires jpeg/webp format and a value from 0 to 100")
		}
		if !oneOf(r.Background, "", "auto", "opaque", "transparent") || (r.Background == "transparent" && r.Format == "jpeg") {
			return fmt.Errorf("invalid background or incompatible jpeg transparency")
		}
		if !oneOf(r.Moderation, "", "auto", "low") {
			return fmt.Errorf("moderation must be auto or low")
		}
		if !oneOf(r.InputFidelity, "", "low", "high") || (r.InputFidelity != "" && len(r.References) == 0) {
			return fmt.Errorf("input-fidelity requires editing and must be low or high")
		}
		if r.InputFidelity != "" && (p.Model == "gpt-image-2" || strings.HasPrefix(p.Model, "gpt-image-2-")) {
			return fmt.Errorf("GPT Image 2 fixes input fidelity; do not supply input-fidelity")
		}
		if r.AspectRatio != "" || r.Resolution != "" {
			return fmt.Errorf("GPT Image uses size, not aspect-ratio or resolution")
		}
		if !oneOf(r.Quality, "", "auto", "low", "medium", "high") && !(strings.HasPrefix(p.Model, "gpt-image-2.5-") && oneOf(r.Quality, "xhigh", "max")) {
			return fmt.Errorf("GPT Image quality must be auto, low, medium or high")
		}
		if !oneOf(r.Format, "", "auto", "png", "jpeg", "webp") {
			return fmt.Errorf("GPT Image format must be auto, png, jpeg or webp")
		}
		if len(r.References) > 16 {
			return fmt.Errorf("GPT Image accepts at most sixteen references")
		}
	}
	return nil
}
func oneOf(s string, options ...string) bool {
	for _, v := range options {
		if s == v {
			return true
		}
	}
	return false
}

func Generate(ctx context.Context, p Profile, key string, r Request, outputStem string) (Result, error) {
	return GenerateWithProgress(ctx, p, key, r, outputStem, nil)
}

func GenerateWithProgress(ctx context.Context, p Profile, key string, r Request, outputStem string, progress func(Result) error) (Result, error) {
	var result Result
	if e := ValidateRequest(p, r); e != nil {
		return result, e
	}
	if strings.TrimSpace(key) == "" {
		return result, fmt.Errorf("API key is missing")
	}
	if !filepath.IsAbs(outputStem) {
		return result, fmt.Errorf("output stem must be absolute")
	}
	if p.SingleImageRequests && imageCount(r) > 1 {
		return generateSerialImages(ctx, p, key, r, outputStem, progress)
	}
	// Check all supported encodings before spending money, then atomically refuse
	// overwrite again at publication time.
	for i := 0; i < imageCount(r); i++ {
		for _, ext := range []string{"png", "jpeg", "webp"} {
			if _, e := os.Lstat(imageStem(outputStem, i, imageCount(r)) + "." + ext); e == nil && !r.Overwrite {
				return result, fmt.Errorf("output already exists")
			} else if e != nil && !os.IsNotExist(e) {
				return result, e
			}
		}
	}
	if r.Stream && !r.Overwrite {
		for i := 1; i <= 3; i++ {
			for _, ext := range []string{"png", "jpeg", "webp"} {
				if _, e := os.Lstat(fmt.Sprintf("%s-preview-%d.%s", outputStem, i, ext)); e == nil {
					return result, fmt.Errorf("preview output already exists")
				} else if !os.IsNotExist(e) {
					return result, e
				}
			}
		}
	}
	body, ct, e := providerBody(p, r)
	if e != nil {
		return result, e
	}
	u, _ := safeURL(p.BaseURL)
	u.Path = strings.TrimRight(u.Path, "/")
	if u.Path == "" {
		u.Path = "/v1"
	}
	u.Path += "/images/generations"
	if len(r.References) > 0 {
		u.Path = strings.TrimSuffix(u.Path, "generations") + "edits"
	}
	timeout := time.Duration(p.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if e != nil {
		return result, providerFailure(false, "cannot construct image request")
	}
	for name, value := range p.Headers {
		if strings.EqualFold(name, "Authorization") || strings.EqualFold(name, "Content-Type") || strings.EqualFold(name, "Host") {
			return result, fmt.Errorf("provider extra headers cannot override authentication or protocol headers")
		}
		req.Header.Set(name, value)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Accept", "application/json")
	if r.Stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return fmt.Errorf("API redirects are disabled") }}
	resp, e := client.Do(req)
	if e != nil {
		return result, providerFailure(true, "image request transport failed; remote outcome unknown")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := fmt.Sprintf("image provider returned HTTP %d", resp.StatusCode)
		// Only extract recognized fields from a bounded JSON error, never echo
		// arbitrary response bodies, credential material or signed URLs.
		errorBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if readErr == nil {
			if detail := safeProviderDetail(errorBody, key); detail != "" {
				message += ": " + detail
			}
		}
		retry, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
		return result, &ProviderError{Unknown: resp.StatusCode >= 500, Message: message, StatusCode: resp.StatusCode, RetryAfterSeconds: retry, SafeFallback: safeRejection(resp.StatusCode, errorBody)}
	}
	result.RequestID = resp.Header.Get("x-request-id")
	if result.RequestID == "" {
		result.RequestID = resp.Header.Get("x-wecode-request-id")
	}
	if r.Stream {
		return consumeImageStream(resp.Body, r, outputStem, result, progress)
	}
	raw, e := limitedBytes(resp.Body)
	if e != nil {
		return result, providerFailure(true, "cannot read image response; remote outcome unknown")
	}
	var payload imagePayload
	if json.Unmarshal(raw, &payload) != nil || len(payload.Data) == 0 || len(payload.Data) > 10 {
		return result, providerFailure(true, "image response contains no images or exceeds supported count")
	}
	result.Usage = payload.Usage
	result.ReturnedModel = payload.Model
	for i, item := range payload.Data {
		var data []byte
		if item.B64 != "" {
			data, e = base64.StdEncoding.DecodeString(item.B64)
		} else if item.URL != "" {
			data, e = downloadImage(ctx, item.URL)
		} else {
			e = fmt.Errorf("missing image")
		}
		if e != nil || len(data) > maxImageBytes {
			return result, providerFailure(true, "cannot retrieve image bytes; remote outcome unknown")
		}
		file, e := saveImage(data, imageStem(outputStem, i, len(payload.Data)), r.Overwrite)
		if e != nil {
			return result, e
		}
		result.Images = append(result.Images, file)
		setFirstImage(&result)
	}
	addResultWarnings(&result, r)
	if len(result.Images) != imageCount(r) {
		return result, &ProviderError{Partial: true, Message: fmt.Sprintf("provider returned %d of %d requested images; saved results retained", len(result.Images), imageCount(r))}
	}
	return result, nil
}

var errorURLPattern = regexp.MustCompile(`(?i)https?://[^\s<>"']+`)

func safeProviderDetail(body []byte, key string) string {
	var payload struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return ""
	}
	var detail string
	if json.Unmarshal(payload.Error, &detail) != nil {
		var object struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		}
		if json.Unmarshal(payload.Error, &object) != nil {
			return ""
		}
		detail = object.Message
		if object.Code != "" {
			detail = object.Code + ": " + detail
		}
	}
	if key != "" {
		detail = strings.ReplaceAll(detail, key, "[REDACTED]")
	}
	detail = errorURLPattern.ReplaceAllString(detail, "[URL REDACTED]")
	detail = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return ' '
		}
		return r
	}, detail)
	detail = strings.Join(strings.Fields(detail), " ")
	runes := []rune(detail)
	if len(runes) > 500 {
		detail = string(runes[:497]) + "..."
	}
	return strings.TrimSpace(detail)
}

func providerBody(p Profile, r Request) ([]byte, string, error) {
	fields := map[string]any{"model": p.Model, "prompt": r.Prompt, "n": imageCount(r)}
	if r.NegativePrompt != "" {
		fields["prompt"] = r.Prompt + "\n\nAvoid: " + r.NegativePrompt
	}
	optional := func(k, v string) {
		if v != "" {
			fields[k] = v
		}
	}
	optional("quality", r.Quality)
	if p.Kind == "gpt-image" {
		optional("background", r.Background)
		optional("input_fidelity", r.InputFidelity)
		optional("moderation", r.Moderation)
		if r.Compression != nil {
			fields["output_compression"] = *r.Compression
		}
		if r.Stream {
			fields["stream"] = true
			fields["partial_images"] = r.PartialImages
		}
		optional("size", r.Size)
		if r.Format != "auto" {
			optional("output_format", r.Format)
		}
	} else {
		fields["response_format"] = "b64_json"
		optional("resolution", r.Resolution)
		optional("aspect_ratio", r.AspectRatio)
	}
	if len(r.References) > 0 && p.Kind == "gpt-image" {
		if p.EditEncoding == "json" {
			images := []map[string]string{}
			for _, path := range r.References {
				data, format, e := readReference(path)
				if e != nil {
					return nil, "", e
				}
				images = append(images, map[string]string{"image_url": "data:image/" + format + ";base64," + base64.StdEncoding.EncodeToString(data)})
			}
			fields["images"] = images
			if r.Mask != "" {
				data, _, e := readReference(r.Mask)
				if e != nil {
					return nil, "", e
				}
				fields["mask"] = map[string]string{"image_url": "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)}
			}
			body, e := json.Marshal(fields)
			return body, "application/json", e
		}
		var b bytes.Buffer
		mw := multipart.NewWriter(&b)
		for k, v := range fields {
			if e := mw.WriteField(k, fmt.Sprint(v)); e != nil {
				return nil, "", e
			}
		}
		for _, path := range r.References {
			data, format, e := readReference(path)
			if e != nil {
				return nil, "", e
			}
			header := make(textproto.MIMEHeader)
			header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": "image[]", "filename": filepath.Base(path)}))
			header.Set("Content-Type", "image/"+format)
			part, e := mw.CreatePart(header)
			if e != nil {
				return nil, "", e
			}
			if _, e = part.Write(data); e != nil {
				return nil, "", e
			}
		}
		if r.Mask != "" {
			data, _, e := readReference(r.Mask)
			if e != nil {
				return nil, "", e
			}
			header := make(textproto.MIMEHeader)
			header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": "mask", "filename": filepath.Base(r.Mask)}))
			header.Set("Content-Type", "image/png")
			part, e := mw.CreatePart(header)
			if e != nil {
				return nil, "", e
			}
			if _, e = part.Write(data); e != nil {
				return nil, "", e
			}
		}
		if e := mw.Close(); e != nil {
			return nil, "", e
		}
		return b.Bytes(), mw.FormDataContentType(), nil
	}
	if len(r.References) > 0 {
		images := make([]map[string]string, 0, len(r.References))
		for _, path := range r.References {
			data, format, e := readReference(path)
			if e != nil {
				return nil, "", e
			}
			images = append(images, map[string]string{"url": "data:image/" + format + ";base64," + base64.StdEncoding.EncodeToString(data), "type": "image_url"})
		}
		if len(images) == 1 {
			fields["image"] = images[0]
		} else {
			fields["images"] = images
		}
	}
	b, e := json.Marshal(fields)
	return b, "application/json", e
}
func limitedBytes(r io.Reader) ([]byte, error) {
	b, e := io.ReadAll(io.LimitReader(r, maxImageBytes+1))
	if len(b) > maxImageBytes {
		return nil, fmt.Errorf("body exceeds 128 MiB")
	}
	return b, e
}
func readReference(path string) ([]byte, string, error) {
	stat, e := os.Stat(path)
	if e != nil || !stat.Mode().IsRegular() || stat.Size() > maxImageBytes {
		return nil, "", fmt.Errorf("reference must be a regular image file under 128 MiB")
	}
	f, e := os.Open(path)
	if e != nil {
		return nil, "", fmt.Errorf("cannot open reference image")
	}
	defer f.Close()
	b, e := limitedBytes(f)
	if e != nil {
		return nil, "", fmt.Errorf("cannot read reference image")
	}
	format, _, _, e := inspectImageBytes(b)
	if e != nil {
		return nil, "", fmt.Errorf("reference must be valid PNG, JPEG or WebP")
	}
	return b, format, nil
}
func inspectImageBytes(b []byte) (string, int, int, error) {
	c, f, e := image.DecodeConfig(bytes.NewReader(b))
	if e != nil {
		return "", 0, 0, e
	}
	if !oneOf(f, "png", "jpeg", "webp") || c.Width <= 0 || c.Height <= 0 || int64(c.Width)*int64(c.Height) > 100000000 {
		return "", 0, 0, fmt.Errorf("unsupported image dimensions or encoding")
	}
	if _, _, e = image.Decode(bytes.NewReader(b)); e != nil {
		return "", 0, 0, e
	}
	return f, c.Width, c.Height, nil
}
func downloadImage(ctx context.Context, raw string) ([]byte, error) {
	// Signed image URLs may contain a query; it is never logged or persisted.
	u, e := url.Parse(raw)
	if e != nil {
		return nil, fmt.Errorf("invalid image URL")
	}
	q := u.RawQuery
	u.RawQuery = ""
	if _, e = safeURL(u.String()); e != nil {
		return nil, e
	}
	u.RawQuery = q
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if e != nil {
		return nil, e
	}
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 5 {
			return fmt.Errorf("too many redirects")
		}
		u := *req.URL
		u.RawQuery = ""
		if _, e := safeURL(u.String()); e != nil {
			return e
		}
		if via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
			return fmt.Errorf("insecure redirect")
		}
		return nil
	}}
	resp, e := client.Do(req)
	if e != nil {
		return nil, e
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("image download failed")
	}
	return limitedBytes(resp.Body)
}
func publishImage(path string, b []byte) error {
	f, e := os.CreateTemp(filepath.Dir(path), ".imagen-*")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, e = f.Write(b); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	// link(2) atomically publishes a complete file and fails if the target exists.
	return os.Link(tmp, path)
}
