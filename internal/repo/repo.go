// Package repo implements pit's on-disk model: a .pit directory holding an
// ordered, append-only log of natural-language prompts. The prompts are the
// source of truth — the actual project is regenerated from them by the builder.
package repo

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	// DirName is the metadata directory at the root of a pit repository.
	DirName = ".pit"
	// promptsDirName holds the numbered prompt files, one per "commit".
	promptsDirName = "prompts"
	// configFileName holds repo-level settings.
	configFileName = "config.json"
)

// DefaultModel is the Claude model used by the api backend.
const DefaultModel = "claude-opus-4-8"

// DefaultOllamaModel is the model used by the ollama backend when none is set.
const DefaultOllamaModel = "qwen2.5-coder"

// DefaultOutDir is where a build materializes the project.
const DefaultOutDir = "build"

// Build backends.
const (
	BackendAPI        = "api"         // Anthropic API directly (needs ANTHROPIC_API_KEY)
	BackendClaudeCode = "claude-code" // the installed Claude Code CLI
	BackendCopilot    = "copilot"     // the installed GitHub Copilot CLI
	BackendAgent      = "agent"       // a custom command from Config.Agent
	BackendOllama     = "ollama"      // local Ollama server (OpenAI-compatible API)
)

// AgentConfig is a custom agent-CLI invocation. Args may contain the literal
// token "{prompt}", which is replaced with the wrapped prompt at build time; if
// absent, the prompt is appended as the final argument.
type AgentConfig struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
}

// Config holds repo-level settings, persisted as .pit/config.json.
type Config struct {
	Model   string       `json:"model"`
	OutDir  string       `json:"out_dir"`
	Backend string       `json:"backend"`
	Agent   *AgentConfig `json:"agent,omitempty"`
}

// Prompt is a single entry in the prompt log — pit's equivalent of a commit.
type Prompt struct {
	Seq  int    // 1-based ordinal, encoded in the filename
	Slug string // short human-readable identifier
	File string // absolute path to the prompt file
	Text string // the prompt body
}

// Title returns the first non-empty line of the prompt, for display.
func (p Prompt) Title() string {
	for _, line := range strings.Split(p.Text, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return p.Slug
}

// Repo is a handle to a pit repository rooted at Root.
type Repo struct {
	Root string
}

var promptFileRe = regexp.MustCompile(`^(\d+)-(.*)\.md$`)

// Find walks up from start looking for a .pit directory and returns the repo.
func Find(start string) (*Repo, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return nil, err
	}
	for {
		if fi, err := os.Stat(filepath.Join(dir, DirName)); err == nil && fi.IsDir() {
			return &Repo{Root: dir}, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, fmt.Errorf("not a pit repository (no %s directory found)", DirName)
		}
		dir = parent
	}
}

// Init creates a new pit repository at root. It errors if one already exists.
func Init(root string) (*Repo, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	pitDir := filepath.Join(abs, DirName)
	if _, err := os.Stat(pitDir); err == nil {
		return nil, fmt.Errorf("%s already exists", pitDir)
	}
	if err := os.MkdirAll(filepath.Join(pitDir, promptsDirName), 0o755); err != nil {
		return nil, err
	}
	r := &Repo{Root: abs}
	if err := r.writeConfig(Config{Model: DefaultModel, OutDir: DefaultOutDir, Backend: BackendAPI}); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Repo) pitDir() string     { return filepath.Join(r.Root, DirName) }
func (r *Repo) promptsDir() string { return filepath.Join(r.pitDir(), promptsDirName) }
func (r *Repo) configPath() string { return filepath.Join(r.pitDir(), configFileName) }

// Config loads repo settings, filling in defaults for any missing fields.
func (r *Repo) Config() (Config, error) {
	cfg := Config{Model: DefaultModel, OutDir: DefaultOutDir}
	data, err := os.ReadFile(r.configPath())
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("reading %s: %w", r.configPath(), err)
	}
	if cfg.Model == "" {
		cfg.Model = DefaultModel
	}
	if cfg.OutDir == "" {
		cfg.OutDir = DefaultOutDir
	}
	if cfg.Backend == "" {
		cfg.Backend = BackendAPI
	}
	return cfg, nil
}

// SetConfig persists cfg as the repository configuration.
func (r *Repo) SetConfig(cfg Config) error { return r.writeConfig(cfg) }

func (r *Repo) writeConfig(cfg Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(r.configPath(), append(data, '\n'), 0o644)
}

// Prompts returns the prompt log in order.
func (r *Repo) Prompts() ([]Prompt, error) {
	entries, err := os.ReadDir(r.promptsDir())
	if err != nil {
		return nil, err
	}
	var prompts []Prompt
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := promptFileRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		seq, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		path := filepath.Join(r.promptsDir(), e.Name())
		text, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		prompts = append(prompts, Prompt{
			Seq:  seq,
			Slug: m[2],
			File: path,
			Text: strings.TrimRight(string(text), "\n"),
		})
	}
	sort.Slice(prompts, func(i, j int) bool { return prompts[i].Seq < prompts[j].Seq })
	return prompts, nil
}

// AddPrompt appends a new prompt to the log and returns it.
func (r *Repo) AddPrompt(text string) (Prompt, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Prompt{}, fmt.Errorf("prompt text is empty")
	}
	existing, err := r.Prompts()
	if err != nil {
		return Prompt{}, err
	}
	seq := 1
	if n := len(existing); n > 0 {
		seq = existing[n-1].Seq + 1
	}
	slug := slugify(text)
	name := fmt.Sprintf("%04d-%s.md", seq, slug)
	path := filepath.Join(r.promptsDir(), name)
	if err := os.WriteFile(path, []byte(text+"\n"), 0o644); err != nil {
		return Prompt{}, err
	}
	return Prompt{Seq: seq, Slug: slug, File: path, Text: text}, nil
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// slugify derives a short filesystem-friendly identifier from prompt text.
func slugify(text string) string {
	// Use the first line so the slug reflects the headline intent.
	first := text
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		first = text[:i]
	}
	s := nonSlug.ReplaceAllString(strings.ToLower(first), "-")
	s = strings.Trim(s, "-")
	if len(s) > 50 {
		s = strings.Trim(s[:50], "-")
	}
	if s == "" {
		s = "prompt"
	}
	return s
}
