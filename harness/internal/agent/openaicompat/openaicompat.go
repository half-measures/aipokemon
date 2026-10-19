// Package openaicompat is a brain backed by any server that speaks the
// OpenAI chat-completions API: LM Studio, Ollama, llama.cpp, vLLM.
//
// It uses plain net/http rather than an SDK, so the whole protocol is
// visible here: one POST per step, tools described as JSON Schema, and the
// model's answer coming back as a tool call.
package openaicompat

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"harness/internal/agent"
)

const systemPrompt = `You are playing Pokemon Red on a Game Boy. Your goal is to make progress in the game: get a starter Pokemon, win battles, reach new towns, and earn gym badges.

Each turn you get the game state read from memory, the text on screen, a small map of what is around you, and your recent actions. Call the press tool with the buttons to press next.

Controls: up/down/left/right move one square or move a menu cursor. a confirms, talks to people, and advances dialog. b cancels or backs out. start opens the menu.

Tips:
- If on-screen text is showing, press a to advance it.
- On the map, @ is you, N is a person or object, . is walkable, # is blocked. Up is north. Doors and stairs usually show as # until you step onto them; walk into them.
- If your position has not changed for several turns, you are stuck: try a different direction or press b.
- Batch your moves: when the map shows a clear path, send all of them in one call, e.g. ["down","down","down","left","left"] (up to 8). Use single presses only in menus.
- Keep reasoning to one short sentence. Think briefly; every turn costs time.`

type Config struct {
	BaseURL string // e.g. http://localhost:1234/v1 (LM Studio) or http://localhost:11434/v1 (Ollama)
	Model   string
	APIKey  string // local servers ignore it
	Vision  bool   // send the screenshot as well as the text state
	// NoThink asks the server to skip the model's thinking step. llama.cpp
	// and vLLM honour chat_template_kwargs; LM Studio ignores it for Qwen3.5,
	// so there the prompt template has to be edited instead (see README).
	NoThink bool
}

type Agent struct {
	cfg Config
	hc  *http.Client

	warnedThinking bool
}

func New(cfg Config) *Agent {
	// Local models can be slow, especially the first call while weights load.
	return &Agent{cfg: cfg, hc: &http.Client{Timeout: 5 * time.Minute}}
}

func (a *Agent) Name() string {
	if a.cfg.Vision {
		return a.cfg.Model + " (vision)"
	}
	return a.cfg.Model + " (text)"
}

