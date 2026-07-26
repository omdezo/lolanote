package cognition

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Gemini is a Provider backed by Google's Generative Language API.
//
// It exists to prove the point of the cognition plane: the harness depends on
// the Provider interface, not on a vendor. Everything that decides whether a
// run is correct — scope containment, id validation, plan compilation,
// verification, revert — is identical whichever implementation answers.
//
// Raw HTTP rather than an SDK: the surface used here is one endpoint, and a
// second vendor SDK would be a large dependency for a small, stable contract.
type Gemini struct {
	apiKey string
	model  string
	http   *http.Client
}

var _ Provider = (*Gemini)(nil)

// DefaultGeminiModel is the starting point. If the account does not serve it,
// the provider discovers a model that works rather than failing on a name this
// code guessed — see resolveModel.
const DefaultGeminiModel = "gemini-2.5-flash"

const geminiBase = "https://generativelanguage.googleapis.com/v1beta"

// NewGemini constructs the provider.
func NewGemini(apiKey, model string) (*Gemini, error) {
	if apiKey == "" {
		return nil, ErrNotConfigured
	}
	if model == "" {
		model = DefaultGeminiModel
	}
	return &Gemini{apiKey: apiKey, model: model, http: &http.Client{Timeout: 3 * time.Minute}}, nil
}

// Name implements Provider.
func (g *Gemini) Name() string { return "gemini" }

// Model implements Provider.
func (g *Gemini) Model() string { return g.model }

// --- wire types -------------------------------------------------------------

type geminiFunctionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type geminiFunctionResponse struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}

type geminiPart struct {
	Text             string                  `json:"text,omitempty"`
	InlineData       *geminiInlineData       `json:"inlineData,omitempty"`
	FunctionCall     *geminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitempty"`
}

// geminiInlineData carries an image as base64, the same bytes Anthropic gets.
type geminiInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiTool struct {
	FunctionDeclarations []geminiFunctionDecl `json:"function_declarations"`
}

type geminiFunctionDecl struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
}

type geminiToolCfg struct {
	FunctionCallingConfig struct {
		Mode                 string   `json:"mode"`
		AllowedFunctionNames []string `json:"allowed_function_names,omitempty"`
	} `json:"function_calling_config"`
}

type geminiRequest struct {
	SystemInstruction *geminiContent  `json:"system_instruction,omitempty"`
	Contents          []geminiContent `json:"contents"`
	Tools             []geminiTool    `json:"tools,omitempty"`
	ToolConfig        *geminiToolCfg  `json:"tool_config,omitempty"`
	GenerationConfig  *struct {
		MaxOutputTokens int `json:"maxOutputTokens,omitempty"`
	} `json:"generationConfig,omitempty"`
}

