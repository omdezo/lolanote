package agent

// A visual grammar for connectors.
//
// Every arrow the agent drew was the same arrow: grey, straight, one head. A
// diagram made of identical lines is one you have to read label by label, which
// is exactly the work a diagram exists to save. Giving the relationship a KIND
// and letting the kind decide the drawing means the shape of a process is
// legible before a single word is read — a blocker is red, a dependency points
// backwards, a loose association is a soft dashed line.
//
// The model picks the meaning; the server picks the pixels. Same division as
// the geometry, for the same reason: a model asked to choose hex values and
// stroke weights produces a board that looks like nothing else in the product.

// Relation is the closed set of meanings a connector may carry.
type Relation string

const (
	// RelationLeadsTo is the default: this comes before that. The spine of any
	// process — a solid arrow pointing forward.
	RelationLeadsTo Relation = "leads_to"
	// RelationDependsOn points at what is needed FIRST, so the arrow runs
	// against the flow. Drawn with the head at the source end for that reason.
	RelationDependsOn Relation = "depends_on"
	// RelationBlocks is the one relationship a person needs to see without
	// reading: it is why something is not moving.
	RelationBlocks Relation = "blocks"
	// RelationRelated is an association with no direction — "these belong
	// together" — and is drawn without a head so it is not mistaken for flow.
	RelationRelated Relation = "related"
)

// connectorStyle is how one relation is drawn, in the fields the renderer reads
// from a LINE's content (see LineLayer.tsx).
type connectorStyle struct {
	Color      string
	Weight     int
	Curve      float64
	StartArrow bool
	EndArrow   bool
}

// relationStyles maps meaning to drawing. Colours come from the product's own
// palette rather than from anywhere else, so an agent-drawn connector is
// indistinguishable from a hand-drawn one.
var relationStyles = map[Relation]connectorStyle{
	RelationLeadsTo:   {Color: "#8a86a0", Weight: 2, Curve: 0, EndArrow: true},
	RelationDependsOn: {Color: "#5e5ce6", Weight: 2, Curve: 0, StartArrow: true},
	RelationBlocks:    {Color: "#d6455d", Weight: 3, Curve: 0, EndArrow: true},
	// Curved and headless: an association that ran straight with an arrow was
	// indistinguishable from a step in the process.
	RelationRelated: {Color: "#b9b6c8", Weight: 2, Curve: 28},
}

// ValidRelation reports whether a name is one the server can draw.
func ValidRelation(r Relation) bool {
	_, ok := relationStyles[r]
	return ok
}

// styleFor returns how to draw a relation, defaulting to the plain forward
// arrow. An unknown value draws as the ordinary case rather than failing: a
// connector with a strange name is still a connector the person asked for.
func styleFor(r Relation) connectorStyle {
	if s, ok := relationStyles[r]; ok {
		return s
	}
	return relationStyles[RelationLeadsTo]
}

// relationEnum lists the kinds, in a stable order, for the tool schema. Stable
// because the prompt is cached and a reordered enum invalidates it.
func relationEnum() []string {
	return []string{
		string(RelationLeadsTo), string(RelationDependsOn),
		string(RelationBlocks), string(RelationRelated),
	}
}

// arrowFor renders a relation as the glyph the review list shows between the
// two endpoints, so a plan row says what KIND of connection it is without a
// second column for it.
func arrowFor(r Relation) string {
	switch r {
	case RelationDependsOn:
		return "←"
	case RelationBlocks:
		return "⊘"
	case RelationRelated:
		return "↔"
	default:
		return "→"
	}
}

// connectLabel reads the connector's caption, tolerating either field name.
// The schema says "label"; models that have seen the other create tools reach
// for "title" often enough that refusing it would just lose the caption.
func connectLabel(in *toolArgs) string {
	if in.Label != "" {
		return in.Label
	}
	return in.Title
}
