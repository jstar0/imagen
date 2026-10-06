# imagen

A Go CLI plus an agent skill for image generation and editing. Select a model;
imagen chooses a configured provider, runs a detached task, and saves the images
locally. No MCP server or always-running daemon is required.

## Install

Requires Go 1.26 to build. Supports macOS and Linux; Windows process/file locking
is not implemented. The installed CLI has no Python or Node runtime dependency.

```sh
git clone git@github.com:jstar0/imagen.git
cd imagen
bash scripts/install.sh
```

This installs `~/.local/bin/imagen` and backs up replaced binaries/symlinks. It
preserves existing configuration. `RUNTIME_OUT`, `BIN_DIR`, `CONFIG_DIR` and
`BACKUP_DIR` may be overridden. An explicit `IMAGEN_CONFIG_SOURCE` links an existing
configuration file into the config directory; it is never inferred from a clone.

Install or symlink `skills/imagen` into your agent's skill directory. The CLI is
also usable directly, without any AI client.

## Configure

Use `config.example.json` as a template for `~/.config/imagen/config.json`.
`IMAGEN_CONFIG` or `--config` selects another file. Set the referenced environment
variables, or use `api_key_file` / `env_file` with an owner-only credential file
(mode 0600). Explicit file references take precedence; missing files never cause
silent use of an unrelated inherited key.

**The example lists API protocols, not verified access for your account.** No
credentials, personal endpoints, generated images or runtime state are included.

Config v2 separates:

- `aliases`: convenient names mapping to exact model IDs;
- `providers`: credentials, API endpoints, exact `supported_models`, capabilities;
- `default_model` and a shared local concurrency limit.

`kind` is `gpt-image` or `grok`. A model may have multiple providers. Config v1 and
old task records remain readable.

Optional provider adapter settings:

| Setting | Purpose |
| --- | --- |
| `edit_encoding: "json"` | GPT gateway expects JSON `images[].image_url` data URIs instead of multipart |
| `single_image_requests: true` | Gateway ignores native `n`; explicitly generate the requested count through separate single-image calls |
| `headers` | Additional non-secret gateway headers; cannot override API authentication |
| `priority` | Lower number is preferred when health/success history is otherwise equal |
| `notes` | Surface known channel limitations in results |
| `native_codex: true` | Gateways restricted to official clients: invoke the installed Codex Images client in an isolated, image-only local relay; requires `generation_api: "images"` and `responses_model` |

Capability names: `generate`, `edit`, `batch`, `mask`, `stream`, `compression`,
`background`, `input_fidelity`, `moderation`. Omission preserves the legacy
generate/edit baseline. Declarations guide routing; a gateway may still ignore
individual parameter values, which must be judged from the response.

## Use

```sh
# Provider selected automatically; two separate output files
imagen generate --model gpt-image --prompt 'A blue ceramic teapot' \
  --count 2 --name teapot --output ./images --json

# Edit a previous task, retaining its model but reselecting a capable provider
imagen edit --from JOB --image-index 1 --prompt 'Add a lemon on the right' \
  --name revision --output ./images --json

# Pin a configured provider only when desired
imagen edit --model gpt-image --provider openai --reference ./source.png \
  --mask ./mask.png --prompt 'Edit the transparent area' --output ./images --json

imagen generate --model grok --prompt-file ./prompt.txt \
  --resolution 1k --aspect-ratio 3:2 --output ./images --wait --timeout 120 --json

imagen wait JOB --timeout 30 --json
imagen status JOB --json
imagen list --json
imagen cancel JOB --json
imagen models --json
imagen providers --json
```

See `generate --help` and `edit --help` for all flags. Prompt stdin is
`--prompt-file -`; references are repeatable. `--negative-prompt` appends explicit
exclusions. GPT supports pixel sizes and legacy 1K/2K/4K presets with aspect ratio;
Grok supports resolution/aspect ratio. `--partial-images` enables streaming when
supported. Native streaming currently supports a single image per task.

`--name` is a filename stem, not a path. Existing outputs are protected unless
`--overwrite` is explicit. Returned encoding determines the real extension: the
CLI does not rename PNG bytes to JPEG or silently resize a provider's result.
Masks are alpha PNGs matching the first reference; generative editing cannot
guarantee pixel-exact preservation. macOS `--notify` opts into completion notices.

## Routing, lifetime and costs

For gateways needing streaming Responses generation, see the explicit
[Responses adapter configuration](docs/responses-generation.md). Reference edits
keep their existing Images API path.

For an official-Codex-only gateway, see [native image transport](docs/native-codex-images.md).
This adapter retains the image key and requested image API parameters. Its local
control messages invoke only the native image tool; no remote coding turn is sent.

Without `--provider`, select a capable provider with readable credentials, avoiding
recent failures and preferring recent success, then configured priority. Definite
auth/quota/model/rate rejection and terminal channel errors (HTTP 5xx or explicit
upstream/server errors, including an error-body EOF) fail over if no image or preview
was delivered. Each candidate is attempted at most once within the same job.
A pinned provider never changes. Safety/input errors, local polling timeouts,
partial images and transport loss without a terminal server error do not replay.
Prior attempts retain response metadata and possible duplicate-billing warnings.
No automatic paid health probes or model substitution.
See [fallback behavior and evidence](docs/fallback.md) for the exact boundaries.

Tasks outlive the submitting CLI under `~/.local/state/imagen` (`IMAGEN_HOME` or
`--home` overrides). This directory privately retains prompts, reference paths,
status and health records. Images remain in the chosen output directory. Every
attempt is recorded; credentials are not included. A credential fingerprint
invalidates old cooldown records after rotation.

Since 0.3.0, `wait` and `generate --wait` exit 0 on success or a normal polling timeout, and 1 for
terminal failure/partial/unknown/cancellation. Check JSON `status`; `running`
with `wait_timeout: true` means collect the same job again, not a completed image.
This replaces the old timeout exit 2 to avoid reporting normal waits as tool failures.
Timeout does not cancel. `status` exits 0 when it can read a task,
including a failed task. `partial`/`unknown` results may still contain saved images
or previews; inspect before initiating another paid task. Cancellation/reboot
does not imply upstream cancellation or refund. No automatic restart after a
worker is lost. Use exact IDs for continued edits: global `last` may refer to a
different client's task.

## Develop and verify

```sh
go test ./...
go test -race ./...
go vet ./...
go build ./cmd/imagen
```

Tests use local HTTP fixtures and real detached CLI subprocesses, not paid APIs.
They cover both editing protocols, multiple outputs, masks, SSE heartbeats and
previews, safe failover, pinned providers, no replay on uncertain/safety failures,
cross-session retrieval, cancellation and secret non-persistence. Optional
`IMAGEN_STREAM_FIXTURE` exercises a privately held live SSE capture. Real provider
acceptance is separate from these offline tests.
