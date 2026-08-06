package agent

import (
	"errors"
	"fmt"
	"strings"
)

// quotas is every limit a single run is held to, in one place.
//
// These were seven counters spread across the staging struct, each compared
// against a constant at whichever call site happened to increment it. Nothing
// could answer "what does this agent actually limit?" without grepping, the
// refusal wording was rewritten per site, and adding a capability meant
// remembering that limits were a thing — which is how `comment` and `ask`
// ended up enforced with bare booleans while the others counted.
//
// Each limit exists for a stated reason. They are not tuning knobs; they are
// the shape of what one run is allowed to be.
type quotas struct {
	labels      counter
	images      counter
	urls        counter
	connections counter
	comments    counter
	questions   counter

	// outOfScope is not a limit but a tally: ids the model named that it was
	// never shown. Every one is dropped, and the count is reported.
	outOfScope int
	// lifted counts the subset of those ids that appear in the board's own TEXT.
	//
	// This is the distinction that matters and the original tally did not make.
	// An id written in a card is the signature of an injection — content trying
	// to hand the agent a target. An id that appears nowhere on the board is the
	// model mis-remembering one, which is a competence problem and not an
	// attack. Quarantining on the combined count told a person their board had
	// "repeatedly tried to redirect this run" when nothing of the sort had
	// happened, which is a serious thing to say wrongly.
	lifted int
}

// counter is one limit and how much of it has been used.
type counter struct {
	used, max int
	// refusal is what the model is told when it asks for one too many. Written
	// to be actionable: "you have enough, do this instead" rather than "no".
	refusal string
}

// take spends one unit, or explains why it cannot.
//
// The limit is substituted only when the refusal asks for it. Passing it
// unconditionally to a message with no verb appends Go's "%!(EXTRA int=1)" to
// the text — which would go straight into the model's context as noise, on the
// one turn where the message needs to be understood exactly.
func (c *counter) take() error {
	if c.used >= c.max {
		if strings.Contains(c.refusal, "%d") {
			return fmt.Errorf(c.refusal, c.max)
		}
		return errors.New(c.refusal)
	}
	c.used++
	return nil
}

func newQuotas() quotas {
	return quotas{
		// Reuse is nearly always the better answer than coining a term. "Tag
		// everything" should not spray a taxonomy nobody asked for.
		labels: counter{max: maxNewLabelsPerRun,
			refusal: "that is enough new labels for one run (%d); reuse one of the existing ones"},
		// Each image is real tokens and real latency, and a model that has seen
		// six is not usually short of context.
		images: counter{max: maxImagesPerRun,
			refusal: "that is enough images for one run (%d); work from what you have seen"},
		// Each fetch is a request this server makes on the user's behalf to
		// somewhere it does not control.
		urls: counter{max: maxURLsPerRun,
			refusal: "that is enough pages for one run (%d); work from what you have read"},
		// Asked to "connect related ideas", an unbounded model draws N-squared
		// edges and produces a hairball strictly worse than no lines at all.
		connections: counter{max: maxConnectionsPerRun,
			refusal: "that is enough connections for one run (%d) — keep only the ones that carry meaning"},
		// Notes left where the work is. A run that annotates everything is
		// writing a report on the canvas, so the ceiling stays low — but one was
		// too low once a note can be anchored beside the card it concerns:
		// three decisions on three sub-boards is a normal organizing run, and
		// with a budget of one the other two had nowhere to go.
		comments: counter{max: 3,
			refusal: "that is enough notes on the board for one run (%d); put the rest in your summary"},
		// An agent that can ask twice will ask forever, and the bias has to
		// stay toward attempting.
		questions: counter{max: 1,
			refusal: "you have already asked; make your best attempt and say what you assumed"},
	}
}
