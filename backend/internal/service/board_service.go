package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"qomranote/backend/internal/domain"
)

// BoardService serves the read paths a board render needs: the board itself
// with its breadcrumb path (§3.2), its children (the core query, §9.4), the
// Unsorted tray (§3.3), search (§3.5), templates (§5), and export (§7.2).
type BoardService struct {
	elements    domain.ElementRepository
	users       domain.UserRepository
	access      *AccessResolver
	attachments domain.AttachmentRepository // optional: the export's file manifest
}

// NewBoardService constructs the service.
func NewBoardService(elements domain.ElementRepository, users domain.UserRepository, access *AccessResolver) *BoardService {
	return &BoardService{elements: elements, users: users, access: access}
}

// AttachAttachments lets an export name the files it refers to. Optional: with
// no attachment store the export still strips the credentials, it just cannot
// say what the files were called.
func (s *BoardService) AttachAttachments(a domain.AttachmentRepository) { s.attachments = a }

// BoardView is everything the client needs to open a board.
type BoardView struct {
	Board      *domain.Element          `json:"board"`
	Breadcrumb []domain.BreadcrumbEntry `json:"breadcrumb"`
	Role       string                   `json:"role"`
}

// Get loads a board, the caller's role on it, and the Home→…→here path.
func (s *BoardService) Get(ctx context.Context, p *domain.Principal, boardID string) (*BoardView, error) {
	role, _, err := s.access.RequireView(ctx, boardID, p)
	if err != nil {
		return nil, err
	}
	board, err := s.elements.Get(ctx, boardID)
	if err != nil {
		return nil, err
	}
	if board.Type != domain.TypeBoard {
		return nil, domain.ErrNotFound
	}

	// Breadcrumb: walk up, then reverse. Only hops the caller can see are
	// shown. Starts non-nil so the JSON stays an array for root boards.
	crumbs := []domain.BreadcrumbEntry{}
	id := board.Location.ParentID
	for depth := 0; id != "" && depth < maxDepth; depth++ {
		el, err := s.elements.Get(ctx, id)
		if err != nil {
			break
		}
		if el.Type == domain.TypeBoard {
			if r, _, err := s.access.Resolve(ctx, el.ID, p); err != nil || !r.CanView() {
				break
			}
			crumbs = append(crumbs, domain.BreadcrumbEntry{ID: el.ID, Title: el.Title()})
		}
		id = el.Location.ParentID
	}
	for i, j := 0, len(crumbs)-1; i < j; i, j = i+1, j-1 {
		crumbs[i], crumbs[j] = crumbs[j], crumbs[i]
	}

	return &BoardView{Board: redact(board, role), Breadcrumb: crumbs, Role: roleName(role)}, nil
}

// Children returns every live element on the board's canvas plus the
// contents of its columns/task lists — one payload renders the whole board.
func (s *BoardService) Children(ctx context.Context, p *domain.Principal, boardID string) ([]*domain.Element, error) {
	role, _, err := s.access.RequireView(ctx, boardID, p)
	if err != nil {
		return nil, err
	}
	direct, err := s.elements.Children(ctx, domain.ElementFilter{ParentID: boardID})
	if err != nil {
		return nil, err
	}
	// Nested boards carry their OWN ACL, so this payload used to hand a viewer
	// every sub-board's share tokens along with the top one's.
	out := make([]*domain.Element, 0, len(direct))
	var containerIDs []string
	for _, el := range direct {
		out = append(out, el)
		if el.Type.IsContainer() && el.Type != domain.TypeBoard {
			containerIDs = append(containerIDs, el.ID)
		}
	}
	for _, cid := range containerIDs {
		nested, err := s.elements.Descendants(ctx, cid, false)
		if err != nil {
			return nil, err
		}
		out = append(out, nested...)
	}
	// Clone instances render their source's content: resolve sources the
	// caller may not have loaded yet.
	var sourceIDs []string
	seen := map[string]bool{}
	for _, el := range out {
		seen[el.ID] = true
	}
	for _, el := range out {
		if el.Type == domain.TypeClone {
			if src, ok := el.Content["cloneSourceId"].(string); ok && !seen[src] {
				sourceIDs = append(sourceIDs, src)
				seen[src] = true
			}
		}
	}
	if len(sourceIDs) > 0 {
		sources, err := s.elements.GetMany(ctx, sourceIDs)
		if err != nil {
			return nil, err
		}
		// A CLONE names its source by id, and the write path lets anyone put a
		// CLONE on their OWN board pointing at ANY element — it only checks the
		// clone's parent. So resolving those ids unchecked turns this read into
		// a cross-tenant disclosure: forge a clone, read the victim's card.
		//
		// Every source is therefore authorised on its own merits, against the
		// board it actually lives under, not the board being rendered. Sources
		// the caller cannot view are dropped rather than erroring: a clone
		// whose source was un-shared is a normal state, not a failed request.
		for _, src := range sources {
			if _, _, err := s.access.RequireView(ctx, src.ID, p); err != nil {
				continue
			}
			out = append(out, src)
		}
	}
	return redactAll(out, role), nil
}

