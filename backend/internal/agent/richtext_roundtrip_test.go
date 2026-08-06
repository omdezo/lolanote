package agent

import (
	"strings"
	"testing"

	"qomranote/backend/internal/domain"
)

// tiptapDoc and PlainTextOf are two halves of one representation, and a drift
// between them silently changes what search finds and what an export contains.
// One fixture table, walked both ways.
func TestRichText_TheTwoWalkersRoundTrip(t *testing.T) {
	cases := []string{
		"one line",
		"two\nlines",
		"a paragraph\n\nwith a blank line between",
		"نصّ عربي على سطرين\nوالسطر الثاني",
		"trailing spaces are text   \nand so is this",
		strings.Repeat("a long body that outruns every preview cap. ", 80),
	}
	for _, want := range cases {
		got := domain.PlainTextOf(tiptapDoc(want))
		if got != want {
			t.Errorf("round trip lost text:\n want %q\n  got %q", clip(want), clip(got))
		}
	}
}

// The preview cap is the CONTRACT, and the agent used to ignore it: it wrote up
// to 20,000 characters into the field the editor truncates at 500, so the first
// keystroke a person typed into the agent's document permanently destroyed 97%
// of what search, export and every future run could see.
func TestRichText_TheAgentObeysThePreviewCap(t *testing.T) {
	body := strings.Repeat("The harbour scene runs at dusk. ", 400) // ~12,800 chars
	op := createOp(Action{
		Kind: ActWriteDocument, ElementID: "d1", ParentID: "b1",
		Title: "Treatment", Text: body,
	})
	content, _ := op.Changes["content"].(map[string]any)

	preview, _ := content["textPreview"].(string)
	if len([]rune(preview)) > domain.TextPreviewMax {
		t.Errorf("preview is %d runes, the contract is %d", len([]rune(preview)), domain.TextPreviewMax)
	}
	// And the whole body is still there, under the key that is allowed to hold
	// it — otherwise obeying the cap would just be data loss with a tidier name.
	search, _ := content["searchText"].(string)
	if search != body {
		t.Errorf("the body was not preserved: %d of %d chars", len(search), len(body))
	}
	if !strings.HasSuffix(strings.TrimSpace(search), "runs at dusk.") {
		t.Error("the end of the document is missing from the searchable body")
	}
}

func clip(s string) string {
	if len(s) > 90 {
		return s[:90] + "…"
	}
	return s
}
