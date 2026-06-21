// Command pit is version control for prompts: a project is described by an
// ordered log of natural-language prompts and rebuilt from them on demand.
//
//	pit init                initialize a pit repository
//	pit add "<prompt>"      append a prompt to the log
//	pit log                 list the prompt log
//	pit show <n>            print prompt #n
//	pit build               regenerate the project from the prompt log
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"

	"pit/internal/builder"
	"pit/internal/repo"
)

const usage = `pit — p(rompt)(g)it: version control for prompts

Usage:
  pit init [--backend B]   Initialize a pit repository in the current directory
  pit add <prompt...>      Append a prompt to the log (or -f FILE, or - for stdin)
  pit log                  Show the prompt log
  pit show <n>             Print the text of prompt number n
  pit build [flags]        Rebuild the project from the prompt log
  pit config [flags]       Show or update repository configuration
  pit help                 Show this help

The prompts in .pit/prompts are the source of truth. 'pit build' replays them in
order to regenerate the project, writing it to the out_dir (default ./build).

Backends (set with 'pit init --backend B', 'pit build --backend B', or the
"backend" field in .pit/config.json):
  api          Call the Anthropic API directly (needs ANTHROPIC_API_KEY)
  ollama       Call a local Ollama server (default model: qwen2.5-coder)
  claude-code  Drive the installed Claude Code CLI (claude) — no API key needed
  copilot      Drive the installed GitHub Copilot CLI (copilot)
  agent        Run a custom command from the "agent" field in config.json

build flags:
  --out DIR        Output directory (overrides out_dir)
  --backend B      Build backend (overrides config)
  --model M        Model for the api backend (overrides config)

config flags:
  --backend B      Set the default build backend
  --model M        Set the default model
  --out DIR        Set the default output directory`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}

	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "init":
		err = cmdInit(args)
	case "add":
		err = cmdAdd(args)
	case "log":
		err = cmdLog()
	case "show":
		err = cmdShow(args)
	case "build":
		err = cmdBuild(args)
	case "config":
		err = cmdConfig(args)
	case "help", "-h", "--help":
		fmt.Println(usage)
	default:
		fmt.Fprintf(os.Stderr, "pit: unknown command %q\n\n%s\n", cmd, usage)
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "pit:", err)
		os.Exit(1)
	}
}

func cmdInit(args []string) error {
	backend := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--backend", "-b":
			if i+1 >= len(args) {
				return fmt.Errorf("--backend requires a value")
			}
			backend = args[i+1]
			i++
		default:
			return fmt.Errorf("unknown flag %q", args[i])
		}
	}

	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	r, err := repo.Init(wd)
	if err != nil {
		return err
	}
	cfg, err := r.Config()
	if err != nil {
		return err
	}
	if backend != "" {
		cfg.Backend = backend
		if err := r.SetConfig(cfg); err != nil {
			return err
		}
	}

	if err := scaffoldGit(wd, cfg.OutDir); err != nil {
		fmt.Fprintf(os.Stderr, "warning: git scaffold: %v\n", err)
	}

	fmt.Printf("Initialized empty pit repository in %s\n", repo.DirName)
	fmt.Printf("Add your first prompt:  pit add \"describe what to build\"\n")
	return nil
}

// scaffoldGit initialises a git repo (if not already one), writes a README and
// .gitignore, and makes an initial commit.
func scaffoldGit(dir, outDir string) error {
	// Only init if not already inside a git repo.
	check := exec.Command("git", "rev-parse", "--git-dir")
	check.Dir = dir
	if err := check.Run(); err != nil {
		init_ := exec.Command("git", "init", "-q")
		init_.Dir = dir
		init_.Stderr = os.Stderr
		if err := init_.Run(); err != nil {
			return fmt.Errorf("git init: %w", err)
		}
	}

	projectName := filepath.Base(dir)

	readmePath := filepath.Join(dir, "README.md")
	if _, err := os.Stat(readmePath); os.IsNotExist(err) {
		readme := fmt.Sprintf("# %s\n\n> Describe what this project does.\n\nBuilt with [pit](https://github.com/slopstack-labs/pit) — prompts are the source of truth.\n", projectName)
		if err := os.WriteFile(readmePath, []byte(readme), 0o644); err != nil {
			return err
		}
	}

	ignorePath := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(ignorePath); os.IsNotExist(err) {
		if outDir == "" {
			outDir = repo.DefaultOutDir
		}
		gitignore := fmt.Sprintf("%s/\n", outDir)
		if err := os.WriteFile(ignorePath, []byte(gitignore), 0o644); err != nil {
			return err
		}
	}

	add := exec.Command("git", "add", repo.DirName, "README.md", ".gitignore")
	add.Dir = dir
	add.Stderr = os.Stderr
	if err := add.Run(); err != nil {
		return fmt.Errorf("git add: %w", err)
	}

	commit := exec.Command("git", "commit", "-q", "-m", "pit init")
	commit.Dir = dir
	commit.Stderr = os.Stderr
	if err := commit.Run(); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}

	return nil
}

