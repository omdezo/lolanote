package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"golang.org/x/crypto/bcrypt"

	"qomranote/backend/internal/domain"
)

// ShareService implements all four sharing mechanisms (§6.1): invite editors
// by email, shareable editor link, read-only link with feedback, and
// account-free view link (with optional password + welcome message).
// Permissions cascade to nested sub-boards automatically because access
// resolution walks ancestors (see AccessResolver).
type ShareService struct {
	elements domain.ElementRepository
	users    *UserService
	notifier *Notifier
	access   *AccessResolver
	audit    *AuditService
	evictor  SessionEvictor // optional
}

// SessionEvictor closes the live realtime sessions a revocation should end.
//
// "Remove" in a sharing dialog means NOW. It meant "at some point up to the
// remaining lifetime of a token you cannot see": authorization for a socket is
// checked exactly once, at handshake, and room membership is keyed on the
// connection and never re-resolved — so a removed collaborator kept receiving,
// live, the full text of every card anyone edited on the board they had just
// been cut off from. On a production board that is precisely the window in which
// the removal happened for a reason.
//
// Driven by the ACL WRITE rather than by a sweeper, because "eventually" is a
// different promise from the one the dialog makes.
type SessionEvictor interface {
	// Evict closes the named person's connections to a board.
	Evict(boardID, sub string)
	// EvictByShareToken closes the connections that joined on a revoked link.
	EvictByShareToken(boardID, token string)
}

// AttachEvictor wires realtime eviction. Optional so tests and minimal wiring
// can skip it — and so a deployment without it degrades to the old behaviour
// rather than failing to start.
func (s *ShareService) AttachEvictor(e SessionEvictor) { s.evictor = e }

// NewShareService constructs the service.
//
// The audit log is a constructor argument rather than an Attach, unlike the
// evictor: every verb in here changes who can read somebody's board, and "who
// was given access, by whom, when" is the first question of any security review.
// A dependency that can be forgotten at one call site is a dependency that will
// be, and an audit trail with a deployment-shaped hole in it answers nothing.
func NewShareService(elements domain.ElementRepository, users *UserService, notifier *Notifier, access *AccessResolver, audit *AuditService) *ShareService {
	return &ShareService{elements: elements, users: users, notifier: notifier, access: access, audit: audit}
}

// auditBoard names the board as it was at the moment of the event, so the row
// stays readable after a rename and after a delete.
func auditBoard(board *domain.Element) domain.AuditTarget {
	return domain.AuditTarget{Type: "board", ID: board.ID, Label: board.Title()}
}

// ShareState is what the Share dialog renders.
//
// The two link tokens are named explicitly here rather than inherited from the
// domain type, and that is the point: this struct is the ONE door a share token
// leaves through. domain.ACL keeps its tokens out of JSON entirely, so a new
// endpoint that serializes an element cannot leak them by forgetting to redact
// — which is how a view-link holder came to be able to read the board's edit
// token out of an ordinary GET.
type ShareState struct {
	OwnerID        string         `json:"ownerId"`
	Editors        []string       `json:"editors"`
	PublicEditLink string         `json:"publicEditLink,omitempty"`
	ViewLink       *ShareViewLink `json:"viewLink,omitempty"`
	// AgentPolicy rides the Share dialog because that is where an owner already
	// decides who may edit; who may AUTOMATE is the same decision one field over.
	AgentPolicy *domain.AgentPolicy `json:"agentPolicy,omitempty"`
}

// ShareViewLink mirrors domain.ViewLink on the wire, token included.
type ShareViewLink struct {
	Token          string `json:"token"`
	AllowFeedback  bool   `json:"allowFeedback"`
	RequireAccount bool   `json:"requireAccount"`
	WelcomeMessage string `json:"welcomeMessage,omitempty"`
}

func (s *ShareService) requireOwner(ctx context.Context, boardID string, p *domain.Principal) (*domain.Element, error) {
	board, err := s.elements.Get(ctx, boardID)
	if err != nil {
		return nil, err
	}
	if board.Type != domain.TypeBoard {
		return nil, domain.ErrValidation
	}
	if isHome(board) {
		return nil, domain.ErrHomeBoard // the Home board can never be shared (§3.1)
	}
	role, _, err := s.access.Resolve(ctx, boardID, p)
	if err != nil {
		return nil, err
	}
	if role != RoleOwner {
		return nil, domain.ErrForbidden
	}
	if board.ACL == nil {
		board.ACL = &domain.ACL{OwnerID: p.Sub, Editors: []string{}}
	}
	return board, nil
}

