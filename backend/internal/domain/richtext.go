package domain

import "strings"

// The contract for the two text fields a rich-text element carries.
//
// `content.doc` is the document. `content.textPreview` is a preview OF it — and
// two writers held incompatible contracts for that one field. The human path
// capped it at 500 characters (NoteCard, DocumentCard: `editor.getText().slice(0,
// 500)`); the agent path wrote up to 20,000 characters straight in. Three
// consumers read the preview and nothing else — search, markdown export, and the
// agent's own digest of the board — so the field's length WAS the horizon of
// everything that could find, ship, or reason about a document.
//
// The corrupting writer turned out to be the person. The agent would write a
// genuine 20,000-character treatment, and the first keystroke a human typed into
// it to READ it truncated the preview back to 500 — permanently removing 97% of
// the text from search, from every export, and from every future run's reading,
// while `doc` kept it and only the editor could see it.
//
// The invariant: textPreview is a preview; nothing that NEEDS the text may read
// it. PlainTextOf is what the things that need it read instead.

// TextPreviewMax is the one cap, shared by every writer. It mirrors
// frontend/src/lib/textPreview.ts — a disagreement here is the bug this
// constant exists to make impossible.
const TextPreviewMax = 500

// TextPreview caps a plain-text rendering to the preview contract. Runes, not
// bytes: slicing Arabic or an emoji mid-sequence produces a preview the client
// renders as replacement characters.
func TextPreview(text string) string {
	r := []rune(text)
	if len(r) <= TextPreviewMax {
		return text
	}
	return string(r[:TextPreviewMax])
}

// PlainTextOf renders a Tiptap document back to plain text.
//
// The inverse of the tiptapDoc walker the agent's compiler uses, and it must
// round-trip against it: they are two halves of one representation, and a drift
// between them silently changes what search finds and what an export contains.
//
// Structure-agnostic by design. It walks any node tree, taking `text` wherever
// it appears and treating block-level nodes as line breaks, so a document
// containing headings, lists or tables written by a client this code has never
// seen still yields its words rather than nothing.
func PlainTextOf(doc any) string {
	var b strings.Builder
	walkRichText(doc, &b)
	return strings.Trim(b.String(), "\n")
}

// blockNodes end a line. Anything not named here is treated as inline, which is
// the safe default: a stray line break costs nothing, a missing one runs two
// paragraphs together.
var blockNodes = map[string]bool{
	"paragraph": true, "heading": true, "listItem": true, "blockquote": true,
	"codeBlock": true, "tableRow": true, "horizontalRule": true,
}

func walkRichText(node any, b *strings.Builder) {
	switch n := node.(type) {
	case []any:
		for _, child := range n {
			walkRichText(child, b)
		}
		return
	case map[string]any:
		kind, _ := n["type"].(string)
		if kind == "text" {
			if txt, _ := n["text"].(string); txt != "" {
				b.WriteString(txt)
			}
			return
		}
		if kind == "hardBreak" {
			b.WriteString("\n")
			return
		}
		walkRichText(n["content"], b)
		if blockNodes[kind] {
			b.WriteString("\n")
		}
	}
}
