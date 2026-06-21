package builder

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"pit/internal/repo"
)

const defaultOllamaHost = "http://localhost:11434"

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
	Type     string           `json:"type"`
	Function ollamaToolFunc   `json:"function"`
}

type ollamaToolFunc struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type ollamaMessage struct {
	Role       string            `json:"role"`
	Content    string            `json:"content,omitempty"`
	ToolCalls  []ollamaToolCall  `json:"tool_calls,omitempty"`
	ToolCallID string            `json:"tool_call_id,omitempty"`
}

type ollamaToolCall struct {
	ID       string              `json:"id"`
	Type     string              `json:"type"`
	Function ollamaToolCallFunc  `json:"function"`
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
		{Role: "system", Content: apiSystemPrompt},
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
			return nil
		}

		for _, tc := range choice.Message.ToolCalls {
			out, isErr := b.applyTool(tc.Function.Name, tc.Function.Arguments)
			role := "tool"
			content := out
			if isErr {
				content = "error: " + out
			}
			messages = append(messages, ollamaMessage{
				Role:       role,
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

func (b *ollamaBackend) Flush(dir string) error {
	// Reuse apiBackend's flush logic via a temporary instance.
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
	// Delegate to the same logic used by apiBackend via a temporary wrapper.
	tmp := &apiBackend{files: b.files}
	return tmp.applyTool(name, rawInput)
}

func (b *ollamaBackend) promptMessage(p repo.Prompt) string {
	tmp := &apiBackend{files: b.files}
	return tmp.promptMessage(p)
}