// State returns the current sharing configuration.
func (s *ShareService) State(ctx context.Context, p *domain.Principal, boardID string) (*ShareState, error) {
	board, err := s.requireOwner(ctx, boardID, p)
	if err != nil {
		return nil, err
	}
	return stateFromACL(board.ACL), nil
}

// SetAgentPolicy records the owner's rules for the assistant on this board.
//
// It goes through requireOwner for the same reason sharing does: it decides
// what OTHER people may do here. Until this existed the only consent gate on a
// run was "can the caller edit", so an editor could set autonomy=auto and have
// thirty model-chosen actions land unpreviewed on somebody else's board — and
// the owner had no way to express "no AI on this board" or "preview only",
// which are ordinary requirements for a client-facing or contractual board.
//
// A nil policy clears back to the defaults rather than storing an empty object,
// so "unset" and "explicitly permissive" stay distinguishable in the data.
func (s *ShareService) SetAgentPolicy(ctx context.Context, p *domain.Principal, boardID string, policy *domain.AgentPolicy) (*ShareState, error) {
	board, err := s.requireOwner(ctx, boardID, p)
	if err != nil {
		return nil, err
	}
	if policy != nil {
		switch policy.Allow {
		case domain.AgentAllowUnset, domain.AgentAllowNone, domain.AgentAllowOwner, domain.AgentAllowEditors:
		default:
			return nil, domain.ErrValidation
		}
		switch policy.AutoApply {
		case domain.AgentAllowUnset, domain.AgentAllowNone, domain.AgentAllowOwner, domain.AgentAllowEditors:
		default:
			return nil, domain.ErrValidation
		}
		if policy.DailyCapUSD < 0 {
			return nil, domain.ErrValidation
		}
		if policy.ChargedTo != "" && policy.ChargedTo != "runner" && policy.ChargedTo != "owner" {
			return nil, domain.ErrValidation
		}
	}
	board.ACL.AgentPolicy = policy
	if err := s.elements.SetACL(ctx, boardID, board.ACL); err != nil {
		return nil, err
	}
	return stateFromACL(board.ACL), nil
}

// InviteEditor grants full edit rights to an existing account by email
// (mechanism 1). The invitee is notified with a deep link to the board.
func (s *ShareService) InviteEditor(ctx context.Context, p *domain.Principal, boardID, email string) (*ShareState, error) {
	board, err := s.requireOwner(ctx, boardID, p)
	if err != nil {
		return nil, err
	}
	sub, _, err := s.users.LookupByEmail(ctx, email)
	if err != nil {
		return nil, err
	}
	if sub == board.ACL.OwnerID {
		return stateFromACL(board.ACL), nil
	}
	for _, e := range board.ACL.Editors {
		if e == sub {
			return stateFromACL(board.ACL), nil
		}
	}
	board.ACL.Editors = append(board.ACL.Editors, sub)
	if err := s.elements.SetACL(ctx, boardID, board.ACL); err != nil {
		return nil, err
	}
	// After the write and only after it — the two returns above are no-ops on
	// an ACL that already said this, and a log that records them cannot be used
	// to answer when access was actually granted.
	s.audit.Emit(ctx, p, &domain.AuditEvent{
		Action: "share.editor_invited",
		Target: auditBoard(board),
		Meta:   map[string]any{"editor": sub},
	})
	s.notifier.Notify(ctx, &domain.Notification{
		ID: newToken(12), UserID: sub, Kind: domain.NotifyShare, ActorID: p.Sub,
		BoardID: boardID, Message: p.Name + " shared \"" + board.Title() + "\" with you",
		CreatedAt: time.Now().UTC(),
	})
	return stateFromACL(board.ACL), nil
}

// RemoveEditor revokes a collaborator.
func (s *ShareService) RemoveEditor(ctx context.Context, p *domain.Principal, boardID, sub string) (*ShareState, error) {
	board, err := s.requireOwner(ctx, boardID, p)
	if err != nil {
		return nil, err
	}
	kept := board.ACL.Editors[:0]
	for _, e := range board.ACL.Editors {
		if e != sub {
			kept = append(kept, e)
		}
	}
	board.ACL.Editors = kept
	if err := s.elements.SetACL(ctx, boardID, board.ACL); err != nil {
		return nil, err
	}
	s.audit.Emit(ctx, p, &domain.AuditEvent{
		Action: "share.editor_removed",
		Target: auditBoard(board),
		Meta:   map[string]any{"editor": sub},
	})
	// After the write, never before: a session closed while the ACL still
	// granted access would simply reconnect.
	if s.evictor != nil {
		s.evictor.Evict(boardID, sub)
	}
	return stateFromACL(board.ACL), nil
}

