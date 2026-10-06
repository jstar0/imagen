# Native Codex Images transport

An Images provider can set `native_codex: true` when its upstream accepts only
official Codex clients. Both generation and editing then originate at the
installed Codex Images client and are relayed to the configured image endpoint.

```json
{
  "kind": "gpt-image",
  "native_codex": true,
  "generation_api": "images",
  "responses_model": "gpt-5.6-luna",
  "headers": {"x-openai-actor-authorization": "Bearer wecode"}
}
```

`responses_model` configures the native session bootstrap only. Local, deterministic
control messages invoke one `image_gen.imagegen` operation; no remote mainline
model request or open-ended coding turn is sent. The original image API body,
requested model, references, mask, count and output parameters are retained. This
is not an identity-only `User-Agent` spoof or a credential substitution.

The native client uses an ephemeral, read-only session and a private temporary
`CODEX_HOME`. Its key is supplied by an environment reference, not written into
the temporary config or `auth.json`. Existing Codex login/config and image keys
are untouched. Only the owned child process and loopback relay are cleaned up.
Automatic API retries are disabled and each local relay accepts one image call.
The native executor receives at most five reference paths; the forwarded image
API body retains every Imagen-validated reference and the mask.

The adapter buffers the non-streaming Images response. It is incompatible with
`generation_api: "responses"`; declare streaming capability only if the gateway
actually supports it. The local wecode profile remains non-streaming. Changing
this transport changes the provider-health fingerprint, so old protocol failures
do not keep the repaired channel in cooldown.

On macOS the current ChatGPT/Codex app's bundled CLI is preferred. Other installs
fall back to `codex` on PATH; `IMAGEN_CODEX_BINARY` can choose an executable path.
The client must expose the native image-generation tool and app-server protocol.
No runtime is installed automatically by an image job.

## Local acceptance, 2026-09-29

Installed `imagen`, automatic provider selection, original wecode image key and
requested `gpt-image-2.5`:

- Generation: job `20260929T030328-5e54e469a65a`, HTTP 200, saved PNG; request id
  `c78bb980d04c2032912273dfa2d28003`.
- Continued edit of that exact job: `20260929T030444-5af82f63abf1`, HTTP 200;
  request id `7d480c9b3a907de676a1d32d3d6abce6`.
- The blue circle became green; white background, layout and `OK` remained.
- Both responses returned 1254×1254 despite a 1024×1024 request. Imagen preserved
  the real bytes and reported the size warning. Neither response echoed a model
  name, so these tests prove image delivery for the requested model, not an
  independently verified backend-model variant.

Unit tests cover payload/credential/native-header preservation, cookie stripping,
duplicate-call refusal, error propagation, constrained local control and health
invalidation. `go test ./...`, `go vet ./...` and race tests passed. These fixture
tests supplement, and do not replace, the two real image calls above.
