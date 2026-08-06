package agent

import (
	"fmt"
	"regexp"
	"strings"
)

// The agent's writing surface.
//
// `tiptapDoc` split on "\n" and emitted paragraph/text nodes, and nothing else.
// The editor loads StarterKit + Link + TextStyle + Color + Highlight +
// Underline; the format bar offers bold, italic, underline, strike, H1, H2,
// bullet and ordered lists, blockquote, code, links, colour and highlight. So:
//
//   - ASKED FOR A TREATMENT, `write_document` produced an undifferentiated wall.
//     Every structural affordance the product has was unreachable to the one
//     author on the board that writes at length.
//   - THE DIGEST COMPOUNDED IT. `textFor` reads `textPreview` alone, so the agent
//     could not see that a document HAS structure — and `set_note_text`
//     ("replaces what is there") flattened a human's carefully formatted note
//     into paragraphs. That is destructive, not merely limited.
//
// So: one markdown subset, written by the compiler and read back by the digest,
// and a REFUSAL when a document carries formatting the subset cannot express. A
// lossy round trip that silently succeeds is worse than the flat state it
// replaces, because the person cannot tell it happened.

// The subset. Chosen as the intersection of what markdown states unambiguously
// in one line and what the editor's own schema stores — nothing here needs a
// parser with a stack, and nothing here is a guess about intent.
//
//	# / ##        headings 1 and 2
//	- / * / 1.    bullet and ordered list items
//	> quote       blockquote
//	```           fenced code block
//	**bold** *italic* `code` [text](url)   inline marks
var (
	mdHeading   = regexp.MustCompile(`^(#{1,3})\s+(.*)$`)
	mdBullet    = regexp.MustCompile(`^\s*[-*+]\s+(.*)$`)
	mdOrdered   = regexp.MustCompile(`^\s*\d+[.)]\s+(.*)$`)
	mdQuote     = regexp.MustCompile(`^>\s?(.*)$`)
	mdInline    = regexp.MustCompile("(\\*\\*[^*]+\\*\\*)|(\\*[^*]+\\*)|(`[^`]+`)|(\\[[^\\]]+\\]\\([^)]+\\))")
	mdLinkParts = regexp.MustCompile(`^\[([^\]]+)\]\(([^)]+)\)$`)
)

// expressibleMarks are the inline marks the translator can carry BOTH ways. A
// document whose text carries anything else cannot be rewritten without loss,
// which is what makes the refusal computable rather than a judgement call.
var expressibleMarks = map[string]bool{
	"bold": true, "italic": true, "code": true, "link": true,
}

// expressibleNodes are the block types the translator round-trips.
var expressibleNodes = map[string]bool{
	"doc": true, "paragraph": true, "text": true, "heading": true,
	"bulletList": true, "orderedList": true, "listItem": true,
	"blockquote": true, "codeBlock": true, "hardBreak": true,
}

// MarkdownToTiptap turns the authoring subset into the document shape the editor
// stores.
//
// It replaces the old paragraph-splitter and is a strict superset of it: text
// with no markdown in it produces exactly the paragraphs it always did, which is
// what keeps every existing note identical.
func MarkdownToTiptap(text string) map[string]any {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	content := make([]any, 0, len(lines))

	for i := 0; i < len(lines); i++ {
		line := lines[i]

		// A fenced block is verbatim by definition: nothing inside it is markup,
		// which is the whole reason somebody fenced it.
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			lang := strings.TrimPrefix(strings.TrimSpace(line), "```")
			var body []string
			i++
			for ; i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), "```"); i++ {
				body = append(body, lines[i])
			}
			node := map[string]any{
				"type":    "codeBlock",
				"content": []any{map[string]any{"type": "text", "text": strings.Join(body, "\n")}},
			}
			if lang != "" {
				node["attrs"] = map[string]any{"language": lang}
			}
			content = append(content, node)
			continue
		}

		if m := mdHeading.FindStringSubmatch(line); m != nil {
			content = append(content, map[string]any{
				"type":    "heading",
				"attrs":   map[string]any{"level": len(m[1])},
				"content": inlineNodes(m[2]),
			})
			continue
		}

		// A run of list items is ONE list node holding many items — the shape the
		// editor stores. Emitting one list per line produces a document that looks
		// right and cannot be edited, because every bullet is its own list.
		if mdBullet.MatchString(line) || mdOrdered.MatchString(line) {
			ordered := mdOrdered.MatchString(line)
			var items []any
			for ; i < len(lines); i++ {
				var body string
				switch {
				case !ordered && mdBullet.MatchString(lines[i]):
					body = mdBullet.FindStringSubmatch(lines[i])[1]
				case ordered && mdOrdered.MatchString(lines[i]):
					body = mdOrdered.FindStringSubmatch(lines[i])[1]
				default:
					i--
					goto flushList
				}
				items = append(items, map[string]any{
					"type": "listItem",
					"content": []any{map[string]any{
						"type": "paragraph", "content": inlineNodes(body),
					}},
				})
			}
			i--
		flushList:
			kind := "bulletList"
			if ordered {
				kind = "orderedList"
			}
			content = append(content, map[string]any{"type": kind, "content": items})
			continue
		}

		if m := mdQuote.FindStringSubmatch(line); m != nil {
			content = append(content, map[string]any{
				"type": "blockquote",
				"content": []any{map[string]any{
					"type": "paragraph", "content": inlineNodes(m[1]),
				}},
			})
			continue
		}

		if strings.TrimSpace(line) == "" {
			content = append(content, map[string]any{"type": "paragraph"})
			continue
		}
		content = append(content, map[string]any{
			"type": "paragraph", "content": inlineNodes(line),
		})
	}
	return map[string]any{"type": "doc", "content": content}
}

