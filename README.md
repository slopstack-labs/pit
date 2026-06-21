# pit
### *Beyond Deterministic Version Control*

> "We didn't reinvent the repository. We asked an AI to rebuild it on demand."
> — pit Engineering Blog, Issue 1 (Ongoing)

**pit** is a next-generation, AI-first, LLM-native prompt versioning platform that leverages the full generative capacity of large language models to holistically materialize software artifacts from natural-language intent — on demand, at inference speed, across every major intelligence provider in the ecosystem.

Traditional version control persists *code*. Code is a lossy compression of intent — brittle, opinionated, and constrained by the cognitive bandwidth of whoever happened to be at the keyboard. pit moves the source of truth to the *prompt layer*, reasoning about what you meant rather than what you typed — eliminating the deterministic intermediate steps that have constrained the software development lifecycle for decades. The bottleneck is no longer your codebase. It's your willingness to describe.

**Key differentiators:**
- Intent-native versioning substrate — commit prompts, not implementations
- Zero hand-written code — every file is a generated stakeholder deliverable
- Multi-backend intelligence routing — Anthropic API, Ollama, Claude Code, GitHub Copilot, or your own agentic pipeline
- Full project regeneration from a clean prompt log on every build
- Non-deterministic reproducibility — the prompts pin *intent*, not bytes
- Fully on-premise inference available via the `ollama` backend — your prompts, your model, your build
- Radical shift-left code delivery: bypass the implementation layer entirely
- `pit build` as a deployment strategy

**pit is the only version control system built on the insight that your code doesn't need to be *written* — it needs to be *described*.**

## Roadmap

- [x] Anthropic API backend
- [x] Ollama backend (local inference, no API key required)
- [x] Claude Code backend
- [x] GitHub Copilot backend
- [x] Custom agent backend
- [ ] `pit revert` — roll back to an earlier prompt-space checkpoint
- [ ] `pit diff` — semantic diff between prompt versions
- [ ] `pit push` / remote prompt registries

## Contributing

See the [Contributor Excellence Framework](CONTRIBUTING.md).

## Requirements

- Go 1.21+ to build
- **For `api` backend:** `ANTHROPIC_API_KEY`
- **For `ollama` backend:** [Ollama](https://ollama.com) running locally with a model pulled
- **For `claude-code` backend:** Claude Code CLI (`claude`) installed
- **For `copilot` backend:** GitHub Copilot CLI (`copilot`) installed

## Build

```sh
go build -o pit .
```

## Quick start

```sh
pit init
pit add "Create a snake game in a single index.html using <canvas> and vanilla JS"
pit add "Add a score counter and a game-over screen with a restart button"
pit add "Support both arrow keys and WASD for movement"
pit build              # replays all prompts -> ./build
```

`pit build` clears the output directory and replays the prompt log **in order**, each prompt building on the result of the previous one, regenerating the entire project from scratch.

## Commands

| Command | Description |
|---|---|
| `pit init [--backend B]` | Initialize a pit repository, git repo, README, and .gitignore |
| `pit add <prompt…>` | Append a prompt to the log (also `-f FILE`, or `-` for stdin) |
| `pit log` | List the prompt log |
| `pit show <n>` | Print prompt number *n* |
| `pit build [flags]` | Rebuild the project from the prompt log |

`pit build` flags: `--out DIR`, `--backend B`, `--model M`.

Prompts are plain Markdown files under `.pit/prompts/`. Edit them by hand, reorder them by renaming, delete one to drop that step — then `pit build`.

## Backends

A **backend** is the intelligence substrate that materializes prompts into code. pit routes your build intent through the agent you already have — no API key required unless you want one.

| Backend | Intelligence Layer | Requirements |
|---|---|---|
| `api` *(default)* | Anthropic API | `ANTHROPIC_API_KEY` |
| `ollama` | Local Ollama server | Ollama running locally |
| `claude-code` | Claude Code CLI (`claude`) | Claude Code installed |
| `copilot` | GitHub Copilot CLI (`copilot`) | Copilot CLI installed |
| `agent` | Custom command | That command on `PATH` |

Set the backend at init, per build, or in `.pit/config.json`:

```sh
pit init --backend claude-code      # no API key needed
pit build --backend copilot
```

### Ollama — *Fully On-Premise Intent Materialization*

The `ollama` backend routes build intent through a local [Ollama](https://ollama.com) server via its OpenAI-compatible inference API. No API key. No data egress. Your prompts, your model, your build.

```sh
pit init --backend ollama
pit build --backend ollama --model qwen2.5-coder:14b
```

The default model is `qwen2.5-coder`. Override with `--model` or the `"model"` field in `.pit/config.json`. The server URL defaults to `http://localhost:11434`; set `OLLAMA_HOST` to route to a remote inference node.

Models with strong tool-calling fidelity perform best: `qwen2.5-coder`, `llama3.1`, `mistral-nemo`.

### Claude Code — *Agentic CLI Co-Build Pipeline*

The VS Code extension and the terminal both surface the same `claude` CLI, so the `claude-code` backend covers all of them. pit drives it headlessly in the build directory, auto-accepting all file edits:

```
claude -p "<wrapped prompt>" --permission-mode acceptEdits
```

### GitHub Copilot — *Enterprise Intelligence Routing*

The `copilot` backend drives the GitHub Copilot CLI non-interactively:

```
copilot -p "<wrapped prompt>" --allow-all-tools
```

### Custom Agent — *Bring Your Own Intelligence Substrate*

Route build intent through any agent (Cursor, aider, a different flag set) via the `agent` field in `.pit/config.json`. The token `{prompt}` is replaced with the wrapped prompt; omit it and the prompt is appended as the final argument.

```json
{
  "out_dir": "build",
  "backend": "agent",
  "agent": {
    "command": "claude",
    "args": ["-p", "{prompt}", "--permission-mode", "bypassPermissions"]
  }
}
```

## Notes

- Builds are not bit-for-bit reproducible — the prompts pin *intent*, not exact bytes. Write prompts that are specific where it matters.
- The output directory is fully regenerated each build; don't hand-edit it — edit the prompts instead.
- `build/` is a generated artifact. `pit init` adds it to `.gitignore` automatically.

---

<sub>This is a real tool that actually works. Mostly. Results vary by model, prompt quality, and the position of the moon.</sub>
