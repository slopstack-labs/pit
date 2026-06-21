// Package builder regenerates a project from a pit prompt log by replaying each
// prompt in order through a build backend. The prompts are the source of truth,
// so the materialized output is a function of the log.
//
// Backends decouple "what to build" (the prompts) from "what builds it":
//
//   - api         calls the Anthropic API directly (needs ANTHROPIC_API_KEY)
//   - claude-code drives the installed Claude Code CLI headlessly
//   - copilot     drives the installed GitHub Copilot CLI headlessly
//   - agent       runs any custom agent command from .pit/config.json
//
// The agent backends let pit rebuild a project with no API key, reusing whatever
// coding agent the user already has (Claude Code, Claude Code in VS Code, or
// VS Code Copilot all expose the same CLIs).
package builder

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"pit/internal/repo"
)

// Progress receives human-readable status lines during a build.
type Progress func(format string, args ...any)

// Backend applies a single prompt to the project rooted at dir.
type Backend interface {
	Apply(ctx context.Context, dir string, p repo.Prompt, progress Progress) error
}

// Flusher is an optional Backend capability for backends that accumulate state
// in memory and write it to disk once at the end of a build.
type Flusher interface {
	Flush(dir string) error
}

// NewBackend constructs the backend selected by cfg.
func NewBackend(cfg repo.Config) (Backend, error) {
	switch cfg.Backend {
	case "", repo.BackendAPI:
		return newAPIBackend(cfg.Model)
	case repo.BackendOllama:
		return newOllamaBackend("", cfg.Model), nil
	case repo.BackendClaudeCode, repo.BackendCopilot, repo.BackendAgent:
		cmd, args, err := agentPreset(cfg)
		if err != nil {
			return nil, err
		}
		return newAgentBackend(cfg.Backend, cmd, args)
	default:
		return nil, fmt.Errorf("unknown backend %q", cfg.Backend)
	}
}

// Run rebuilds the project from prompts into outDir, replacing any prior build.
// It returns the absolute output directory and the number of files written.
func Run(ctx context.Context, backend Backend, prompts []repo.Prompt, root, outDir string, progress Progress) (string, int, error) {
	if progress == nil {
		progress = func(string, ...any) {}
	}
	dir, err := resolveOutDir(root, outDir)
	if err != nil {
		return "", 0, err
	}
	if err := os.RemoveAll(dir); err != nil {
		return "", 0, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", 0, err
	}

	for _, p := range prompts {
		progress("[%d/%d] %s", p.Seq, len(prompts), p.Title())
		if err := backend.Apply(ctx, dir, p, progress); err != nil {
			return "", 0, fmt.Errorf("prompt #%d (%s): %w", p.Seq, p.Slug, err)
		}
	}
	if f, ok := backend.(Flusher); ok {
		if err := f.Flush(dir); err != nil {
			return "", 0, err
		}
	}

	n, err := countFiles(dir)
	if err != nil {
		return "", 0, err
	}
	return dir, n, nil
}

// resolveOutDir resolves outDir against root and refuses unsafe locations.
func resolveOutDir(root, outDir string) (string, error) {
	abs := outDir
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, outDir)
	}
	abs = filepath.Clean(abs)

	rootClean := filepath.Clean(root)
	switch {
	case abs == rootClean:
		return "", fmt.Errorf("refusing to build into the repository root; set a dedicated out_dir")
	case abs == filepath.Join(rootClean, repo.DirName):
		return "", fmt.Errorf("refusing to build into %s", repo.DirName)
	}
	if rel, err := filepath.Rel(abs, rootClean); err == nil {
		// rel is the path from abs to rootClean. If it has no leading "..",
		// rootClean is inside abs — the build dir would swallow the repo root.
		if !strings.HasPrefix(rel, "..") {
			return "", fmt.Errorf("refusing to build into %s: it contains the repository root", abs)
		}
	}
	return abs, nil
}

func countFiles(dir string) (int, error) {
	n := 0
	err := filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			n++
		}
		return nil
	})
	return n, err
}
