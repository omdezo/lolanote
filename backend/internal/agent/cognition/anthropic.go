package cognition

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// DefaultModel is the shipped default. Claude Opus 5 is the current Opus tier:
// 1M context, 128K max output, thinking on by default. A deployment may point
// AGENT_MODEL at a cheaper tier, but that is the operator's decision to make,
// not a default this code quietly imposes.
const DefaultModel = "claude-opus-5"

// Anthropic is the production Provider, built on the official Go SDK.
type Anthropic struct {
	client anthropic.Client
	model  string
}

var _ Provider = (*Anthropic)(nil)

// NewAnthropic constructs the provider. An empty apiKey is a configuration
// error rather than a lazy failure at first use.
func NewAnthropic(apiKey, model string) (*Anthropic, error) {
	if apiKey == "" {
		return nil, ErrNotConfigured
	}
	if model == "" {
		model = DefaultModel
	}
	return &Anthropic{client: anthropic.NewClient(option.WithAPIKey(apiKey)), model: model}, nil
}

// Name implements Provider.
func (a *Anthropic) Name() string { return "anthropic" }

// Model implements Provider.
func (a *Anthropic) Model() string { return a.model }

// Complete runs one turn of the tool-use conversation.
func (a *Anthropic) Complete(ctx context.Context, req Request) (*Response, error) {
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 8192
	}

	tools := make([]anthropic.ToolUnionParam, 0, len(req.Tools))
	for i := range req.Tools {
		t := req.Tools[i]
		properties, _ := t.Schema["properties"]
		required, _ := t.Schema["required"].([]string)
		tool := anthropic.ToolParam{
			Name:        t.Name,
			Description: anthropic.String(t.Description),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: properties,
				Required:   required,
				// Strict mode guarantees the emitted input validates, and
				// requires additionalProperties:false alongside the required
				// list. Only applied to a forced call: in an agentic loop the
				// model must stay free to choose among tools.
				ExtraFields: map[string]any{"additionalProperties": false},
			},
		}
		if req.ForceTool == t.Name {
			tool.Strict = anthropic.Bool(true)
		}
		tools = append(tools, anthropic.ToolUnionParam{OfTool: &tool})
	}

	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(a.model),
		MaxTokens: int64(maxTokens),
		System: []anthropic.TextBlockParam{{
			Text: req.System,
			// One breakpoint caches the tool schemas and the instructions
			// together — tools render before system, so this covers both.
			CacheControl: anthropic.NewCacheControlEphemeralParam(),
		}},
		Messages: anthropicMessages(req.Messages),
		Tools:    tools,
	}
	if req.ForceTool != "" {
		params.ToolChoice = anthropic.ToolChoiceParamOfTool(req.ForceTool)
	}

	resp, err := a.client.Messages.New(ctx, params)
	if err != nil {
		return nil, classifyAnthropic(err)
	}

	usage := Usage{
		InputTokens:  int(resp.Usage.InputTokens),
		OutputTokens: int(resp.Usage.OutputTokens),
		CachedTokens: int(resp.Usage.CacheReadInputTokens),
	}
	if cost, ok := CostUSD(a.model, usage); ok {
		usage.CostUSD = cost
	}

	// A refusal is a successful HTTP response with an empty or partial body.
	// Checking stop_reason BEFORE touching content is the difference between a
	// clean terminal state and an index-out-of-range panic.
	if resp.StopReason == anthropic.StopReasonRefusal {
		return &Response{StopReason: string(resp.StopReason), Usage: usage}, ErrRefused
	}

	out := &Response{StopReason: string(resp.StopReason), Usage: usage}
	for _, block := range resp.Content {
		switch v := block.AsAny().(type) {
		case anthropic.TextBlock:
			out.Text += v.Text
		case anthropic.ToolUseBlock:
			out.Calls = append(out.Calls, ToolCall{ID: v.ID, Name: v.Name, Input: json.RawMessage(v.Input)})
		}
	}
	if req.ForceTool != "" && len(out.Calls) == 0 {
		return out, ErrNoOutput
	}
	return out, nil
}

// anthropicMessages converts the harness's history into SDK content blocks.
func anthropicMessages(msgs []Message) []anthropic.MessageParam {
	out := make([]anthropic.MessageParam, 0, len(msgs))
	for _, m := range msgs {
		blocks := make([]anthropic.ContentBlockParamUnion, 0, 4)
		if m.Text != "" {
			blocks = append(blocks, anthropic.NewTextBlock(m.Text))
		}
		for _, c := range m.Calls {
			blocks = append(blocks, anthropic.ContentBlockParamUnion{
				OfToolUse: &anthropic.ToolUseBlockParam{ID: c.ID, Name: c.Name, Input: c.Input},
			})
		}
		// Every result for one assistant turn must ride in a SINGLE user
		// message; splitting them trains the model to stop calling tools in
		// parallel.
		for _, r := range m.Outcomes {
			blocks = append(blocks, anthropic.NewToolResultBlock(r.CallID, r.Content, r.IsError))
		}
		for _, img := range m.Images {
			blocks = append(blocks, anthropic.NewImageBlockBase64(
				img.MediaType, base64.StdEncoding.EncodeToString(img.Data)))
		}
		if len(blocks) == 0 {
			continue
		}
		if m.Role == RoleAssistant {
			out = append(out, anthropic.MessageParam{Role: anthropic.MessageParamRoleAssistant, Content: blocks})
		} else {
			out = append(out, anthropic.NewUserMessage(blocks...))
		}
	}
	return out
}

// classifyAnthropic maps a provider error onto a harness sentinel. Retryable
// capacity failures become ErrUnavailable; everything else surfaces with its
// status attached so the journal records why a run failed.
func classifyAnthropic(err error) error {
	var apiErr *anthropic.Error
	if !errors.As(err, &apiErr) {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	switch apiErr.StatusCode {
	case 429, 500, 502, 503, 504, 529:
		return fmt.Errorf("%w: provider returned %d", ErrUnavailable, apiErr.StatusCode)
	default:
		return fmt.Errorf("model call failed (%d): %w", apiErr.StatusCode, err)
	}
}
