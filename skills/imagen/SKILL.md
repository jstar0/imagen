---
name: imagen
description: Generate and edit raster images with GPT Image or Grok through the imagen CLI, including reference images, masks, multiple outputs and continued edits. Channels are selected automatically unless the user chooses one.
---

# Imagen

Use `imagen` (fallback `~/.local/bin/imagen`). Select the user's requested model, or use the configured default. Leave `--provider` omitted unless the user names a channel. Models and providers are different: `imagen models --json` shows the local mappings, capabilities and health; command `--help` supplies parameter details. Credentials are loaded by the CLI.

```bash
imagen generate --model gpt-image --prompt-file ./prompt.txt --count 2 --output ./images --json
imagen edit --from JOB --prompt "..." --name revision --output ./images --json
imagen wait JOB --timeout 30 --json
```

Use a task-specific output directory. A prompt file or stdin avoids shell escaping for long text; its content is only the visual brief, not the whole task message, workflow instructions or validation checklist. Repeated `--reference` passes multiple images; `--negative-prompt` appends explicit exclusions. For continued edits, reuse the exact task ID from this task and optionally `--image-index`; global `--from last` might refer to another client's image. Edits inherit the source model unless overridden. For consistent product identity across variants, edit a shared reference; a count batch does not lock identity.

Submission returns before the image is ready. Unless the user requests background submission only, collect and inspect the images before ending the turn. Continue useful work and collect the same job, or use `--wait`. Read the JSON status: `running` with `wait_timeout` means wait again on that job, not regenerate; exit 0 alone does not mean an image is ready. Unpinned tasks automatically try the next compatible provider after a terminal channel error with no delivered images; keep waiting on the same job and report consequential billing warnings. `list` finds earlier jobs. On terminal `partial` or `unknown`, inspect saved results and diagnostics before another submission; a local empty directory does not prove no upstream generation or charge.

Deliver every `result.images` entry; legacy `result.path` is only the first. Open the images to check the requested visual changes. Report actual encoding/dimensions and consequential warnings: gateways may normalize parameters, and masks do not guarantee pixel-exact preservation. Previews are not final images. Unsupported models are not silently replaced. A local cancellation does not guarantee an upstream refund.
