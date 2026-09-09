# Codex CLI usability trials

Date: 2026-09-10. Three fresh Codex CLI sessions used gpt-5.6-luna with medium reasoning. They did not inherit the implementation conversation, but did have the normal installed rules, skills and local memory. These are bounded real-provider trials, not a claim of universal agent reliability.

| Trial | Real task | Outcome |
| --- | --- | --- |
| A | Generate two Grok product photos, then edit the first with a lemon | Three images delivered. Automatic channel selection and explicit source job/image selection worked. |
| B | Recolor a reference with GPT Image; inspect 2.5 availability without another generation | One image delivered with honest format/size warnings. Found that the agent sent the whole task file as the visual prompt. |
| C | Repeat B after the skill clarification | One image delivered. The agent used a visual-only prompt, automatic routing and no extra model probe generation. |

The skill now explicitly separates visual briefs from workflow instructions. It also recommends exact source job IDs rather than global `last`, and shared references for identity consistency. Two independently generated images in a batch need not depict an identical object.

The GPT gateway returned PNG at 1254×1254 despite JPEG and 1024×1024 being requested. Imagen preserved the actual file and surfaced warnings; no exact-output compliance is claimed. Inspection of configured mappings did not establish GPT Image 2.5 support or independently identify an upstream model. No claim of 2.5 acceptance is made.

Five final images were produced across these trials. Images, agent transcripts, credentials and local provider configuration remain outside this repository. These trials cover basic generation, batch delivery, reference editing and task continuation; they do not constitute fresh acceptance of every mask, streaming, cancellation or failure-recovery combination.
