package builder

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"

	"pit/internal/repo"
)

// maxToolTurns caps the tool-use loop per prompt so a misbehaving turn can't
// spin forever.
const maxToolTurns = 60

const apiSystemPrompt = `You are pit's build engine. A software project is defined entirely by an ordered sequence of natural-language prompts. You are given the current state of the project's files followed by the next prompt in the sequence.

Apply ONLY the intent of that one prompt by creating, modifying, or deleting files using the write_file and delete_file tools. Rules:
- Make the changes the prompt asks for and nothing more; preserve unrelated existing files.
- write_file always takes the COMPLETE new contents of a file, never a diff or partial snippet.
- Do not ask questions or wait for confirmation; act on a reasonable interpretation.
- Produce real, working, runnable code — no placeholders or "TODO" stubs unless the prompt explicitly asks for them.
- When the prompt has been fully applied, stop without further tool calls.`

// vfs is an in-memory project: a map of relative path to file contents.
type vfs map[string]string

// apiBackend builds by calling the Anthropic API and applying the model's
// write_file/delete_file tool calls to an in-memory project carried across
// prompts, flushed to disk once at the end.
type apiBackend struct {
	model  string
	client anthropic.Client
	files  vfs
}

func newAPIBackend(model string) (*apiBackend, error) {
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		return nil, fmt.Errorf("ANTHROPIC_API_KEY is not set (or choose a different backend, e.g. claude-code)")
	}
	return &apiBackend{model: model, client: anthropic.NewClient(), files: vfs{}}, nil
}

func (b *apiBackend) Apply(ctx context.Context, _ string, p repo.Prompt, _ Progress) error {
	tools := []anthropic.ToolUnionParam{
		toolUnion("write_file", "Create or overwrite a file with its complete contents.", map[string]any{
			"path":    map[string]any{"type": "string", "description": "Project-relative file path, e.g. src/main.go"},
			"content": map[string]any{"type": "string", "description": "The full contents of the file"},
		}, "path", "content"),
		toolUnion("delete_file", "Delete a file from the project.", map[string]any{
			"path": map[string]any{"type": "string", "description": "Project-relative file path to remove"},
		}, "path"),
	}

	adaptive := anthropic.ThinkingConfigAdaptiveParam{}
	messages := []anthropic.MessageParam{
		anthropic.NewUserMessage(anthropic.NewTextBlock(b.promptMessage(p))),
	}

	for turn := 0; turn < maxToolTurns; turn++ {
		resp, err := b.client.Messages.New(ctx, anthropic.MessageNewParams{
			Model:     anthropic.Model(b.model),
			MaxTokens: 16000,
			System:    []anthropic.TextBlockParam{{Text: apiSystemPrompt}},
			Tools:     tools,
			Thinking:  anthropic.ThinkingConfigParamUnion{OfAdaptive: &adaptive},
			Messages:  messages,
		})
		if err != nil {
			return err
		}
		messages = append(messages, resp.ToParam())

		var results []anthropic.ContentBlockParamUnion
		for _, block := range resp.Content {
			tu, ok := block.AsAny().(anthropic.ToolUseBlock)
			if !ok {
				continue
			}
			out, isErr := b.applyTool(tu.Name, tu.JSON.Input.Raw())
			results = append(results, anthropic.NewToolResultBlock(tu.ID, out, isErr))
		}

		if resp.StopReason != anthropic.StopReasonToolUse {
			return nil
		}
		if len(results) == 0 {
			return nil
		}
		messages = append(messages, anthropic.NewUserMessage(results...))
	}
	return fmt.Errorf("exceeded %d tool turns", maxToolTurns)
}

// Flush writes the accumulated project to disk.
func (b *apiBackend) Flush(dir string) error {
	for _, path := range sortedPaths(b.files) {
		dest := filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dest, []byte(b.files[path]), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func (b *apiBackend) applyTool(name, rawInput string) (string, bool) {
	switch name {
	case "write_file":
		var in struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal([]byte(rawInput), &in); err != nil {
			return "invalid input: " + err.Error(), true
		}
		clean, err := cleanPath(in.Path)
		if err != nil {
			return err.Error(), true
		}
		b.files[clean] = in.Content
		return "wrote " + clean, false

	case "delete_file":
		var in struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal([]byte(rawInput), &in); err != nil {
			return "invalid input: " + err.Error(), true
		}
		clean, err := cleanPath(in.Path)
		if err != nil {
			return err.Error(), true
		}
		if _, ok := b.files[clean]; !ok {
			return "no such file: " + clean, true
		}
		delete(b.files, clean)
		return "deleted " + clean, false

	default:
		return "unknown tool: " + name, true
	}
}

func (b *apiBackend) promptMessage(p repo.Prompt) string {
	var sb strings.Builder
	sb.WriteString("Current project files:\n\n")
	if len(b.files) == 0 {
		sb.WriteString("(the project is empty)\n")
	} else {
		for _, path := range sortedPaths(b.files) {
			fmt.Fprintf(&sb, "=== %s ===\n%s\n\n", path, b.files[path])
		}
	}
	fmt.Fprintf(&sb, "Next prompt (#%d):\n%s", p.Seq, p.Text)
	return sb.String()
}

func sortedPaths(v vfs) []string {
	paths := make([]string, 0, len(v))
	for path := range v {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

// cleanPath normalizes a model-supplied path and rejects anything that would
// escape the project root.
func cleanPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", fmt.Errorf("empty path")
	}
	if filepath.IsAbs(p) {
		return "", fmt.Errorf("absolute paths are not allowed: %s", p)
	}
	clean := filepath.ToSlash(filepath.Clean(p))
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("path escapes project root: %s", p)
	}
	if clean == "." || clean == repo.DirName || strings.HasPrefix(clean, repo.DirName+"/") {
		return "", fmt.Errorf("path is not a valid project file: %s", p)
	}
	return clean, nil
}

func toolUnion(name, desc string, props map[string]any, required ...string) anthropic.ToolUnionParam {
	tool := anthropic.ToolParam{
		Name:        name,
		Description: anthropic.String(desc),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: props,
			Required:   required,
		},
	}
	return anthropic.ToolUnionParam{OfTool: &tool}
}
