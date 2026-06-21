package builder

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"pit/internal/repo"
)

// promptPlaceholder is replaced with the wrapped prompt inside an agent command
// template. If a template contains none, the prompt is appended as a final arg.
const promptPlaceholder = "{prompt}"

const agentInstruction = `You are applying ONE incremental change to a software project located in the current working directory. The project so far was produced by replaying earlier prompts; the next prompt is below.

Apply ONLY this prompt's intent: create, modify, or delete files in the current directory as needed. Make the requested change and nothing more; preserve unrelated files. Write real, working code — no placeholders. Do not ask questions; act on a reasonable interpretation. When finished, stop.

Prompt:
%s`

// agentBackend builds by invoking an external coding-agent CLI (Claude Code,
// GitHub Copilot CLI, or a custom command) once per prompt, with the build
// directory as its working directory. The agent edits files on disk directly.
type agentBackend struct {
	name    string
	command string
	args    []string // template; promptPlaceholder is substituted per prompt
}

func newAgentBackend(name, command string, args []string) (*agentBackend, error) {
	if _, err := exec.LookPath(command); err != nil {
		return nil, fmt.Errorf("%s backend: command %q not found in PATH (install it, or set a different backend)", name, command)
	}
	return &agentBackend{name: name, command: command, args: args}, nil
}

func (b *agentBackend) Apply(ctx context.Context, dir string, p repo.Prompt, progress Progress) error {
	prompt := fmt.Sprintf(agentInstruction, p.Text)

	args := make([]string, 0, len(b.args)+1)
	substituted := false
	for _, a := range b.args {
		if strings.Contains(a, promptPlaceholder) {
			a = strings.ReplaceAll(a, promptPlaceholder, prompt)
			substituted = true
		}
		args = append(args, a)
	}
	if !substituted {
		args = append(args, prompt)
	}

	cmd := exec.CommandContext(ctx, b.command, args...)
	cmd.Dir = dir
	cmd.Stdin = nil
	cmd.Stdout = os.Stderr // surface the agent's output as build progress
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s (%s) failed: %w", b.name, b.command, err)
	}
	return nil
}

// agentPreset resolves the command and argument template for an agent backend.
// A custom command in cfg.Agent always wins; otherwise built-in presets apply.
func agentPreset(cfg repo.Config) (string, []string, error) {
	if cfg.Agent != nil && cfg.Agent.Command != "" {
		return cfg.Agent.Command, cfg.Agent.Args, nil
	}
	switch cfg.Backend {
	case repo.BackendClaudeCode:
		// Headless print mode, auto-accepting file edits.
		return "claude", []string{"-p", promptPlaceholder, "--permission-mode", "acceptEdits"}, nil
	case repo.BackendCopilot:
		// GitHub Copilot CLI, non-interactive with tools enabled.
		return "copilot", []string{"-p", promptPlaceholder, "--allow-all-tools"}, nil
	default:
		return "", nil, fmt.Errorf("backend %q requires an \"agent\" command in %s/config.json", cfg.Backend, repo.DirName)
	}
}
