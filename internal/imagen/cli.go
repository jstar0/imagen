package imagen

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const Version = "0.2.1"

// BuildCommit is supplied by the release/local installer; never changes routing.
var BuildCommit string

type jobView struct {
	ID              string    `json:"id"`
	Status          string    `json:"status"`
	Alias           string    `json:"alias"`
	Model           string    `json:"model"`
	Provider        string    `json:"provider,omitempty"`
	Attempts        []Attempt `json:"attempts,omitempty"`
	Output          string    `json:"output"`
	CreatedAt       string    `json:"created_at"`
	UpdatedAt       string    `json:"updated_at"`
	Result          *Result   `json:"result,omitempty"`
	Error           string    `json:"error,omitempty"`
	CancelRequested bool      `json:"cancel_requested,omitempty"`
}

func publicJob(j Job) jobView {
	if j.Result != nil && j.Request.Size != "" && j.Request.Size != "auto" {
		result := *j.Result
		result.RequestedSize = j.Request.Size
		actual := fmt.Sprintf("%dx%d", result.Width, result.Height)
		if actual != j.Request.Size {
			result.Warnings = append(result.Warnings, fmt.Sprintf("Provider returned %s instead of requested %s; original image preserved", actual, j.Request.Size))
		}
		j.Result = &result
	}
	return jobView{ID: j.ID, Status: j.Status, Alias: j.Alias, Model: j.Profile.Model, Provider: j.Profile.Name, Attempts: j.Attempts, Output: j.Request.Output, CreatedAt: j.CreatedAt, UpdatedAt: j.UpdatedAt, Result: j.Result, Error: j.Error, CancelRequested: j.CancelRequested}
}

