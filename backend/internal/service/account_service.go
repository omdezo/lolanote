package service

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"go.uber.org/zap"

	"qomranote/backend/internal/domain"
)

// AccountService owns everything behind the Settings dialog: profile edits,
// email/password changes (written through to Keycloak so the identity stays
// the source of truth), per-user settings, the privacy data export, and full
// account deletion.
type AccountService struct {
	users         domain.UserRepository
	elements      domain.ElementRepository
	labels        domain.LabelRepository
	attachments   domain.AttachmentRepository
	notifications domain.NotificationRepository
	accounts      domain.AccountManager // nil when the Keycloak admin client is not configured
	audit         *AuditService
	purgers       []TenantPurger
	blobs         BlobRemover                  // optional
	comments      domain.CommentRepository     // optional
	journal       domain.TransactionRepository // optional
	collector     *Collector                   // optional
	log           *zap.Logger
}

// AttachCollector lets account deletion strip the destroyed boards' content out
// of journal rows a COLLABORATOR wrote, which the by-author purge cannot reach.
func (s *AccountService) AttachCollector(c *Collector) { s.collector = c }

// TenantPurger removes a user's data from a subsystem that owns its own
// storage. Account deletion means ALL of the user's data, so any subsystem that
// keeps its own collections registers here rather than being forgotten.
type TenantPurger interface {
	PurgeTenant(ctx context.Context, tenant string) error
}

// AttachmentLister enumerates a person's uploads so the BYTES can go before the
// rows that name them do.
type AttachmentLister interface {
	ListByOwner(ctx context.Context, ownerSub string) ([]*domain.Attachment, error)
}

// ThreadCommentPurger is the comment store's forget half. Named narrowly and
// reached by assertion because the port itself has none: §4.17's "messages
// cannot be removed from a thread once posted" is a rule about what a PERSON may
// do, and it had been implemented as the storage layer being unable to.
type ThreadCommentPurger interface {
	DeleteByThread(ctx context.Context, threadID string) error
	DeleteByAuthor(ctx context.Context, sub string) error
}

// AttachPurger registers a subsystem to purge on account deletion.
func (s *AccountService) AttachPurger(p TenantPurger) { s.purgers = append(s.purgers, p) }

// AttachBlobs lets account deletion remove the stored bytes, not merely the rows
// that name them.
func (s *AccountService) AttachBlobs(b BlobRemover) { s.blobs = b }

// AttachComments lets account deletion reach the one collection with no delete
// method at the interface level.
func (s *AccountService) AttachComments(c domain.CommentRepository) { s.comments = c }

// AttachJournal lets account deletion reach the transaction log — the collection
// that holds full content snapshots of everything the person ever wrote, keyed
// by their own userId, and that no purge path has ever touched.
func (s *AccountService) AttachJournal(t domain.TransactionRepository) { s.journal = t }

// NewAccountService constructs the service.
func NewAccountService(
	users domain.UserRepository,
	elements domain.ElementRepository,
	labels domain.LabelRepository,
	attachments domain.AttachmentRepository,
	notifications domain.NotificationRepository,
	accounts domain.AccountManager,
	audit *AuditService,
	log *zap.Logger,
) *AccountService {
	return &AccountService{
		users: users, elements: elements, labels: labels,
		attachments: attachments, notifications: notifications,
		accounts: accounts, audit: audit, log: log.Named("account"),
	}
}

var emailPattern = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// ProfilePatch is the mutable slice of the profile. Empty fields are ignored;
// AvatarURL supports explicit clearing via the "-" sentinel.
type ProfilePatch struct {
	DisplayName string `json:"displayName"`
	Email       string `json:"email"`
	AvatarURL   string `json:"avatarUrl"`
}

// UpdateProfile applies name/email/avatar changes to Keycloak (when managed)
// and the local mirror, returning the fresh user.
func (s *AccountService) UpdateProfile(ctx context.Context, p *domain.Principal, patch ProfilePatch) (*domain.User, error) {
	u, err := s.users.GetBySub(ctx, p.Sub)
	if err != nil {
		return nil, err
	}

	patch.DisplayName = strings.TrimSpace(patch.DisplayName)
	patch.Email = strings.TrimSpace(strings.ToLower(patch.Email))
	if patch.Email != "" && !emailPattern.MatchString(patch.Email) {
		return nil, domain.ErrValidation
	}
	if patch.Email != "" && patch.Email != u.Email {
		// Refuse an address another local account already mirrors.
		if other, err := s.users.GetByEmail(ctx, patch.Email); err == nil && other.KeycloakSub != p.Sub {
			return nil, domain.ErrConflict
		}
	}

	// Write through to the identity server first — it is authoritative, and a
	// failure there must not desync the mirror.
	if s.accounts != nil && (patch.DisplayName != "" || patch.Email != "") {
		first, last := splitName(patch.DisplayName)
		if err := s.accounts.UpdateProfile(ctx, p.Sub, first, last, patch.Email); err != nil {
			return nil, err
		}
	}

	if patch.DisplayName != "" {
		u.DisplayName = patch.DisplayName
	}
	if patch.Email != "" {
		u.Email = patch.Email
	}
	switch patch.AvatarURL {
	case "":
	case "-":
		u.AvatarURL = ""
	default:
		u.AvatarURL = patch.AvatarURL
	}
	if err := s.users.Update(ctx, u); err != nil {
		return nil, err
	}
	return u, nil
}

