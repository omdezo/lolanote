package service

import (
	"context"
	"strings"
	"testing"

	"qomranote/backend/internal/domain"
)

// The markdown you send a producer contained a 500-character stub of every
// document — no ellipsis, no warning — because the exporter read
// content.textPreview, which is a PREVIEW. The full text was on the server the
// whole time, read by exactly one consumer: the Tiptap editor.
//
// Asserted on the RENDERED file, and specifically on the LAST sentence, because
// a check on the first one passes on the broken version.
func TestExport_ContainsTheWholeDocumentNotItsPreview(t *testing.T) {
	svc, elements, items := fixture(t)
	ctx := context.Background()
	alice := &domain.Principal{Sub: "alice"}
	boards := NewBoardService(elements, nil, NewAccessResolver(elements))

	// A treatment written the way a person writes one: through the ordinary
	// write path, carrying a real document, far longer than any preview.
	const tail = "and the harbour goes dark on the last line."
	body := strings.Repeat("The scene runs at dusk and nobody speaks. ", 200) + tail
	paragraphs := make([]any, 0, 2)
	for _, p := range strings.Split(body, "\n") {
		paragraphs = append(paragraphs, map[string]any{
			"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": p}},
		})
	}

	if _, err := svc.Apply(ctx, alice, items["boardA"].ID, "c1", []domain.Op{{
		ElementID: "aaaaaaaaaaaaaaaaaaaaaad1",
		Action:    domain.ActionCreate,
		Changes: domain.Content{
			"type":     string(domain.TypeDocument),
			"location": map[string]any{"parentId": items["boardA"].ID, "section": "CANVAS"},
			"content": map[string]any{
				"title": "Treatment",
				// What the editor actually writes: the whole document, and a
				// preview capped at the editor's own limit.
				"doc":         map[string]any{"type": "doc", "content": paragraphs},
				"textPreview": domain.TextPreview(body),
			},
		},
	}}); err != nil {
		t.Fatalf("write the treatment: %v", err)
	}

	out, _, err := boards.Export(ctx, alice, items["boardA"].ID, "markdown")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if !strings.Contains(out, tail) {
		t.Errorf("the export stops short — the last sentence is missing from %d chars of output", len(out))
	}

	// And the derived search body was written at commit, so the document is
	// findable by its ending too, not only by its opening paragraph.
	el, err := elements.Get(ctx, "aaaaaaaaaaaaaaaaaaaaaad1")
	if err != nil {
		t.Fatalf("reread: %v", err)
	}
	search, _ := el.Content["searchText"].(string)
	if !strings.Contains(search, tail) {
		t.Errorf("searchText does not carry the whole body: %d chars", len(search))
	}
	// The preview stayed a preview.
	preview, _ := el.Content["textPreview"].(string)
	if len([]rune(preview)) > domain.TextPreviewMax {
		t.Errorf("textPreview grew past its contract: %d runes", len([]rune(preview)))
	}
}