func NewCommand() *cobra.Command {
	var configPath, home string
	var asJSON bool
	root := &cobra.Command{Use: "imagen", Short: "Persistent asynchronous Grok and GPT Image CLI", Version: Version, SilenceUsage: true, SilenceErrors: true}
	if BuildCommit!=""{root.Version+=" ("+BuildCommit+")"}
	root.PersistentFlags().StringVar(&configPath, "config", ConfigPath(), "Provider configuration file")
	root.PersistentFlags().StringVar(&home, "home", StateRoot(), "Persistent task directory (shared between clients)")
	root.PersistentFlags().BoolVar(&asJSON, "json", false, "Emit stable JSON")
	emit := func(cmd *cobra.Command, v any) error {
		if asJSON {
			switch x := v.(type) {
			case Job:
				v = publicJob(x)
			case []Job:
				views := make([]jobView, 0, len(x))
				for _, j := range x {
					views = append(views, publicJob(j))
				}
				v = views
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(v)
		}
		switch x := v.(type) {
		case Job:
			x.Result = publicJob(x).Result
			fmt.Fprintf(cmd.OutOrStdout(), "%s  %s  %s via %s\n", x.ID, x.Status, x.Profile.Model, x.Profile.Name)
			if x.Result != nil {
				images := x.Result.Images
				if len(images) == 0 && x.Result.Path != "" {
					images = []ImageFile{{Path: x.Result.Path, Width: x.Result.Width, Height: x.Result.Height, Format: x.Result.Format}}
				}
				for _, im := range images {
					fmt.Fprintf(cmd.OutOrStdout(), "%s  %dx%d %s\n", im.Path, im.Width, im.Height, im.Format)
				}
				for _, im := range x.Result.Previews {
					fmt.Fprintln(cmd.OutOrStdout(), "preview:", im.Path)
				}
				for _, warning := range x.Result.Warnings {
					fmt.Fprintln(cmd.OutOrStdout(), "Warning:", warning)
				}
			}
			if x.Error != "" {
				fmt.Fprintln(cmd.OutOrStdout(), x.Error)
			}
			if x.CancelRequested && !terminal(x.Status) {
				fmt.Fprintln(cmd.OutOrStdout(), "Cancellation requested; the provider may still complete and bill a running request.")
			}
		case []Job:
			for _, j := range x {
				fmt.Fprintf(cmd.OutOrStdout(), "%s  %-12s %s\n", j.ID, j.Status, j.Alias)
			}
		default:
			b, _ := json.MarshalIndent(v, "", "  ")
			fmt.Fprintln(cmd.OutOrStdout(), string(b))
		}
		return nil
	}
	for _, op := range []string{"generate", "edit"} {
		var r Request
		var alias, promptFile, provider, from string
		var imageIndex, compression int
		var wait bool
		var waitSeconds float64
		gen := &cobra.Command{Use: op, Short: "Generate or edit images with automatic provider selection", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := LoadConfig(configPath)
			if err != nil {
				return err
			}
			if promptFile != "" {
				if cmd.Flags().Changed("prompt") {
					return fmt.Errorf("choose --prompt or --prompt-file")
				}
				var b []byte
				if promptFile == "-" {
					b, err = io.ReadAll(io.LimitReader(cmd.InOrStdin(), (1<<20)+1))
				} else {
					b, err = os.ReadFile(expandPath(promptFile))
				}
				if err != nil {
					return err
				}
				r.Prompt = string(b)
			}
			if strings.TrimSpace(r.Prompt) == "" {
				return fmt.Errorf("--prompt or --prompt-file is required")
			}
			if len(r.Prompt) > 1<<20 {
				return fmt.Errorf("prompt exceeds 1 MiB")
			}
			s := Store{Root: expandPath(home)}
			if from != "" {
				source, e := s.SourceImage(from, imageIndex)
				if e != nil {
					return e
				}
				r.References = append(r.References, source.Path)
				if alias == "" {
					alias = source.Model
				}
			}
			if op == "edit" && len(r.References) == 0 {
				return fmt.Errorf("edit requires --reference or --from JOB")
			}
			if cmd.Flags().Changed("compression") {
				r.Compression = &compression
			}
			model, e := ResolveModel(cfg, alias)
			if e != nil {
				return e
			}
			r, e = NormalizeRequest(model, r)
			if e != nil {
				return e
			}
			profiles, e := s.Routes(cfg, model, provider, r)
			if e != nil {
				return e
			}
			j, err := s.SubmitRoutes(model, profiles, r, cfg.Concurrency)
			if err != nil {
				return err
			}
			if wait {
				j, timed, e := s.Wait(cmd.Context(), j.ID, waitSeconds)
				if e != nil {
					return e
				}
				if timed && asJSON {
					e = json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
						jobView
						WaitTimeout bool `json:"wait_timeout"`
					}{publicJob(j), true})
				} else {
					e = emit(cmd, j)
				}
				if e != nil {
					return e
				}
				if timed {
					return &ExitError{Code: 2}
				}
				if j.Status != "succeeded" {
					return &ExitError{Code: 1}
				}
				return nil
			}
			return emit(cmd, j)
		}}
		gen.Flags().StringVar(&alias, "model", "", "Configured model alias; see imagen models")
		gen.Flags().StringVar(&provider, "provider", "", "Pin a provider; omitted means automatic selection and safe failover")
		gen.Flags().StringVar(&from, "from", "", "Edit an output from JOB, last or latest")
		gen.Flags().IntVar(&imageIndex, "image-index", 1, "One-based image number for --from")
		gen.Flags().StringVar(&r.Name, "name", "", "Output filename stem; defaults to unique job ID")
		gen.Flags().BoolVar(&r.Overwrite, "overwrite", false, "Explicitly replace an existing named image")
		gen.Flags().BoolVar(&r.Notify, "notify", false, "Notify on macOS when the background task finishes")
		gen.Flags().IntVar(&r.Count, "count", 1, "Number of images (1..10)")
		gen.Flags().StringVar(&r.Mask, "mask", "", "PNG alpha mask matching the first reference dimensions")
		gen.Flags().StringVar(&r.NegativePrompt, "negative-prompt", "", "Explicit exclusions appended to the prompt")
		gen.Flags().IntVar(&compression, "compression", 100, "JPEG/WebP output compression (0..100)")
		gen.Flags().StringVar(&r.Background, "background", "", "GPT background: auto/opaque/transparent when supported")
		gen.Flags().StringVar(&r.InputFidelity, "input-fidelity", "", "For image models supporting adjustable input fidelity")
		gen.Flags().StringVar(&r.Moderation, "moderation", "", "GPT moderation: auto/low")
		gen.Flags().BoolVar(&r.Stream, "stream", false, "Stream GPT image events when supported by provider")
		gen.Flags().IntVar(&r.PartialImages, "partial-images", 0, "Intermediate previews, 0..3 (implies stream)")
		gen.Flags().BoolVar(&wait, "wait", false, "Wait for images instead of returning after submission")
		gen.Flags().Float64Var(&waitSeconds, "timeout", 120, "Maximum wait seconds; timed-out jobs keep running")
		gen.Flags().StringVar(&r.Prompt, "prompt", "", "Prompt, forwarded unchanged")
		gen.Flags().StringVar(&promptFile, "prompt-file", "", "Read prompt from a UTF-8 file, or - for stdin")
		gen.Flags().StringArrayVar(&r.References, "reference", nil, "Local reference image; repeat for multiple inputs")
		gen.Flags().StringVar(&r.Output, "output", ".", "Output directory; unique job-named image, never overwrite")
		gen.Flags().StringVar(&r.Size, "size", "", "GPT pixel dimensions or legacy 1K/2K/4K preset")
		gen.Flags().StringVar(&r.AspectRatio, "aspect-ratio", "", "Grok aspect ratio, e.g. 16:9")
		gen.Flags().StringVar(&r.Resolution, "resolution", "", "Grok resolution: 1k or 2k")
		gen.Flags().StringVar(&r.Quality, "quality", "", "GPT: low/medium/high/auto; Grok: low/medium/auto")
		gen.Flags().StringVar(&r.Format, "format", "", "GPT output format: png/jpeg/webp; Grok keeps native encoding")
		root.AddCommand(gen)
	}
	status := &cobra.Command{Use: "status JOB", Short: "Read task state; never retry or restart generation", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		j, e := (Store{Root: expandPath(home)}).Status(args[0])
		if e != nil {
			return e
		}
		return emit(cmd, j)
	}}
	root.AddCommand(status)
	var timeout float64
	wait := &cobra.Command{Use: "wait JOB", Short: "Wait for a task; timeout does not cancel generation", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if timeout < 0 || timeout > 3600 {
			return fmt.Errorf("timeout must be 0..3600 seconds")
		}
		deadline := time.Now().Add(time.Duration(timeout * float64(time.Second)))
		for {
			j, e := (Store{Root: expandPath(home)}).Status(args[0])
			if e != nil {
				return e
			}
			if terminal(j.Status) {
				if e = emit(cmd, j); e != nil {
					return e
				}
				if j.Status != "succeeded" {
					return &ExitError{Code: 1}
				}
				return nil
			}
			if !time.Now().Before(deadline) {
				if asJSON {
					e = json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
						jobView
						WaitTimeout bool `json:"wait_timeout"`
					}{publicJob(j), true})
				} else {
					e = emit(cmd, j)
				}
				if e != nil {
					return e
				}
				return &ExitError{Code: 2}
			}
			select {
			case <-cmd.Context().Done():
				return cmd.Context().Err()
			case <-time.After(250 * time.Millisecond):
			}
		}
	}}
	wait.Flags().Float64Var(&timeout, "timeout", 30, "Seconds to wait (exit 2 if still active; worker continues)")
	root.AddCommand(wait)
	var limit int
	list := &cobra.Command{Use: "list", Short: "List recent tasks across sessions", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if limit < 1 || limit > 1000 {
			return fmt.Errorf("limit must be 1..1000")
		}
		v, e := (Store{Root: expandPath(home)}).List(limit)
		if e != nil {
			return e
		}
		return emit(cmd, v)
	}}
	list.Flags().IntVar(&limit, "limit", 20, "Maximum jobs")
	root.AddCommand(list)
	root.AddCommand(&cobra.Command{Use: "cancel JOB", Short: "Request local cancellation; does not guarantee upstream cancellation or refund", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		j, e := (Store{Root: expandPath(home)}).Cancel(args[0])
		if e != nil {
			return e
		}
		return emit(cmd, j)
	}})
	for _, name := range []string{"models", "providers"} {
		root.AddCommand(&cobra.Command{Use: name, Short: "Show models, provider capabilities and local health (no paid probes)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
			cfg, e := LoadConfig(configPath)
			if e != nil {
				return e
			}
			out, e := (Store{Root: expandPath(home)}).ProviderStatus(cfg)
			if e != nil {
				return e
			}
			return emit(cmd, map[string]any{"default_model": cfg.DefaultModel, "aliases": cfg.Aliases, "providers": out})
		}})
	}
	var id, workerToken string
	worker := &cobra.Command{Use: "_worker", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		ready := os.NewFile(3, "ready")
		if ready != nil {
			defer ready.Close()
		}
		return (Store{Root: expandPath(home)}).Worker(id, workerToken, ready)
	}}
	worker.Flags().StringVar(&id, "id", "", "Job ID")
	worker.Flags().StringVar(&workerToken, "token", "", "Worker identity")
	root.AddCommand(worker)
	return root
}

type ExitError struct{ Code int }

func (e *ExitError) Error() string { return "" }