type geminiResponse struct {
	Candidates []struct {
		Content      geminiContent `json:"content"`
		FinishReason string        `json:"finishReason"`
	} `json:"candidates"`
	PromptFeedback struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
	UsageMetadata struct {
		PromptTokenCount        int `json:"promptTokenCount"`
		CandidatesTokenCount    int `json:"candidatesTokenCount"`
		CachedContentTokenCount int `json:"cachedContentTokenCount"`
	} `json:"usageMetadata"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Complete runs one turn of the tool-use conversation.
func (g *Gemini) Complete(ctx context.Context, req Request) (*Response, error) {
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 8192
	}

	decls := make([]geminiFunctionDecl, 0, len(req.Tools))
	for _, t := range req.Tools {
		decls = append(decls, geminiFunctionDecl{
			Name: t.Name, Description: t.Description,
			// Gemini accepts an OpenAPI 3.0 subset, narrower than JSON Schema.
			// The tools are written once for the harness and adapted here,
			// rather than weakened for every provider.
			Parameters: geminiSchema(t.Schema),
		})
	}

	body := geminiRequest{
		SystemInstruction: &geminiContent{Parts: []geminiPart{{Text: req.System}}},
		Contents:          geminiContents(req.Messages),
	}
	body.GenerationConfig = &struct {
		MaxOutputTokens int `json:"maxOutputTokens,omitempty"`
	}{MaxOutputTokens: maxTokens}
	if len(decls) > 0 {
		body.Tools = []geminiTool{{FunctionDeclarations: decls}}
		cfg := &geminiToolCfg{}
		if req.ForceTool != "" {
			// ANY with one allowed name is Gemini's forced call.
			cfg.FunctionCallingConfig.Mode = "ANY"
			cfg.FunctionCallingConfig.AllowedFunctionNames = []string{req.ForceTool}
		} else {
			// AUTO leaves the model free to call a tool, answer, or stop —
			// which is what an agentic loop needs.
			cfg.FunctionCallingConfig.Mode = "AUTO"
		}
		body.ToolConfig = cfg
	}

	resp, usage, err := g.call(ctx, g.model, body)
	if err != nil {
		// A model name this build guessed may not be served by the caller's
		// account. Discover one that works rather than failing on the name.
		if isModelNotFound(err) {
			if alt, lerr := g.resolveModel(ctx); lerr == nil && alt != "" && alt != g.model {
				g.model = alt
				resp, usage, err = g.call(ctx, alt, body)
			}
		}
		if err != nil {
			return nil, err
		}
	}

	if resp.PromptFeedback.BlockReason != "" {
		return &Response{StopReason: resp.PromptFeedback.BlockReason, Usage: usage}, ErrRefused
	}

	out := &Response{Usage: usage}
	for _, cand := range resp.Candidates {
		out.StopReason = cand.FinishReason
		if cand.FinishReason == "SAFETY" || cand.FinishReason == "PROHIBITED_CONTENT" {
			return out, ErrRefused
		}
		for i, part := range cand.Content.Parts {
			if part.Text != "" {
				out.Text += part.Text
			}
			if part.FunctionCall != nil {
				// Gemini supplies no call id, so one is synthesized. The
				// harness correlates on it; the adapter maps it back to the
				// function name when replaying outcomes.
				out.Calls = append(out.Calls, ToolCall{
					ID:    fmt.Sprintf("%s-%d", part.FunctionCall.Name, i),
					Name:  part.FunctionCall.Name,
					Input: part.FunctionCall.Args,
				})
			}
		}
		break
	}
	if req.ForceTool != "" && len(out.Calls) == 0 {
		return out, ErrNoOutput
	}
	return out, nil
}

// geminiContents converts the harness's history into Gemini's shape.
func geminiContents(msgs []Message) []geminiContent {
	out := make([]geminiContent, 0, len(msgs))
	for _, m := range msgs {
		parts := make([]geminiPart, 0, 4)
		if m.Text != "" {
			parts = append(parts, geminiPart{Text: m.Text})
		}
		for _, c := range m.Calls {
			parts = append(parts, geminiPart{FunctionCall: &geminiFunctionCall{Name: c.Name, Args: c.Input}})
		}
		for _, r := range m.Outcomes {
			// Gemini keys responses by function NAME, not by call id — hence
			// the name carried on ToolOutcome.
			payload := map[string]any{"result": r.Content}
			if r.IsError {
				payload = map[string]any{"error": r.Content}
			}
			parts = append(parts, geminiPart{FunctionResponse: &geminiFunctionResponse{Name: r.Name, Response: payload}})
		}
		// Documents and images travel identically here: inlineData with the
		// right mimeType. Gemini extracts a PDF's native text, layout, tables
		// and charts without any pipeline of ours.
		for _, media := range m.Images {
			parts = append(parts, geminiPart{InlineData: &geminiInlineData{
				MimeType: media.MediaType,
				Data:     base64.StdEncoding.EncodeToString(media.Data),
			}})
		}
		if len(parts) == 0 {
			continue
		}
		role := "user"
		if m.Role == RoleAssistant {
			role = "model"
		}
		out = append(out, geminiContent{Role: role, Parts: parts})
	}
	return out
}

func (g *Gemini) call(ctx context.Context, model string, body geminiRequest) (*geminiResponse, Usage, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, Usage{}, err
	}
	url := fmt.Sprintf("%s/models/%s:generateContent", geminiBase, model)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, Usage{}, err
	}
	httpReq.Header.Set("content-type", "application/json")
	// The key travels in a header, not the query string: query strings land in
	// proxy and access logs.
	httpReq.Header.Set("x-goog-api-key", g.apiKey)

	res, err := g.http.Do(httpReq)
	if err != nil {
		return nil, Usage{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if err != nil {
		return nil, Usage{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}

	var parsed geminiResponse
	if uerr := json.Unmarshal(raw, &parsed); uerr != nil && res.StatusCode < 400 {
		return nil, Usage{}, fmt.Errorf("cognition: unreadable response: %v", uerr)
	}
	usage := Usage{
		InputTokens:  parsed.UsageMetadata.PromptTokenCount,
		OutputTokens: parsed.UsageMetadata.CandidatesTokenCount,
		CachedTokens: parsed.UsageMetadata.CachedContentTokenCount,
	}
	if cost, ok := CostUSD(model, usage); ok {
		usage.CostUSD = cost
	}

	if res.StatusCode >= 400 {
		msg := strings.TrimSpace(string(raw))
		if parsed.Error != nil {
			msg = parsed.Error.Message
		}
		switch res.StatusCode {
		case 429, 500, 502, 503, 504:
			return nil, usage, fmt.Errorf("%w: provider returned %d: %s", ErrUnavailable, res.StatusCode, msg)
		default:
			return nil, usage, fmt.Errorf("model call failed (%d): %s", res.StatusCode, msg)
		}
	}
	return &parsed, usage, nil
}

// resolveModel asks the account which models it can use and picks the best fit.
func (g *Gemini) resolveModel(ctx context.Context) (string, error) {
	models, err := g.ListModels(ctx)
	if err != nil {
		return "", err
	}
	for _, want := range []string{"flash-latest", "2.5-flash", "2.0-flash", "flash", "pro"} {
		for _, m := range models {
			if strings.Contains(m, want) && !strings.Contains(m, "thinking") {
				return m, nil
			}
		}
	}
	if len(models) > 0 {
		return models[0], nil
	}
	return "", fmt.Errorf("no models available to this key")
}

// ListModels returns the model ids this key may call generateContent on.
func (g *Gemini) ListModels(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, geminiBase+"/models?pageSize=200", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-goog-api-key", g.apiKey)
	res, err := g.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var listed struct {
		Models []struct {
			Name                       string   `json:"name"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if err := json.NewDecoder(res.Body).Decode(&listed); err != nil {
		return nil, err
	}
	var out []string
	for _, m := range listed.Models {
		for _, method := range m.SupportedGenerationMethods {
			if method == "generateContent" {
				out = append(out, strings.TrimPrefix(m.Name, "models/"))
				break
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

func isModelNotFound(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "(404)") || strings.Contains(s, "not found") ||
		strings.Contains(s, "is not supported") || strings.Contains(s, "not_found")
}

// geminiSchema adapts JSON Schema to Gemini's OpenAPI 3.0 subset. The important
// removal is additionalProperties: Anthropic's strict mode REQUIRES it and
// Gemini rejects it outright.
func geminiSchema(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		switch k {
		case "additionalProperties", "$schema", "strict":
			continue
		case "properties":
			if props, ok := v.(map[string]any); ok {
				cleaned := make(map[string]any, len(props))
				for name, prop := range props {
					cleaned[name] = geminiSchemaValue(prop)
				}
				out[k] = cleaned
				continue
			}
			out[k] = v
		default:
			out[k] = geminiSchemaValue(v)
		}
	}
	return out
}

func geminiSchemaValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return geminiSchema(t)
	case []any:
		items := make([]any, 0, len(t))
		for _, item := range t {
			items = append(items, geminiSchemaValue(item))
		}
		return items
	case []string:
		items := make([]any, 0, len(t))
		for _, item := range t {
			items = append(items, item)
		}
		return items
	default:
		return v
	}
}
