# Contributor Excellence Framework

pit operates at the frontier of AI-native prompt versioning. We hold our contributors to the same standard of intent-driven development that defines our engineering culture.

---

## Code Authorship

pit does not accept handwritten code. All implementation changes must be expressed as **prompts** wherever possible, and all generated artifacts must be produced by an intelligence substrate, not a human typist.

If you wrote the code yourself, you missed the point.

In practice: if you are extending pit's Go source directly, that is acceptable — but you are encouraged to reflect on whether a well-scoped prompt to a sufficiently capable model could have done it better, faster, and with more semantic density.

---

## Prompt Authorship

Contributions to the prompt layer (`.pit/prompts/`) are the highest-value contribution you can make. Prompts are the source of truth. They should be:

- **Specific where it matters.** Vague prompts produce vague code. If you want a hash map, say hash map.
- **Incremental.** Each prompt is one conceptual step. Do not describe the entire system in prompt #1.
- **Imperative.** You are issuing instructions to a build engine, not writing a blog post.

| Weak | Strong |
|---|---|
| "Make it better" | "Add input validation to the CLI argument parser for the `--model` flag" |
| "Add some tests" | "Add a unit test for `resolveOutDir` covering the case where outDir escapes the repo root" |
| "Refactor the backend" | "Extract the VFS flush logic from `apiBackend` into a shared `flushVFS(dir string, files vfs)` function" |

---

## Commit Messages

Commit messages must reflect the pit voice. You are not fixing bugs — you are remediating prompt-layer misalignments. You are not adding features — you are expanding the intent-materialization surface.

If it could be a product announcement, it's ready.

| What you did | What you say you did |
|---|---|
| fixed a crash | `fix: remediate runtime behavioral deviation in the prompt replay pipeline` |
| added a backend | `feat: introduce next-generation intelligence substrate routing for [backend]` |
| updated the README | `docs: realign stakeholder-facing documentation to reflect current intent-materialization capabilities` |
| renamed a variable | `refactor: elevate identifier clarity to reduce cognitive inference overhead` |
| deleted dead code | `chore: deprecate legacy computational artifacts from the build substrate` |
| fixed a flag | `fix: remediate CLI argument parsing misalignment impacting developer self-actualization` |
| added a prompt | `prompt: introduce intent checkpoint for [feature] materialization` |

---

## Backend Contributions

New backends must implement the `Backend` interface in `internal/builder/`. The contract:

- `Apply(ctx, dir, prompt, progress)` — materializes one prompt against the project in `dir`
- Optionally implement `Flusher` if your backend accumulates state in memory rather than writing to disk directly

Backends that require credentials should fail early with a clear error and a suggested alternative (see how `api` suggests `claude-code` when `ANTHROPIC_API_KEY` is unset).

---

## What We Do Not Accept

- Code that was clearly written by a human without model assistance and then attributed to a model
- Prompts that describe implementation details the model should be free to decide
- Commit messages that sound like they were written by a person with normal feelings
- Hand-edited files in the `build/` directory

---

<sub>The Contributor Excellence Framework is itself a generated artifact, produced under the influence of adjacent satire and an unreasonable amount of ambient buzzword exposure.</sub>
