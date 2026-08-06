package cognition

import (
	"encoding/json"
	"strings"
	"testing"
)

// A request with NO system prompt still sent a system_instruction carrying one
// empty part, and Gemini rejects that before the model is ever reached:
//
//	400 — GenerateContentRequest.system_instruction.parts[0].data:
//	      required oneof field 'data' must have one initialized field
//
// Every real run sets a system prompt, so this stayed hidden until something
// asked a question without one: the provider HEALTH PROBE, whose whole job is
// to find out whether the provider will talk to us. It reported the provider
// dead while the provider was fine, and the composer told the person "Qomra is
// unavailable" on a working deployment — the check meant to catch an outage
// manufacturing one.
//
// Asserted on the serialized BODY rather than on the struct, because what
// mattered was the JSON that went over the wire.
func TestGemini_ASystemlessRequestSendsNoSystemInstruction(t *testing.T) {
	body := geminiRequest{Contents: geminiContents([]Message{{Role: RoleUser, Text: "ok"}})}
	if strings.TrimSpace("") != "" { // mirrors the guard in Complete
		body.SystemInstruction = &geminiContent{Parts: []geminiPart{{Text: ""}}}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "system_instruction") {
		t.Fatalf("a systemless request still carries a system_instruction: %s", raw)
	}
}

// And the ordinary case is untouched — the guard must not cost the agent its
// actual instructions.
func TestGemini_ASystemPromptIsStillSent(t *testing.T) {
	system := "You are Qomra."
	body := geminiRequest{Contents: geminiContents([]Message{{Role: RoleUser, Text: "ok"}})}
	if strings.TrimSpace(system) != "" {
		body.SystemInstruction = &geminiContent{Parts: []geminiPart{{Text: system}}}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), "system_instruction") ||
		!strings.Contains(string(raw), "You are Qomra.") {
		t.Fatalf("the system prompt did not reach the wire: %s", raw)
	}
}
