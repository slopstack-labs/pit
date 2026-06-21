package builder

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"pit/internal/repo"
)

const defaultOllamaHost = "http://localhost:11434"

// ollamaSystemPrompt is more explicit than the shared apiSystemPrompt because
// smaller local models frequently ignore tool definitions and respond with plain
// text. We spell out the tool-use requirement in direct, unambiguous language.
const ollamaSystemPrompt = `You are pit's build engine. A software project is defined entirely by an ordered sequence of natural-language prompts. You are given the current state of the project's files followed by the next prompt in the sequence.

You MUST apply the prompt's intent using ONLY the write_file and delete_file tool calls. You MUST NOT write file contents in your text response — every file MUST be created or updated via a write_file tool call.

Rules:
- ALWAYS call write_file to create or update files. Never output file contents as prose or code blocks.
- write_file always takes the COMPLETE new contents of a file, never a diff or partial snippet.
- Make the changes the prompt asks for and nothing more; preserve unrelated existing files.
- Do not ask questions or wait for confirmation; act on a reasonable interpretation.
- Produce real, working, runnable code — no placeholders or "TODO" stubs unless the prompt explicitly asks for them.
- When the prompt has been fully applied, stop without further tool calls.`

// ollamaBackend builds by calling a local Ollama server via its OpenAI-compatible
// chat-completions endpoint with tool calling. It maintains the same in-memory VFS
// strategy as apiBackend: files accumulate across Apply calls and are flushed once.
type ollamaBackend struct {
	host  string
	model string
	files vfs
}

func newOllamaBackend(host, model string) *ollamaBackend {
	if host == "" {
		host = os.Getenv("OLLAMA_HOST")
	}
	if host == "" {
		host = defaultOllamaHost
	}
	if model == "" {
		model = repo.DefaultOllamaModel
	}
	return &ollamaBackend{host: host, model: model, files: vfs{}}
}

// OpenAI-compatible request/response types (minimal, enough for tool calling).

type ollamaTool struct {
	Type     string         `json:"type"`
	Function ollamaToolFunc `json:"function"`
}

type ollamaToolFunc struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type ollamaMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content,omitempty"`
	ToolCalls  []ollamaToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type ollamaToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function ollamaToolCallFunc `json:"function"`
}

type ollamaToolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ollamaChatRequest struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Tools    []ollamaTool    `json:"tools,omitempty"`
	Stream   bool            `json:"stream"`
}