// LinkOptions configures link creation.
type LinkOptions struct {
	Kind           string `json:"kind"` // edit | view
	AllowFeedback  bool   `json:"allowFeedback"`
	RequireAccount bool   `json:"requireAccount"`
	Password       string `json:"password,omitempty"`
	WelcomeMessage string `json:"welcomeMessage,omitempty"`
}

// CreateLink enables an editor link (mechanism 2) or a read-only/view link
// (mechanisms 3–4).
func (s *ShareService) CreateLink(ctx context.Context, p *domain.Principal, boardID string, opts LinkOptions) (*ShareState, error) {
	board, err := s.requireOwner(ctx, boardID, p)
	if err != nil {
		return nil, err
	}
	// What the row will say about the link, filled as the link is built. The
	// TOKEN is never among these keys: it is a bearer credential, and an audit
	// log is read by more people than the Share dialog is.
	meta := map[string]any{"kind": opts.Kind}
	switch opts.Kind {
	case "edit":
		if board.ACL.PublicEditLink == "" {
			board.ACL.PublicEditLink = newToken(24)
		}
	case "view":
		link := &domain.ViewLink{
			Token:          newToken(24),
			AllowFeedback:  opts.AllowFeedback,
			RequireAccount: opts.RequireAccount,
			WelcomeMessage: opts.WelcomeMessage,
		}
		if board.ACL.ViewLink != nil {
			link.Token = board.ACL.ViewLink.Token // regenerating settings keeps the URL stable
		}
		if opts.Password != "" {
			hash, err := bcrypt.GenerateFromPassword([]byte(opts.Password), bcrypt.DefaultCost)
			if err != nil {
				return nil, err
			}
			link.PasswordHash = string(hash)
		}
		board.ACL.ViewLink = link
		meta["allowFeedback"] = link.AllowFeedback
		meta["requireAccount"] = link.RequireAccount
		meta["passwordProtected"] = link.PasswordHash != ""
	default:
		return nil, domain.ErrValidation
	}
	if err := s.elements.SetACL(ctx, boardID, board.ACL); err != nil {
		return nil, err
	}
	s.audit.Emit(ctx, p, &domain.AuditEvent{
		Action: "share.link_created",
		Target: auditBoard(board),
		Meta:   meta,
	})
	return stateFromACL(board.ACL), nil
}

// RevokeLink disables a link kind.
func (s *ShareService) RevokeLink(ctx context.Context, p *domain.Principal, boardID, kind string) (*ShareState, error) {
	board, err := s.requireOwner(ctx, boardID, p)
	if err != nil {
		return nil, err
	}
	// The token has to be read before it is cleared: it is the only handle on the
	// sessions that joined with it.
	revoked := ""
	switch kind {
	case "edit":
		revoked = board.ACL.PublicEditLink
		board.ACL.PublicEditLink = ""
	case "view":
		if board.ACL.ViewLink != nil {
			revoked = board.ACL.ViewLink.Token
		}
		board.ACL.ViewLink = nil
	default:
		return nil, domain.ErrValidation
	}
	if err := s.elements.SetACL(ctx, boardID, board.ACL); err != nil {
		return nil, err
	}
	s.audit.Emit(ctx, p, &domain.AuditEvent{
		Action: "share.link_revoked",
		Target: auditBoard(board),
		Meta:   map[string]any{"kind": kind},
	})
	if s.evictor != nil && revoked != "" {
		s.evictor.EvictByShareToken(boardID, revoked)
	}
	return stateFromACL(board.ACL), nil
}