// Wire types: just the parts of the chat-completions schema we use.
type (
	message struct {
		Role    string `json:"role"`
		Content any    `json:"content"` // string, or []contentPart for text + image
	}
	contentPart struct {
		Type     string    `json:"type"` // "text" or "image_url"
		Text     string    `json:"text,omitempty"`
		ImageURL *imageURL `json:"image_url,omitempty"`
	}
	imageURL struct {
		URL string `json:"url"`
	}
	tool struct {
		Type     string       `json:"type"`
		Function functionDecl `json:"function"`
	}
	functionDecl struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	}
	request struct {
		Model       string    `json:"model"`
		Messages    []message `json:"messages"`
		Tools       []tool    `json:"tools"`
		Temperature float64   `json:"temperature"`
		MaxTokens   int       `json:"max_tokens"`
		// ChatTemplateKwargs reach the model's chat template; Qwen reads enable_thinking.
		ChatTemplateKwargs map[string]any `json:"chat_template_kwargs,omitempty"`
	}
	toolCall struct {
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"` // a JSON string, not an object
		} `json:"function"`
	}
	response struct {
		Choices []struct {
			Message struct {
				Content          string     `json:"content"`
				ReasoningContent string     `json:"reasoning_content"` // thinking models, on some servers
				Reasoning        string     `json:"reasoning"`         // the same, on others
				ToolCalls        []toolCall `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
)

var buttonsEnum = []string{"a", "b", "start", "select", "up", "down", "left", "right"}

var pressTool = tool{Type: "function", Function: functionDecl{
	Name:        "press",
	Description: "Press Game Boy buttons one after another.",
	Parameters: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"buttons": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string", "enum": buttonsEnum},
				"minItems":    1,
				"maxItems":    8,
				"description": "Buttons to press in order, e.g. [\"up\",\"up\",\"a\"].",
			},
			"reasoning": map[string]any{
				"type":        "string",
				"description": "One short sentence: what you see and why these buttons.",
			},
		},
		"required": []string{"buttons"},
	},
}}

func (a *Agent) Decide(ctx context.Context, obs agent.Observation) (agent.Action, error) {
	user := userText(obs)
	var content any = user
	if a.cfg.Vision {
		img, err := upscalePNG(obs.Screen, 3)
		if err != nil {
			return agent.Action{}, err
		}
		content = []contentPart{
			{Type: "text", Text: user},
			{Type: "image_url", ImageURL: &imageURL{URL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(img)}},
		}
	}

	req := request{
		Model: a.cfg.Model,
		Messages: []message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: content},
		},
		Tools:       []tool{pressTool},
		Temperature: 0.4,
		MaxTokens:   2048, // room for thinking models to think before the call
	}
	if a.cfg.NoThink {
		req.ChatTemplateKwargs = map[string]any{"enable_thinking": false}
	}
	resp, err := a.post(ctx, req)
	if err != nil {
		return agent.Action{}, err
	}
	if len(resp.Choices) == 0 {
		return agent.Action{}, errors.New("model returned no choices")
	}
	msg := resp.Choices[0].Message
	if a.cfg.NoThink && !a.warnedThinking && (msg.ReasoningContent != "" || msg.Reasoning != "" || strings.Contains(msg.Content, "</think>")) {
		a.warnedThinking = true
		slog.Warn("thinking is still on: the model server ignored the request to turn it off. " +
			"In LM Studio, add {%- set enable_thinking = false %} as the first line of the model's Prompt Template and reload it.")
	}

	act, err := a.parse(msg.Content, msg.ToolCalls)
	act.Tokens = resp.Usage.CompletionTokens
	return act, err
}

func (a *Agent) parse(content string, calls []toolCall) (agent.Action, error) {
	// Preferred path: a native tool call.
	for _, tc := range calls {
		if tc.Function.Name == "press" {
			return toAction(tc.Function.Arguments, content)
		}
	}
	// Fallback: small models often write the JSON in plain text instead.
	if js := lastJSONObject(content); js != "" {
		return toAction(js, content)
	}
	return agent.Action{}, fmt.Errorf("model did not call press; it said: %.200q", content)
}

func userText(obs agent.Observation) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Turn %d.\n\n%s\n", obs.Step, obs.StateText)
	if len(obs.Recent) > 0 {
		sb.WriteString("\nYour recent actions, oldest first:\n")
		for _, r := range obs.Recent {
			fmt.Fprintf(&sb, "- turn %d: %s %s", r.Step, r.Action.Tool, r.Action.Args)
			if r.Err != "" {
				fmt.Fprintf(&sb, " (failed: %s)", r.Err)
			}
			sb.WriteByte('\n')
		}
	}
	sb.WriteString("\nCall press with your next buttons.")
	return sb.String()
}

// toAction validates the model's arguments before they reach the emulator.
func toAction(rawArgs, content string) (agent.Action, error) {
	var args struct {
		Buttons   []string `json:"buttons"`
		Reasoning string   `json:"reasoning"`
	}
	if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
		return agent.Action{}, fmt.Errorf("bad press arguments %q: %w", rawArgs, err)
	}
	if len(args.Buttons) == 0 || len(args.Buttons) > 8 {
		return agent.Action{}, fmt.Errorf("press needs 1 to 8 buttons, got %d", len(args.Buttons))
	}
	for i, b := range args.Buttons {
		b = strings.ToLower(strings.TrimSpace(b))
		if !slices.Contains(buttonsEnum, b) {
			return agent.Action{}, fmt.Errorf("unknown button %q", b)
		}
		args.Buttons[i] = b
	}
	reasoning := args.Reasoning
	if reasoning == "" {
		reasoning = strings.TrimSpace(content)
	}
	out, _ := json.Marshal(agent.PressArgs{Buttons: args.Buttons})
	return agent.Action{Tool: "press", Args: out, Reasoning: reasoning}, nil
}

func (a *Agent) post(ctx context.Context, req request) (response, error) {
	var out response
	body, err := json.Marshal(req)
	if err != nil {
		return out, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(a.cfg.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return out, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	if a.cfg.APIKey != "" {
		hreq.Header.Set("Authorization", "Bearer "+a.cfg.APIKey)
	}
	resp, err := a.hc.Do(hreq)
	if err != nil {
		return out, fmt.Errorf("model server unreachable (is LM Studio's server started?): %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("model server returned %d, not JSON: %.300s", resp.StatusCode, raw)
	}
	if out.Error != nil {
		return out, fmt.Errorf("model server: %s", out.Error.Message)
	}
	if resp.StatusCode/100 != 2 {
		return out, fmt.Errorf("model server returned %d: %.300s", resp.StatusCode, raw)
	}
	return out, nil
}

// lastJSONObject finds the last {...} block in free text, skipping any
// thinking the model wrote before it.
func lastJSONObject(s string) string {
	end := strings.LastIndex(s, "}")
	for start := strings.LastIndex(s[:max(end, 0)], "{"); start >= 0 && end > start; start = strings.LastIndex(s[:start], "{") {
		if cand := s[start : end+1]; json.Valid([]byte(cand)) {
			return cand
		}
	}
	return ""
}

// upscalePNG enlarges the 160x144 frame with nearest-neighbour scaling;
// vision encoders see tiny images poorly.
func upscalePNG(pngBytes []byte, k int) ([]byte, error) {
	src, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx()*k, b.Dy()*k))
	for y := range b.Dy() * k {
		for x := range b.Dx() * k {
			dst.Set(x, y, src.At(b.Min.X+x/k, b.Min.Y+y/k))
		}
	}
	var buf bytes.Buffer
	err = png.Encode(&buf, dst)
	return buf.Bytes(), err
}