type ollamaChatResponse struct {
	Choices []struct {
		Message      ollamaMessage `json:"message"`
		FinishReason string        `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

var ollamaTools = []ollamaTool{
	{
		Type: "function",
		Function: ollamaToolFunc{
			Name:        "write_file",
			Description: "Create or overwrite a file with its complete contents.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":    map[string]any{"type": "string", "description": "Project-relative file path, e.g. src/main.go"},
					"content": map[string]any{"type": "string", "description": "The full contents of the file"},
				},
				"required": []string{"path", "content"},
			},
		},
	},
	{
		Type: "function",
		Function: ollamaToolFunc{
			Name:        "delete_file",
			Description: "Delete a file from the project.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string", "description": "Project-relative file path to remove"},
				},
				"required": []string{"path"},
			},
		},
	},
}

func (b *ollamaBackend) Apply(ctx context.Context, _ string, p repo.Prompt, _ Progress) error {
	messages := []ollamaMessage{
		{Role: "system", Content: ollamaSystemPrompt},
		{Role: "user", Content: b.promptMessage(p)},
	}

	for turn := 0; turn < maxToolTurns; turn++ {
		resp, err := b.chat(ctx, messages)
		if err != nil {
			return err
		}
		if len(resp.Choices) == 0 {
			return fmt.Errorf("ollama returned empty choices")
		}
		choice := resp.Choices[0]
		messages = append(messages, choice.Message)

		if len(choice.Message.ToolCalls) == 0 {
			// The model responded with text instead of tool calls — a common failure
			// mode for smaller local models. Fall back to parsing code blocks from
			// the text response so the build still produces files.
			b.parseTextFallback(choice.Message.Content)
			return nil
		}

		for _, tc := range choice.Message.ToolCalls {
			out, isErr := b.applyTool(tc.Function.Name, tc.Function.Arguments)
			content := out
			if isErr {
				content = "error: " + out
			}
			messages = append(messages, ollamaMessage{
				Role:       "tool",
				Content:    content,
				ToolCallID: tc.ID,
			})
		}

		if choice.FinishReason != "tool_calls" && choice.FinishReason != "" {
			return nil
		}
	}
	return fmt.Errorf("exceeded %d tool turns", maxToolTurns)
}

// parseTextFallback extracts code blocks from a plain-text model response and
// writes them into the in-memory VFS. It handles the two most common patterns
// small models use when they ignore tool definitions:
//
//   - Filename after the language tag:  ```python main.py
//   - Filename in a comment first line: ```python\n# main.py\n...
//   - Language tag only, filename inferred: ```python → main.py
func (b *ollamaBackend) parseTextFallback(text string) {
	lines := strings.Split(text, "\n")
	inBlock := false
	var lang, filename string
	var blockLines []string

	for _, line := range lines {
		if !inBlock {
			if strings.HasPrefix(line, "```") {
				inBlock = true
				header := strings.TrimSpace(strings.TrimPrefix(line, "```"))
				lang, filename = parseFenceHeader(header)
				blockLines = nil
			}
			continue
		}
		if strings.TrimSpace(line) == "```" {
			inBlock = false
			if len(blockLines) > 0 {
				name, content := resolveBlock(blockLines, lang, filename)
				if name != "" {
					if clean, err := cleanPath(name); err == nil {
						b.files[clean] = content
					}
				}
			}
			filename = ""
			continue
		}
		blockLines = append(blockLines, line)
	}
}

// parseFenceHeader splits a code fence header into language and optional filename.
// E.g. "python main.py" → ("python", "main.py"); "go" → ("go", "").
func parseFenceHeader(header string) (lang, filename string) {
	parts := strings.Fields(header)
	if len(parts) == 0 {
		return "", ""
	}
	lang = parts[0]
	if len(parts) >= 2 {
		filename = parts[1]
	}
	return lang, filename
}

// resolveBlock determines the filename and content for a parsed code block.
// Priority: explicit filename from fence header > first-line comment > language inference.
func resolveBlock(lines []string, lang, filename string) (name, content string) {
	if filename != "" {
		return filename, strings.Join(lines, "\n")
	}
	// Check if the first line is a comment containing a filename.
	if len(lines) > 0 {
		first := strings.TrimSpace(lines[0])
		for _, prefix := range []string{"# ", "// ", "-- ", "<!-- "} {
			if strings.HasPrefix(first, prefix) {
				candidate := strings.TrimSpace(strings.TrimPrefix(first, prefix))
				candidate = strings.TrimSuffix(candidate, " -->")
				if strings.Contains(candidate, ".") && !strings.ContainsAny(candidate, " /\\") {
					return candidate, strings.Join(lines[1:], "\n")
				}
			}
		}
	}
	// Infer a default filename from the language tag.
	if name := inferFilename(lang); name != "" {
		return name, strings.Join(lines, "\n")
	}
	return "", ""
}

var langToFilename = map[string]string{
	"python":     "main.py",
	"py":         "main.py",
	"javascript": "main.js",
	"js":         "main.js",
	"typescript": "main.ts",
	"ts":         "main.ts",
	"go":         "main.go",
	"golang":     "main.go",
	"bash":       "main.sh",
	"sh":         "main.sh",
	"html":       "index.html",
	"css":        "style.css",
	"java":       "Main.java",
	"c":          "main.c",
	"cpp":        "main.cpp",
	"rust":       "main.rs",
	"ruby":       "main.rb",
}

func inferFilename(lang string) string {
	return langToFilename[strings.ToLower(lang)]
}

func (b *ollamaBackend) Flush(dir string) error {
	tmp := &apiBackend{files: b.files}
	return tmp.Flush(dir)
}

func (b *ollamaBackend) chat(ctx context.Context, messages []ollamaMessage) (*ollamaChatResponse, error) {
	body, err := json.Marshal(ollamaChatRequest{
		Model:    b.model,
		Messages: messages,
		Tools:    ollamaTools,
		Stream:   false,
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.host+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	httpResp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama request failed: %w", err)
	}
	defer httpResp.Body.Close()

	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, err
	}

	var resp ollamaChatResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("ollama response parse error: %w (body: %s)", err, data)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("ollama error: %s", resp.Error.Message)
	}
	if httpResp.StatusCode >= 400 {
		return nil, fmt.Errorf("ollama HTTP %d: %s", httpResp.StatusCode, data)
	}
	return &resp, nil
}

func (b *ollamaBackend) applyTool(name, rawInput string) (string, bool) {
	tmp := &apiBackend{files: b.files}
	return tmp.applyTool(name, rawInput)
}

func (b *ollamaBackend) promptMessage(p repo.Prompt) string {
	tmp := &apiBackend{files: b.files}
	return tmp.promptMessage(p)
}