// ChildBoardStats returns per-child-board content counts for board tiles
// ("14 boards, 2 cards, 1 file" subtitles).
func (s *BoardService) ChildBoardStats(ctx context.Context, p *domain.Principal, boardID string) (map[string]map[domain.ElementType]int64, error) {
	if _, _, err := s.access.RequireView(ctx, boardID, p); err != nil {
		return nil, err
	}
	boards, err := s.elements.Children(ctx, domain.ElementFilter{
		ParentID: boardID, Types: []domain.ElementType{domain.TypeBoard},
	})
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(boards))
	for _, b := range boards {
		ids = append(ids, b.ID)
	}
	return s.elements.CountsByParent(ctx, ids)
}

// Unsorted returns the board's capture tray, ordered (§3.3). Everyone with
// board access — including read-only viewers — can see it.
func (s *BoardService) Unsorted(ctx context.Context, p *domain.Principal, boardID string) ([]*domain.Element, error) {
	role, _, err := s.access.RequireView(ctx, boardID, p)
	if err != nil {
		return nil, err
	}
	tray, err := s.elements.Children(ctx, domain.ElementFilter{
		ParentID: boardID, Section: domain.SectionUnsorted,
	})
	if err != nil {
		return nil, err
	}
	return redactAll(tray, role), nil
}

// Search spans the caller's reachable content; sortable by last modified (§3.5).
func (s *BoardService) Search(ctx context.Context, p *domain.Principal, query string, limit int) ([]*domain.Element, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return []*domain.Element{}, nil
	}
	hits, err := s.elements.Search(ctx, p.Sub, query, limit)
	if err != nil {
		return nil, err
	}
	// Search spans boards held at every role, and resolving each hit's own role
	// would be a walk per result. The lower bound is the honest one here: a
	// result list needs the owner to render, never a member list.
	return redactAll(hits, RoleView), nil
}

// Templates lists boards flagged as templates: the caller's own plus the
// seeded system library (§5).
func (s *BoardService) Templates(ctx context.Context, p *domain.Principal) ([]*domain.Element, error) {
	mine, err := s.elements.BoardsOwnedBy(ctx, p.Sub, true)
	if err != nil {
		return nil, err
	}
	system, err := s.elements.BoardsOwnedBy(ctx, "system", true)
	if err != nil {
		return nil, err
	}
	return redactAll(append(mine, system...), RoleEdit), nil
}

// Boards lists the boards a user owns or edits (share pickers, move targets).
func (s *BoardService) Boards(ctx context.Context, p *domain.Principal) ([]*domain.Element, error) {
	boards, err := s.elements.BoardsOwnedBy(ctx, p.Sub, false)
	if err != nil {
		return nil, err
	}
	return redactAll(boards, RoleEdit), nil
}

// ---- Export (§7.2): linearized markdown / plain text / JSON ----

// Export flattens a board subtree. The Home board can never be exported (§3.1).
func (s *BoardService) Export(ctx context.Context, p *domain.Principal, boardID, format string) (string, string, error) {
	board, err := s.elements.Get(ctx, boardID)
	if err != nil {
		return "", "", err
	}
	if isHome(board) {
		return "", "", domain.ErrHomeBoard
	}
	role, _, err := s.access.RequireView(ctx, boardID, p)
	if err != nil {
		return "", "", err
	}

	// JSON: the full raw subtree — lossless, machine-readable.
	if format == "json" {
		// Bounded, and refusing rather than truncating: an archive that stops
		// silently looks complete, which is the worst thing an archive can be.
		descendants, err := subtreeOf(ctx, s.elements, board.ID, false)
		if err != nil {
			return "", "", err
		}
		// "Lossless" was over-inclusive in the one field that must never leave
		// the owner: this route is in the optional-auth group, so a read-only
		// link plus ?format=json was a complete view-to-edit escalation.
		//
		// It was over-inclusive in a second: content.url carried a presigned
		// AWS SigV4 URL — a bearer credential for direct bucket access that
		// bypasses the application and stays valid for seven days after the
		// share it came with is revoked. An export is an archive, so the files
		// become a manifest of what they ARE and the id that resolves them
		// through the ACL-checked blob route.
		files := attachmentManifest(ctx, s.attachments, descendants)
		stripCredentialURLs(descendants)
		payload, err := json.MarshalIndent(map[string]any{
			"board": redact(board, role), "elements": redactAll(descendants, role),
			"attachments": files,
		}, "", "  ")
		if err != nil {
			return "", "", err
		}
		return string(payload), "application/json; charset=utf-8", nil
	}

	var b strings.Builder
	if err := s.renderBoard(ctx, &b, board, 1, format); err != nil {
		return "", "", err
	}
	switch format {
	case "markdown":
		return b.String(), "text/markdown; charset=utf-8", nil
	case "text":
		plain := strings.NewReplacer("# ", "", "## ", "", "- [ ] ", "[ ] ", "- [x] ", "[x] ", "- ", "").Replace(b.String())
		return plain, "text/plain; charset=utf-8", nil
	default:
		return "", "", domain.ErrValidation
	}
}

