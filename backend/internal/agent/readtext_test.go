package agent

import (
	"context"
	"strings"
	"testing"

	"qomranote/backend/internal/domain"
)

func longDocStaging(body string) *staging {
	s := reachStaging()
	s.scope.Elements["doc-long"] = &domain.Element{
		ID: "doc-long", Type: domain.TypeDocument,
		Content: domain.Content{
			"title": "Treatment",
			"doc":   tiptapDoc(body),
			// What the digest would show: a fragment.
			"textPreview": domain.TextPreview(body),
		},
		Location: domain.Location{ParentID: "b1", Section: domain.SectionCanvas},
	}
	return s
}

func longBody() string {
	return strings.Repeat("The harbour scene runs at dusk and nobody speaks. ", 120) +
		"The last line is the one that matters."
}

// Nothing could read an element's full text: the digest shows a fragment, and
// the deliberate read returned LESS than the free one. A run asked about a long
// document was answering from its opening paragraph.
func TestReadText_PagesTheWholeDocument(t *testing.T) {
	body := longBody()
	s := longDocStaging(body)

	out := s.runReadText(context.Background(),
		&toolArgs{ElementID: "doc-long"}, call(s, toolReadText))
	if out.IsError {
		t.Fatalf("read_text failed: %s", out.Content)
	}
	if !strings.Contains(out.Content, "characters remain") {
		t.Errorf("a truncated page does not say how much is left: %s", clipTo(out.Content, 200))
	}
	// The page must be materially bigger than what the listing shows, or the
	// deliberate read is worse than the free one — which is the defect.
	if len(out.Content) <= maxItemText {
		t.Errorf("read_text returned %d chars, no more than the free listing's %d",
			len(out.Content), maxItemText)
	}

	// Page to the end, then confirm the last sentence actually arrived.
	var seen strings.Builder
	seen.WriteString(out.Content)
	for i := 0; i < 12 && strings.Contains(out.Content, "offset="); i++ {
		next := nextOffset(out.Content)
		out = s.runReadText(context.Background(),
			&toolArgs{ElementID: "doc-long", Offset: next}, call(s, toolReadText))
		if out.IsError {
			t.Fatalf("paging failed at %d: %s", next, out.Content)
		}
		seen.WriteString(out.Content)
	}
	if !strings.Contains(seen.String(), "The last line is the one that matters.") {
		t.Error("paging never reached the end of the document")
	}
}

// The non-negotiable. set_note_text replaces the WHOLE body, and the review
// list shows what will exist, never what will stop existing — so a run that
// rewrites a document it has seen a paragraph of destroys the rest silently.
func TestReadText_RewritingWhatWasNeverReadIsRefused(t *testing.T) {
	body := longBody()
	s := longDocStaging(body)

	out := s.runSetText(context.Background(),
		&toolArgs{ElementID: "doc-long", Text: "A much shorter treatment."},
		call(s, toolSetText))
	if !out.IsError {
		t.Fatal("a run rewrote a long document it had never read")
	}
	if !strings.Contains(out.Content, "read_text") {
		t.Errorf("the refusal does not name the way forward: %s", out.Content)
	}
	if len(s.plan.Actions) != 0 {
		t.Errorf("the refusal still staged something: %+v", s.plan.Actions)
	}

	// After reading it all, the same write is allowed — the guard is about
	// having seen the text, not about forbidding the edit.
	for offset := 0; offset < len([]rune(body)); offset += maxTextPage {
		s.runReadText(context.Background(),
			&toolArgs{ElementID: "doc-long", Offset: offset, Limit: maxTextPage},
			call(s, toolReadText))
	}
	out = s.runSetText(context.Background(),
		&toolArgs{ElementID: "doc-long", Text: "A much shorter treatment."},
		call(s, toolSetText))
	if out.IsError {
		t.Fatalf("a fully-read document could still not be rewritten: %s", out.Content)
	}
}

// A short note is already fully visible in the board listing, so demanding a
// read_text for a two-line sticky would tax the common case instead of guarding
// the dangerous one.
func TestReadText_AShortNoteNeedsNoCeremony(t *testing.T) {
	s := reachStaging()
	s.scope.Elements["short-1"] = &domain.Element{
		ID: "short-1", Type: domain.TypeCard,
		Content:  domain.Content{"textPreview": "Call the harbour master"},
		Location: domain.Location{ParentID: "b1", Section: domain.SectionCanvas},
	}
	out := s.runSetText(context.Background(),
		&toolArgs{ElementID: "short-1", Text: "Call the harbour master on Tuesday"},
		call(s, toolSetText))
	if out.IsError {
		t.Fatalf("editing a short note was refused: %s", out.Content)
	}
}

func nextOffset(page string) int {
	i := strings.LastIndex(page, "offset=")
	if i < 0 {
		return 0
	}
	n := 0
	for _, c := range page[i+len("offset="):] {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func clipTo(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
