package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"qomranote/backend/internal/domain"
)

// recent_changes was keyed to the ROOT board and only the root board.
//
// The client stamps every human transaction with whichever board is currently
// OPEN. The moment an organizing run moves the person's daily work down into
// `Pre-Production`, their edits carry `Pre-Production`'s id while the agent's own
// commits carry the root's — so the tool built to answer "what did the humans do"
// showed the agent mostly its own work, confidently, with no sign of the gap.

// txnLog is a TransactionRepository that answers per board, so a test can prove
// the tool asked about more than one.
type txnLog struct {
	byBoard map[string][]*domain.Transaction
	asked   []string
}

func (l *txnLog) Insert(context.Context, *domain.Transaction) error { return nil }
func (l *txnLog) Get(context.Context, string) (*domain.Transaction, error) {
	return nil, domain.ErrNotFound
}
func (l *txnLog) ListByBoard(_ context.Context, boardID string, limit int) ([]*domain.Transaction, error) {
	l.asked = append(l.asked, boardID)
	rows := l.byBoard[boardID]
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

func historyStaging(log *txnLog) *staging {
	s := capStaging()
	s.txns = log
	s.scope.Elements["nested"] = &domain.Element{ID: "nested", Type: domain.TypeBoard,
		Content:  domain.Content{"title": "Pre-Production"},
		Location: domain.Location{ParentID: "b1", Section: domain.SectionCanvas}}
	return s
}

func txnAt(id, board, origin string, ago time.Duration) *domain.Transaction {
	return &domain.Transaction{ID: id, BoardID: board, Origin: origin,
		CreatedAt: time.Now().UTC().Add(-ago),
		Ops:       []domain.Op{{ElementID: "e" + id, Action: domain.ActionUpdate}}}
}

func TestHistory_ReadsTheWholeSubtreeNotJustTheRoot(t *testing.T) {
	log := &txnLog{byBoard: map[string][]*domain.Transaction{
		"b1":     {txnAt("agent-1", "b1", domain.OriginAgent, time.Hour)},
		"nested": {txnAt("human-1", "nested", "", 10*time.Minute)},
	}}
	s := historyStaging(log)
	out := s.runHistory(context.Background(), &toolArgs{}, &reply{staging: s})

	if out.IsError {
		t.Fatalf("recent_changes failed: %s", out.Content)
	}
	if !strings.Contains(out.Content, "nested") && !strings.Contains(out.Content, "e human-1") &&
		len(log.asked) < 2 {
		t.Fatalf("only %v was read — the humans' own work lives in the nested board",
			log.asked)
	}
	asked := strings.Join(log.asked, ",")
	if !strings.Contains(asked, "b1") || !strings.Contains(asked, "nested") {
		t.Fatalf("boards read = %v, want both the root and the nested board", log.asked)
	}
	if !strings.Contains(out.Content, fmt.Sprintf("across %d board(s)", len(log.asked))) {
		t.Errorf("the merged answer does not say how wide it looked (%d boards read):\n%s",
			len(log.asked), out.Content)
	}
	// Both sides of the log survive the merge. The whole point is that the
	// humans' work and the agent's own live on different boards.
	if !strings.Contains(out.Content, "human") || !strings.Contains(out.Content, "agent") {
		t.Errorf("the merged answer lost one side of the log:\n%s", out.Content)
	}
}

// "since I last looked" has to be expressible, or every answer is the whole log.
func TestHistory_TheSinceWindowIsHonoured(t *testing.T) {
	log := &txnLog{byBoard: map[string][]*domain.Transaction{
		"b1": {
			txnAt("recent", "b1", "", 30*time.Minute),
			txnAt("ancient", "b1", "", 40*24*time.Hour),
		},
	}}
	s := historyStaging(log)
	out := s.runHistory(context.Background(), &toolArgs{When: "week"}, &reply{staging: s})
	if strings.Contains(out.Content, "1 month ago") || strings.Contains(out.Content, "ago · ·") {
		t.Errorf("a row older than the window survived:\n%s", out.Content)
	}
	if !strings.Contains(out.Content, "since ") {
		t.Errorf("the answer does not state the horizon it covered:\n%s", out.Content)
	}
	bad := s.runHistory(context.Background(), &toolArgs{When: "sometime last spring"}, &reply{staging: s})
	if !bad.IsError {
		t.Error("an unparseable date was silently treated as 'everything', which is the " +
			"answer most likely to be believed and most likely to be wrong")
	}
}

// A truncated history read as complete is how an agent tells you nothing
// happened.
func TestHistory_SaysWhenItHitItsOwnCeiling(t *testing.T) {
	var rows []*domain.Transaction
	for i := 0; i < maxHistoryRows+5; i++ {
		rows = append(rows, txnAt("t", "b1", "", time.Duration(i)*time.Minute))
	}
	log := &txnLog{byBoard: map[string][]*domain.Transaction{"b1": rows}}
	s := historyStaging(log)
	out := s.runHistory(context.Background(), &toolArgs{}, &reply{staging: s})
	if !strings.Contains(out.Content, "as far back as this read goes") {
		t.Errorf("the read hit its ceiling and reported a complete history:\n%s", out.Content)
	}
}