func (s *BoardService) renderBoard(ctx context.Context, b *strings.Builder, board *domain.Element, depth int, format string) error {
	if depth > 12 {
		return nil
	}
	fmt.Fprintf(b, "%s %s\n\n", strings.Repeat("#", minInt(depth, 6)), board.Title())
	children, err := s.elements.Children(ctx, domain.ElementFilter{ParentID: board.ID})
	if err != nil {
		return err
	}
	// Reading order: top-to-bottom, then left-to-right — the linearization
	// Milanote applies for Word/Markdown exports.
	sort.SliceStable(children, func(i, j int) bool {
		if children[i].Location.Position.Y != children[j].Location.Position.Y {
			return children[i].Location.Position.Y < children[j].Location.Position.Y
		}
		return children[i].Location.Position.X < children[j].Location.Position.X
	})
	for _, el := range children {
		s.renderElement(ctx, b, el, depth, format)
	}
	return nil
}

// exportBodyOf is the real text of a note or document, in preference order:
// the rich document itself, the derived search body, and only then the preview.
//
// The preview is last because it is a preview. An export built from it shipped
// the first 500 characters of every document with nothing saying so.
func exportBodyOf(el *domain.Element) string {
	if doc, ok := el.Content["doc"]; ok && doc != nil {
		if txt := domain.PlainTextOf(doc); txt != "" {
			return txt
		}
	}
	if txt, _ := el.Content["searchText"].(string); txt != "" {
		return txt
	}
	txt, _ := el.Content["textPreview"].(string)
	return txt
}

func (s *BoardService) renderElement(ctx context.Context, b *strings.Builder, el *domain.Element, depth int, format string) {
	switch el.Type {
	case domain.TypeBoard:
		_ = s.renderBoard(ctx, b, el, depth+1, format)
	case domain.TypeCard, domain.TypeDocument:
		// The whole document, not its first 500 characters. This read
		// textPreview, so the markdown you sent a producer contained a stub of
		// every document — no ellipsis, no warning, and the full text sitting on
		// the server the whole time, read by exactly one consumer: the editor.
		if txt := exportBodyOf(el); txt != "" {
			fmt.Fprintf(b, "%s\n\n", txt)
		}
	case domain.TypeLink:
		title := el.Title()
		if url, ok := el.Content["url"].(string); ok {
			fmt.Fprintf(b, "- [%s](%s)\n\n", title, url)
		}
	case domain.TypeColumn, domain.TypeTaskList:
		if t := el.Title(); t != "" {
			fmt.Fprintf(b, "%s **%s**\n\n", strings.Repeat("#", minInt(depth+1, 6)), t)
		}
		children, err := s.elements.Children(ctx, domain.ElementFilter{ParentID: el.ID})
		if err == nil {
			for _, child := range children {
				s.renderElement(ctx, b, child, depth, format)
			}
		}
	case domain.TypeTask:
		mark := " "
		if done, _ := el.Content["done"].(bool); done {
			mark = "x"
		}
		text, _ := el.Content["text"].(string)
		fmt.Fprintf(b, "- [%s] %s\n", mark, text)
	case domain.TypeImage, domain.TypeFile:
		if url, ok := el.Content["url"].(string); ok {
			fmt.Fprintf(b, "![%s](%s)\n\n", el.Title(), url)
		}
	case domain.TypeColorSwatch:
		if hex, ok := el.Content["hex"].(string); ok {
			fmt.Fprintf(b, "- Color: `%s`\n\n", hex)
		}
	}
}

func roleName(r Role) string {
	switch r {
	case RoleOwner:
		return "owner"
	case RoleEdit:
		return "edit"
	case RoleFeedback:
		return "feedback"
	case RoleView:
		return "view"
	default:
		return "none"
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