// inlineNodes splits one line into text nodes carrying marks.
func inlineNodes(line string) []any {
	if line == "" {
		return nil
	}
	var out []any
	last := 0
	for _, loc := range mdInline.FindAllStringIndex(line, -1) {
		if loc[0] > last {
			out = append(out, textNode(line[last:loc[0]], nil))
		}
		tok := line[loc[0]:loc[1]]
		switch {
		case strings.HasPrefix(tok, "**"):
			out = append(out, textNode(strings.Trim(tok, "*"), []any{markNode("bold", nil)}))
		case strings.HasPrefix(tok, "*"):
			out = append(out, textNode(strings.Trim(tok, "*"), []any{markNode("italic", nil)}))
		case strings.HasPrefix(tok, "`"):
			out = append(out, textNode(strings.Trim(tok, "`"), []any{markNode("code", nil)}))
		case strings.HasPrefix(tok, "["):
			m := mdLinkParts.FindStringSubmatch(tok)
			if m == nil {
				out = append(out, textNode(tok, nil))
				break
			}
			out = append(out, textNode(m[1], []any{markNode("link", map[string]any{"href": m[2]})}))
		}
		last = loc[1]
	}
	if last < len(line) {
		out = append(out, textNode(line[last:], nil))
	}
	return out
}

func textNode(text string, marks []any) map[string]any {
	n := map[string]any{"type": "text", "text": text}
	if len(marks) > 0 {
		n["marks"] = marks
	}
	return n
}

func markNode(kind string, attrs map[string]any) map[string]any {
	m := map[string]any{"type": kind}
	if attrs != nil {
		m["attrs"] = attrs
	}
	return m
}

// TiptapToMarkdown is the inverse: what the digest shows the model, so that
// STRUCTURE ROUND-TRIPS.
//
// Without it the agent reads `textPreview` — 500 characters of flattened prose —
// and cannot tell a formatted document from a wall. Then it rewrites the wall it
// thinks it saw.
func TiptapToMarkdown(doc any) string {
	var b strings.Builder
	renderNode(doc, &b, "")
	return strings.Trim(b.String(), "\n")
}

func renderNode(node any, b *strings.Builder, listPrefix string) {
	switch n := node.(type) {
	case []any:
		for _, c := range n {
			renderNode(c, b, listPrefix)
		}
	case map[string]any:
		kind, _ := n["type"].(string)
		switch kind {
		case "text":
			b.WriteString(wrapMarks(n))
		case "hardBreak":
			b.WriteString("\n")
		case "heading":
			level := 1
			if attrs, ok := n["attrs"].(map[string]any); ok {
				if f, ok := attrs["level"].(float64); ok && f >= 1 && f <= 6 {
					level = int(f)
				}
				if i, ok := attrs["level"].(int); ok && i >= 1 && i <= 6 {
					level = i
				}
			}
			b.WriteString(strings.Repeat("#", level) + " ")
			renderNode(n["content"], b, "")
			b.WriteString("\n")
		case "bulletList":
			renderList(n, b, "- ")
		case "orderedList":
			renderList(n, b, "1. ")
		case "listItem":
			b.WriteString(listPrefix)
			renderNode(n["content"], b, "")
			b.WriteString("\n")
		case "blockquote":
			b.WriteString("> ")
			renderNode(n["content"], b, "")
			b.WriteString("\n")
		case "codeBlock":
			b.WriteString("```\n")
			renderNode(n["content"], b, "")
			b.WriteString("\n```\n")
		case "paragraph":
			renderNode(n["content"], b, "")
			b.WriteString("\n")
		default:
			renderNode(n["content"], b, listPrefix)
		}
	}
}

