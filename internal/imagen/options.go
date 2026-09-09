package imagen

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

var sizePresets = map[string]map[string]string{
	"1K": {"1:1": "1024x1024", "3:2": "1536x1024", "2:3": "1024x1536", "16:9": "1792x1024", "9:16": "1024x1792"},
	"2K": {"1:1": "2048x2048", "3:2": "2304x1536", "2:3": "1536x2304", "16:9": "2560x1440", "9:16": "1440x2560"},
	"4K": {"1:1": "2880x2880", "3:2": "3456x2304", "2:3": "2304x3456", "16:9": "3840x2160", "9:16": "2160x3840"},
}

func NormalizeRequest(model string, r Request) (Request, error) {
	for i, p := range r.References {
		a, e := filepath.Abs(expandPath(p))
		if e != nil {
			return r, e
		}
		r.References[i] = a
	}
	if r.Mask != "" {
		a, e := filepath.Abs(expandPath(r.Mask))
		if e != nil {
			return r, e
		}
		r.Mask = a
	}
	if r.PartialImages > 0 {
		r.Stream = true
	}
	if r.Count < 1 || r.Count > 10 {
		return r, fmt.Errorf("count must be 1..10")
	}
	if strings.HasPrefix(model, "grok") {
		if _, ok := sizePresets[strings.ToUpper(r.Size)]; ok {
			resolution := strings.ToLower(r.Size)
			if resolution == "4k" {
				return r, fmt.Errorf("Grok supports 1k or 2k, not 4K")
			}
			if r.Resolution != "" && r.Resolution != resolution {
				return r, fmt.Errorf("size preset conflicts with resolution")
			}
			r.Resolution = resolution
			r.Size = ""
		}
		return r, nil
	}
	preset := strings.ToUpper(r.Size)
	if r.Size == "" && r.AspectRatio != "" {
		preset = "1K"
	}
	if table, ok := sizePresets[preset]; ok {
		ratio := r.AspectRatio
		if ratio == "" {
			ratio = "1:1"
		}
		size, ok := table[ratio]
		if !ok {
			return r, fmt.Errorf("preset supports 1:1,3:2,2:3,16:9,9:16; use explicit pixel size for another ratio")
		}
		r.Size = size
		r.AspectRatio = ""
	} else if r.AspectRatio != "" {
		return r, fmt.Errorf("combine GPT aspect-ratio with 1K/2K/4K preset, not explicit pixel size")
	}
	return r, nil
}

func (s Store) SourceImage(id string, index int) (struct{ Path, Model string }, error) {
	result := struct{ Path, Model string }{}
	if id == "last" || id == "latest" {
		jobs, e := s.List(1000)
		if e != nil {
			return result, e
		}
		id = ""
		for _, j := range jobs {
			if j.Status == "succeeded" && j.Result != nil {
				id = j.ID
				break
			}
		}
		if id == "" {
			return result, fmt.Errorf("no successful task found")
		}
	}
	j, e := s.Status(id)
	if e != nil {
		return result, e
	}
	if j.Status != "succeeded" || j.Result == nil {
		return result, fmt.Errorf("source task has no successful image")
	}
	images := j.Result.Images
	if len(images) == 0 {
		images = []ImageFile{{Path: j.Result.Path}}
	}
	if index < 1 || index > len(images) {
		return result, fmt.Errorf("image-index must be 1..%d", len(images))
	}
	result.Path = images[index-1].Path
	result.Model = j.Profile.Model
	return result, nil
}

func (s Store) Wait(ctx context.Context, id string, seconds float64) (Job, bool, error) {
	if seconds < 0 || seconds > 3600 {
		return Job{}, false, fmt.Errorf("timeout must be 0..3600 seconds")
	}
	deadline := time.Now().Add(time.Duration(seconds * float64(time.Second)))
	for {
		j, e := s.Status(id)
		if e != nil {
			return j, false, e
		}
		if terminal(j.Status) {
			return j, false, nil
		}
		if !time.Now().Before(deadline) {
			return j, true, nil
		}
		select {
		case <-ctx.Done():
			return j, false, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}
