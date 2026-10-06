# Responses generation adapter

For gateways where long non-streaming Images generation requests time out, a
GPT Image provider may explicitly configure:

```json
{
  "generation_api": "responses",
  "responses_model": "YOUR_SUPPORTED_MAINLINE_MODEL"
}
```

This uses `/responses` with streaming transport, `store: false`, a forced
`image_generation` tool, and the requested image model in the tool's `model`.
The mainline model is separate and must be explicitly configured; it can revise
the image prompt and adds its own usage. The CLI exposes it as
`result.transport_model`. A gateway declaration or request parameter does not
independently establish the upstream image model used.

This setting applies to generation only. Reference edits and masks keep the
provider's existing `/images/edits` encoding. Moderation overrides are not exposed
by this generation adapter and are rejected or routed to another capable provider.
Batches use one serial request per image, retaining completed images on failure.
The adapter saves completed image items, deduplicates them from final response
output, and requires a completed response for task success. An interrupted stream
retains available images and remains uncertain; it does not trigger a paid replay.

It is an explicitly configured protocol, not an implicit protocol switch after a 504.
An unpinned job may move to its next configured provider after a terminal channel
error with no images; that provider uses its own explicitly configured protocol.
Changing protocol invalidates the old protocol's local health record.

Official protocol reference:
https://developers.openai.com/api/docs/guides/tools-image-generation

## Local acceptance, 2026-09-10

The same scientific flowchart brief, GPT Image 2 request, high quality and
1536×1024 size were used throughout:

- The existing Duojie Images path delivered a 1536×1024 PNG in about 223 seconds.
- The wecode Images path returned HTML HTTP 504 after about 61 seconds both with
  and without the local explicit HTTP proxy.
- The wecode Responses path delivered a 1536×1024 PNG in about 60 seconds using
  gpt-5.6-luna as orchestrator and gpt-image-2 as the requested image tool model.

These observations establish working image delivery through the tested paths,
not permanent gateway uptime or scientific correctness of every generated arrow.
The original historical Duojie failures lacked enough diagnostics to identify
their exact response body; this successful run does not retroactively establish
what caused those failures.