// splitName maps a display name onto Keycloak's first/last fields.
func splitName(display string) (first, last string) {
	parts := strings.Fields(display)
	if len(parts) == 0 {
		return "", ""
	}
	if len(parts) == 1 {
		return parts[0], ""
	}
	return parts[0], strings.Join(parts[1:], " ")
}

// ChangePassword verifies the current password, then sets the new one.
// Requires the Keycloak admin client (manage-users).
func (s *AccountService) ChangePassword(ctx context.Context, p *domain.Principal, current, next string) error {
	if s.accounts == nil {
		return domain.ErrUnavailable
	}
	if len(next) < 8 {
		return domain.ErrValidation
	}
	u, err := s.users.GetBySub(ctx, p.Sub)
	if err != nil {
		return err
	}
	if err := s.accounts.VerifyPassword(ctx, u.Email, current); err != nil {
		return domain.ErrUnauthorized
	}
	return s.accounts.SetPassword(ctx, p.Sub, next)
}

// Settings returns the user's effective settings (defaults filled in).
func (s *AccountService) Settings(ctx context.Context, p *domain.Principal) (*domain.UserSettings, error) {
	u, err := s.users.GetBySub(ctx, p.Sub)
	if err != nil {
		return nil, err
	}
	settings := u.EffectiveSettings()
	return &settings, nil
}

// UpdateSettings merges a JSON patch over the current effective settings —
// unmarshalling into the prefilled struct gives natural merge semantics
// (absent fields keep their value) — then normalizes and persists.
func (s *AccountService) UpdateSettings(ctx context.Context, p *domain.Principal, patch json.RawMessage) (*domain.UserSettings, error) {
	u, err := s.users.GetBySub(ctx, p.Sub)
	if err != nil {
		return nil, err
	}
	settings := u.EffectiveSettings()
	if err := json.Unmarshal(patch, &settings); err != nil {
		return nil, domain.ErrValidation
	}
	settings.Normalize()
	if err := s.users.UpdateSettings(ctx, p.Sub, &settings); err != nil {
		return nil, err
	}
	return &settings, nil
}

// DataExport is the "download my data" payload (privacy tab).
type DataExport struct {
	ExportedAt time.Time         `json:"exportedAt"`
	User       *domain.User      `json:"user"`
	Boards     []*domain.Element `json:"boards"`
	Elements   []*domain.Element `json:"elements"`
	Labels     []*domain.Label   `json:"labels"`
	// Attachments names the files this archive refers to. It replaces the
	// presigned URLs that used to sit in content.url — signed credentials for
	// direct bucket access, downloaded into a file the person then mails to
	// themselves, outliving every revocation by a week.
	Attachments []ExportedAttachment `json:"attachments"`
}

// ExportData bundles everything the user owns into one JSON document.
func (s *AccountService) ExportData(ctx context.Context, p *domain.Principal) (*DataExport, error) {
	u, err := s.users.GetBySub(ctx, p.Sub)
	if err != nil {
		return nil, err
	}
	boards, err := s.elements.OwnedBoards(ctx, p.Sub, false)
	if err != nil {
		return nil, err
	}
	// Even the owner's own archive stops carrying live share tokens. A downloaded
	// export outlives every revocation, so a token inside one is a bearer
	// credential sitting in a file the person will mail to themselves.
	export := &DataExport{ExportedAt: time.Now().UTC(), User: u, Boards: redactAll(boards, RoleOwner)}
	seen := map[string]bool{}
	for _, b := range boards {
		seen[b.ID] = true
	}
	for _, b := range boards {
		desc, err := s.elements.Descendants(ctx, b.ID, false)
		if err != nil {
			return nil, err
		}
		for _, el := range desc {
			if !seen[el.ID] {
				seen[el.ID] = true
				export.Elements = append(export.Elements, redact(el, RoleOwner))
			}
		}
	}
	if labels, err := s.labels.ListByOwner(ctx, p.Sub); err == nil {
		export.Labels = labels
	}
	// The files by name and size rather than by signed URL, and the boards too —
	// a board element can carry an attachment reference the same way a card can.
	all := append(append([]*domain.Element{}, export.Boards...), export.Elements...)
	export.Attachments = attachmentManifest(ctx, s.attachments, all)
	stripCredentialURLs(all)
	return export, nil
}

