package domain

import "time"

// Action is what an op does to its element.
type Action string

const (
	ActionCreate  Action = "create"
	ActionUpdate  Action = "update"
	ActionMove    Action = "move"
	ActionDelete  Action = "delete" // soft delete → Trash
	ActionRestore Action = "restore"
)

// Op is one element mutation. Changes is a JSON-merge-patch-style forward
// patch; UndoChanges is its precomputed inverse, replayed by clients for
// undo/redo. The same payload that mutates locally is what gets broadcast —
// one code path for local and remote mutations (§9.5, §9.9).
type Op struct {
	ElementID   string  `bson:"elementId" json:"elementId"`
	Action      Action  `bson:"action" json:"action"`
	Changes     Content `bson:"changes,omitempty" json:"changes,omitempty"`
	UndoChanges Content `bson:"undoChanges,omitempty" json:"undoChanges,omitempty"`
}

// Transaction is the unit of mutation, undo, and broadcast. A multi-select
// drag of N cards is ONE transaction with N ops, not N transactions.
type Transaction struct {
	ID        string    `bson:"_id" json:"id"`
	BoardID   string    `bson:"boardId" json:"boardId"`
	UserID    string    `bson:"userId" json:"userId"`
	ClientID  string    `bson:"clientId" json:"clientId"` // originating socket; excluded from echo
	Ops       []Op      `bson:"ops" json:"ops"`
	CreatedAt time.Time `bson:"createdAt" json:"createdAt"`
}