// ResolveToken maps a share token (from a /shared/:token URL) to its board,
// enforcing the link's own conditions. Used by the public board entry point.
//
// It takes the whole principal now, not a bare password string, because
// requireAccount is one of those conditions and answering it needs to know
// whether anybody is logged in. That flag was written by the sharing dialog,
// stored on the ViewLink, returned to the dialog — and read by nothing. An owner
// who ticked "requires a QomraNote account" got a link anonymous strangers
// opened.
//
// The password comparison itself has moved to AccessResolver, where every other
// route reaches it too; this calls the same code so the two can never disagree
// about what the link grants.
func (s *ShareService) ResolveToken(ctx context.Context, p *domain.Principal, token string) (*domain.Element, string, error) {
	if p == nil {
		p = &domain.Principal{}
	}
	boards, err := s.elements.BoardsByShareToken(ctx, token)
	if err != nil {
		return nil, "", err
	}
	for _, board := range boards {
		if board.ACL == nil {
			continue
		}
		if board.ACL.PublicEditLink == token {
			// Editor links require a logged-in account (§6.1 mechanism 2), the
			// same rule the resolver applies — stated here too so the entry point
			// does not hand out a board id the token cannot then open.
			if p.Sub == "" {
				return nil, "", domain.ErrUnauthorized
			}
			return board, "edit", nil
		}
		if vl := board.ACL.ViewLink; vl != nil && vl.Token == token {
			if !s.access.viewLinkOpen(vl, p) {
				return nil, "", domain.ErrUnauthorized
			}
			return board, "view", nil
		}
	}
	return nil, "", domain.ErrNotFound
}

func stateFromACL(acl *domain.ACL) *ShareState {
	st := &ShareState{
		OwnerID: acl.OwnerID, Editors: acl.Editors, PublicEditLink: acl.PublicEditLink,
		AgentPolicy: acl.AgentPolicy,
	}
	if acl.ViewLink != nil {
		st.ViewLink = &ShareViewLink{
			Token:          acl.ViewLink.Token,
			AllowFeedback:  acl.ViewLink.AllowFeedback,
			RequireAccount: acl.ViewLink.RequireAccount,
			WelcomeMessage: acl.ViewLink.WelcomeMessage,
		}
	}
	if st.Editors == nil {
		st.Editors = []string{}
	}
	return st
}

// newToken returns n bytes of hex-encoded cryptographic randomness.
func newToken(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// TransferOwnership hands a board to one of its editors.
//
// This did not exist, and its absence made a refusal into a lie: deleting an
// account with shared boards is refused with "transfer or unshare them first",
// which instructed the person to perform an action the product had no way to
// perform. Unshare was real; transfer was not. A person whose collaborators
// depended on a board was told to do the one thing that would not destroy their
// work, and then could not do it.
//
// The new owner must ALREADY be an editor. That is the whole safety story: it
// means the board cannot be pushed onto someone who never accepted it, and the
// recipient is by definition someone the owner already chose. The old owner
// keeps edit access rather than being cut out of their own work — leaving is a
// separate decision from handing over, and conflating them would make transfer
// feel like a trapdoor.
func (s *ShareService) TransferOwnership(ctx context.Context, p *domain.Principal, boardID, toSub string) (*ShareState, error) {
	board, err := s.requireOwner(ctx, boardID, p)
	if err != nil {
		return nil, err
	}
	if toSub == "" || toSub == board.ACL.OwnerID {
		return nil, domain.ErrValidation
	}
	if !containsString(board.ACL.Editors, toSub) {
		// Named as its own refusal so the caller can say WHY rather than
		// reporting a bare validation failure on a request that looked right.
		return nil, &notAnEditorError{sub: toSub}
	}

	previous := board.ACL.OwnerID
	board.ACL.OwnerID = toSub
	// The recipient stops being an editor because they are now the owner, and
	// the previous owner takes that place. Editors is a set of people who are
	// NOT the owner; leaving the new owner in it would make every membership
	// check answer twice.
	kept := make([]string, 0, len(board.ACL.Editors))
	for _, e := range board.ACL.Editors {
		if e != toSub {
			kept = append(kept, e)
		}
	}
	if previous != "" && !containsString(kept, previous) {
		kept = append(kept, previous)
	}
	board.ACL.Editors = kept

	if err := s.elements.SetACL(ctx, boardID, board.ACL); err != nil {
		return nil, err
	}
	// Both ends of the handover, because the row has to answer "who owns this
	// now" and "who could have given it away" from itself — the ACL it describes
	// has already been overwritten by the time anybody reads the log.
	s.audit.Emit(ctx, p, &domain.AuditEvent{
		Action: "share.ownership_transferred",
		Target: auditBoard(board),
		Meta:   map[string]any{"from": previous, "to": toSub},
	})
	return stateFromACL(board.ACL), nil
}

// notAnEditorError explains the one refusal a transfer has of its own.
type notAnEditorError struct{ sub string }

func (e *notAnEditorError) Error() string {
	return "a board can only be handed to someone you have already shared it with — " +
		"invite them as an editor first, then transfer"
}
func (e *notAnEditorError) Unwrap() error           { return domain.ErrValidation }
func (e *notAnEditorError) AuthoredMessage() string { return e.Error() }

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