// ErrSharedBoardsBlockDeletion refuses an account deletion that would take other
// people's work with it. The message is the actionable half — a refusal that
// does not say which boards is a dead end.
var ErrSharedBoardsBlockDeletion = &sharedBoardsError{}

type sharedBoardsError struct {
	msg string
}

func (e *sharedBoardsError) Error() string {
	if e.msg == "" {
		return "some of your boards are shared with other people — transfer or unshare them first"
	}
	return e.msg
}
func (e *sharedBoardsError) Unwrap() error { return domain.ErrConflict }

// AuthoredMessage marks this refusal's text as written for the person who will
// read it, so the HTTP boundary puts it on the wire instead of "conflict".
func (e *sharedBoardsError) AuthoredMessage() string { return e.Error() }

// DeleteAccount purges everything the user owns, then removes the identity.
// Content the user contributed to other people's boards stays (their boards,
// their data); the departing user is only stripped from ACLs.
func (s *AccountService) DeleteAccount(ctx context.Context, p *domain.Principal) error {
	boards, err := s.elements.OwnedBoards(ctx, p.Sub, true)
	if err != nil {
		return err
	}

	// Deleting an account HARD-deleted every board the person owned, with its
	// entire subtree — the director's shot lists, the producer's budget tables,
	// the DoP's lookbook — not into the 90-day trash, not exportable afterwards,
	// with no warning to the people losing it and no notification after. The
	// board's ACL decides ownership; CreatedBy is per element, so "my board"
	// silently means "everything four people made in it".
	//
	// It refuses. Silently destroying other people's work to satisfy a
	// self-service delete is not defensible under any privacy reading, and the
	// person's own data is still fully deletable the moment those boards are
	// handed over or unshared. This is deliberately the SAFE half of the fix:
	// ownership transfer is the other half and does not exist yet, so the refusal
	// has to name the boards or it is just a wall.
	if shared := sharedBoardNames(boards); len(shared) > 0 {
		return &sharedBoardsError{msg: sharedBoardsMessage(shared)}
	}

	for _, b := range boards {
		desc, err := s.elements.Descendants(ctx, b.ID, true)
		if err != nil {
			return err
		}
		ids := make([]string, 0, len(desc)+1)
		for _, el := range desc {
			ids = append(ids, el.ID)
		}
		ids = append(ids, b.ID)
		// The messages on a thread outlive the thread unless somebody takes them
		// with it, and until now nobody could: the port had no delete.
		s.purgeThreadComments(ctx, append(desc, b))
		if err := s.elements.HardDelete(ctx, ids); err != nil {
			return err
		}
		// The journal is purged by AUTHOR further down, which reaches the rows
		// this person wrote and not the ones a collaborator wrote on the board
		// being destroyed — and those carry the same content verbatim. Redacting
		// by element closes that half. The blobs are handled by owner below, so
		// nothing is passed here.
		if s.collector != nil {
			s.collector.Collect(ctx, ids, nil)
		}
	}
	if err := s.elements.RemoveEditorEverywhere(ctx, p.Sub); err != nil {
		return err
	}
	// Best-effort side data; the account row and identity are what matter.
	if err := s.labels.DeleteByOwner(ctx, p.Sub); err != nil {
		s.log.Warn("purge labels", zap.Error(err))
	}
	// The BYTES first, then the rows that name them — in that order, because the
	// row is the only thing that knows the key. Reversed, "this permanently
	// deletes all uploaded files" becomes unauditable non-deletion: the files
	// stay in the bucket, still fetchable through an unauthenticated route, and
	// nothing left in the database can even enumerate what survived.
	s.purgeBlobs(ctx, p.Sub)
	if err := s.attachments.DeleteByOwner(ctx, p.Sub); err != nil {
		s.log.Warn("purge attachments", zap.Error(err))
	}
	if err := s.notifications.DeleteByUser(ctx, p.Sub); err != nil {
		s.log.Warn("purge notifications", zap.Error(err))
	}
	s.purgeAuthoredComments(ctx, p.Sub)
	s.purgeJournal(ctx, p.Sub)
	for _, purger := range s.purgers {
		if err := purger.PurgeTenant(ctx, p.Sub); err != nil {
			s.log.Warn("purge subsystem data", zap.Error(err))
		}
	}
	if err := s.users.Delete(ctx, p.Sub); err != nil {
		return err
	}
	if s.accounts != nil {
		if err := s.accounts.DeleteUser(ctx, p.Sub); err != nil {
			// The identity outliving the data is recoverable (next login
			// bootstraps a fresh empty account) — log loudly, don't fail.
			s.log.Error("keycloak user deletion failed after data purge",
				zap.String("sub", p.Sub), zap.Error(err))
		}
	}
	// Last, once the purge has actually run: an event written on entry would
	// record every refusal and every mid-way failure above as a deletion. The row
	// survives the account it describes on purpose — after this call the sub is
	// the only handle anyone has on what was removed, and "prove this account was
	// deleted, and when" is the question a data-protection request asks.
	s.audit.Emit(ctx, p, &domain.AuditEvent{
		Action: "data.account_deleted",
		Target: domain.AuditTarget{Type: "account", ID: p.Sub},
		Meta:   map[string]any{"boards": len(boards)},
	})
	s.log.Info("account deleted", zap.String("sub", p.Sub))
	return nil
}