func renderList(n map[string]any, b *strings.Builder, prefix string) {
	items, _ := n["content"].([]any)
	for i, item := range items {
		p := prefix
		if prefix == "1. " {
			p = fmt.Sprintf("%d. ", i+1)
		}
		// A listItem holds paragraphs; rendering them through the paragraph arm
		// would add a newline before the bullet's own, so the item is rendered
		// with its prefix and the paragraph's trailing newline is the item's.
		var inner strings.Builder
		renderNode(item.(map[string]any)["content"], &inner, "")
		for _, line := range strings.Split(strings.Trim(inner.String(), "\n"), "\n") {
			b.WriteString(p + line + "\n")
		}
	}
}

// wrapMarks re-applies a text node's marks as markdown. Only the expressible
// ones: an inexpressible mark is dropped HERE and refused THERE, so what the
// model reads is honest markdown and what it may overwrite is gated separately.
func wrapMarks(n map[string]any) string {
	text, _ := n["text"].(string)
	if text == "" {
		return ""
	}
	marks, _ := n["marks"].([]any)
	href := ""
	bold, italic, code := false, false, false
	for _, m := range marks {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		switch mm["type"] {
		case "bold":
			bold = true
		case "italic":
			italic = true
		case "code":
			code = true
		case "link":
			if attrs, ok := mm["attrs"].(map[string]any); ok {
				href, _ = attrs["href"].(string)
			}
		}
	}
	if code {
		text = "`" + text + "`"
	}
	if bold {
		text = "**" + text + "**"
	}
	if italic {
		text = "*" + text + "*"
	}
	if href != "" {
		text = "[" + text + "](" + href + ")"
	}
	return text
}

// InexpressibleFormatting names the formatting in a document that the markdown
// subset cannot carry, or "" when a rewrite is lossless.
//
// THIS IS THE REFUSAL BRANCH, and it is the non-negotiable half of the feature.
// `set_note_text` replaces the whole body. Handed a note a person underlined,
// highlighted, coloured or laid out in a table, the translator would write back
// plain paragraphs — silently destroying work that took real effort, on a
// surface whose review row says only "edit note X". A lossy round trip that
// succeeds quietly is worse than the flat state it replaces.
func InexpressibleFormatting(doc any) string {
	found := map[string]bool{}
	scanFormatting(doc, found)
	if len(found) == 0 {
		return ""
	}
	names := make([]string, 0, len(found))
	for n := range found {
		names = append(names, n)
	}
	sortStrings(names)
	return strings.Join(names, ", ")
}

func scanFormatting(node any, found map[string]bool) {
	switch n := node.(type) {
	case []any:
		for _, c := range n {
			scanFormatting(c, found)
		}
	case map[string]any:
		kind, _ := n["type"].(string)
		if kind != "" && !expressibleNodes[kind] {
			found[humanNodeName(kind)] = true
		}
		if marks, ok := n["marks"].([]any); ok {
			for _, m := range marks {
				mm, ok := m.(map[string]any)
				if !ok {
					continue
				}
				mt, _ := mm["type"].(string)
				if mt != "" && !expressibleMarks[mt] {
					found[humanNodeName(mt)] = true
				}
			}
		}
		scanFormatting(n["content"], found)
	}
}

// humanNodeName turns a schema name into what the person would call it, because
// the refusal is read by a model that then has to explain it to somebody.
func humanNodeName(kind string) string {
	switch kind {
	case "underline":
		return "underlining"
	case "strike":
		return "strikethrough"
	case "highlight":
		return "highlighting"
	case "textStyle":
		return "coloured text"
	case "table", "tableRow", "tableCell", "tableHeader":
		return "a table"
	case "image":
		return "an inline image"
	case "taskList", "taskItem":
		return "checkboxes"
	case "horizontalRule":
		return "a divider"
	}
	return kind
}