func cmdAdd(args []string) error {
	r, err := repo.Find(".")
	if err != nil {
		return err
	}

	text, err := readPromptText(args)
	if err != nil {
		return err
	}

	p, err := r.AddPrompt(text)
	if err != nil {
		return err
	}
	fmt.Printf("Added prompt #%d: %s\n", p.Seq, p.Title())
	return nil
}

// readPromptText resolves prompt text from -f FILE, "-" (stdin), or args.
func readPromptText(args []string) (string, error) {
	if len(args) >= 2 && args[0] == "-f" {
		data, err := os.ReadFile(args[1])
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	if len(args) == 1 && args[0] == "-" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	if len(args) == 0 {
		return "", fmt.Errorf("nothing to add; provide a prompt, -f FILE, or -")
	}
	return strings.Join(args, " "), nil
}

func cmdLog() error {
	r, err := repo.Find(".")
	if err != nil {
		return err
	}
	prompts, err := r.Prompts()
	if err != nil {
		return err
	}
	if len(prompts) == 0 {
		fmt.Println("No prompts yet. Add one with: pit add \"...\"")
		return nil
	}
	for _, p := range prompts {
		fmt.Printf("#%-3d  %s\n", p.Seq, p.Title())
	}
	return nil
}

func cmdShow(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: pit show <n>")
	}
	n, err := strconv.Atoi(args[0])
	if err != nil {
		return fmt.Errorf("invalid prompt number %q", args[0])
	}
	r, err := repo.Find(".")
	if err != nil {
		return err
	}
	prompts, err := r.Prompts()
	if err != nil {
		return err
	}
	for _, p := range prompts {
		if p.Seq == n {
			fmt.Println(p.Text)
			return nil
		}
	}
	return fmt.Errorf("no prompt #%d", n)
}

func cmdConfig(args []string) error {
	r, err := repo.Find(".")
	if err != nil {
		return err
	}
	cfg, err := r.Config()
	if err != nil {
		return err
	}

	if len(args) == 0 {
		fmt.Printf("backend: %s\n", cfg.Backend)
		fmt.Printf("model:   %s\n", cfg.Model)
		fmt.Printf("out_dir: %s\n", cfg.OutDir)
		return nil
	}

	changed := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--backend", "-b":
			if i+1 >= len(args) {
				return fmt.Errorf("--backend requires a value")
			}
			cfg.Backend = args[i+1]
			i++
			changed = true
		case "--model", "-m":
			if i+1 >= len(args) {
				return fmt.Errorf("--model requires a value")
			}
			cfg.Model = args[i+1]
			i++
			changed = true
		case "--out", "-o":
			if i+1 >= len(args) {
				return fmt.Errorf("--out requires a directory")
			}
			cfg.OutDir = args[i+1]
			i++
			changed = true
		default:
			return fmt.Errorf("unknown flag %q", args[i])
		}
	}

	if changed {
		if err := r.SetConfig(cfg); err != nil {
			return err
		}
		fmt.Printf("backend: %s\n", cfg.Backend)
		fmt.Printf("model:   %s\n", cfg.Model)
		fmt.Printf("out_dir: %s\n", cfg.OutDir)
	}
	return nil
}

func cmdBuild(args []string) error {
	r, err := repo.Find(".")
	if err != nil {
		return err
	}
	cfg, err := r.Config()
	if err != nil {
		return err
	}

	outDir := cfg.OutDir
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--out", "-o":
			if i+1 >= len(args) {
				return fmt.Errorf("--out requires a directory")
			}
			outDir = args[i+1]
			i++
		case "--model", "-m":
			if i+1 >= len(args) {
				return fmt.Errorf("--model requires a value")
			}
			cfg.Model = args[i+1]
			i++
		case "--backend", "-b":
			if i+1 >= len(args) {
				return fmt.Errorf("--backend requires a value")
			}
			cfg.Backend = args[i+1]
			i++
		default:
			return fmt.Errorf("unknown flag %q", args[i])
		}
	}

	prompts, err := r.Prompts()
	if err != nil {
		return err
	}
	if len(prompts) == 0 {
		return fmt.Errorf("no prompts to build; add one with: pit add \"...\"")
	}

	backend, err := builder.NewBackend(cfg)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	fmt.Fprintf(os.Stderr, "Building %d prompt(s) with backend %q\n", len(prompts), cfg.Backend)
	progress := func(format string, a ...any) {
		fmt.Fprintf(os.Stderr, "  "+format+"\n", a...)
	}

	dest, n, err := builder.Run(ctx, backend, prompts, r.Root, outDir, progress)
	if err != nil {
		return err
	}
	fmt.Printf("Built %d file(s) into %s\n", n, dest)
	return nil
}