// sharedBoardNames is the boards whose deletion would take somebody else's work.
func sharedBoardNames(boards []*domain.Element) []string {
	var out []string
	for _, b := range boards {
		if b.ACL == nil || len(b.ACL.Editors) == 0 {
			continue
		}
		title := b.Title()
		if title == "" {
			title = "Untitled board"
		}
		out = append(out, title)
	}
	return out
}

// sharedBoardsMessage names them, because "you cannot delete your account" with
// no reason and no list is a wall rather than a refusal.
func sharedBoardsMessage(titles []string) string {
	shown := titles
	suffix := ""
	if len(shown) > 5 {
		suffix = fmt.Sprintf(" and %d more", len(shown)-5)
		shown = shown[:5]
	}
	noun := "boards are"
	if len(titles) == 1 {
		noun = "board is"
	}
	return fmt.Sprintf("%d %s shared with other people and deleting your account would delete their work too: %s%s. Unshare them first.",
		len(titles), noun, strings.Join(shown, ", "), suffix)
}

// purgeBlobs removes the stored bytes for everything this person uploaded.
//
// Failures are collected and logged loudly rather than swallowed: a partial
// purge reporting success is the bug this exists to fix, and the log line is the
// only remaining trace of a file the database can no longer name.
func (s *AccountService) purgeBlobs(ctx context.Context, sub string) {
	lister, ok := s.attachments.(AttachmentLister)
	if !ok || s.blobs == nil {
		if s.blobs == nil {
			s.log.Warn("no blob remover configured — uploaded files will outlive this account",
				zap.String("sub", sub))
		}
		return
	}
	rows, err := lister.ListByOwner(ctx, sub)
	if err != nil {
		s.log.Error("could not enumerate uploads before deleting their rows — the files will be unreachable and undeleted",
			zap.String("sub", sub), zap.Error(err))
		return
	}
	failed := 0
	for _, a := range rows {
		if err := s.blobs.Remove(a.Key); err != nil {
			failed++
			s.log.Error("blob survived account deletion", zap.String("key", a.Key), zap.Error(err))
		}
	}
	s.log.Info("uploads purged", zap.Int("removed", len(rows)-failed), zap.Int("failed", failed))
}

// purgeThreadComments takes a purged thread's messages with it.
func (s *AccountService) purgeThreadComments(ctx context.Context, els []*domain.Element) {
	purger, ok := s.comments.(ThreadCommentPurger)
	if !ok {
		return
	}
	for _, el := range els {
		if el.Type != domain.TypeCommentThread {
			continue
		}
		if err := purger.DeleteByThread(ctx, el.ID); err != nil {
			s.log.Warn("thread messages outlived their thread",
				zap.String("thread", el.ID), zap.Error(err))
		}
	}
}

// purgeAuthoredComments removes what this person wrote on OTHER people's boards.
//
// Reachable only from here, never from a UI verb, so "you cannot unsay a
// comment" stays true for users and stops being true for the storage layer.
// Without it the AuthorID pointed at a user row that had just been deleted, and
// the messages persisted as permanently orphaned, permanently un-attributable
// text whose reaction maps still held live users' subject ids.
func (s *AccountService) purgeAuthoredComments(ctx context.Context, sub string) {
	purger, ok := s.comments.(ThreadCommentPurger)
	if !ok {
		s.log.Warn("no comment purger configured — this account's messages will outlive it",
			zap.String("sub", sub))
		return
	}
	if err := purger.DeleteByAuthor(ctx, sub); err != nil {
		s.log.Error("comments survived account deletion", zap.String("sub", sub), zap.Error(err))
	}
}

// purgeJournal removes the transaction rows this person authored.
func (s *AccountService) purgeJournal(ctx context.Context, sub string) {
	if s.journal == nil {
		s.log.Warn("no journal purger configured — full content snapshots will outlive this account",
			zap.String("sub", sub))
		return
	}
	purger, ok := s.journal.(TransactionPurger)
	if !ok {
		return
	}
	if err := purger.DeleteByUser(ctx, sub); err != nil {
		s.log.Error("transaction journal survived account deletion",
			zap.String("sub", sub), zap.Error(err))
	}
}
