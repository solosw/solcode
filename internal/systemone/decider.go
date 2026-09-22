package systemone

import (
	"context"
	"strings"
)

// Decider wraps a Client with deterministic fallbacks.
//
// Every method is total: it never returns an error and never blocks a caller
// on a network failure. When Jev is disabled, unreachable, slow, or answers
// inside the caller's uncertainty band, the method returns the caller-supplied
// fallback — which must be the conservative choice, not the permissive one.
// This is deliberate: a probabilistic model must never be load-bearing on a
// security or data-loss decision path.
type Decider struct {
	client *Client
	// onError surfaces failures for logging/observability. Optional.
	onError func(error)
}

// NewDecider builds a decider. A nil or unconfigured client yields a decider
// that always answers with the fallback.
func NewDecider(client *Client) *Decider {
	return &Decider{client: client}
}

// WithErrorHandler attaches a callback invoked whenever Jev could not be used.
func (d *Decider) WithErrorHandler(fn func(error)) *Decider {
	if d == nil {
		return nil
	}
	d.onError = fn
	return d
}

// Enabled reports whether Jev can actually be called.
func (d *Decider) Enabled() bool {
	return d != nil && d.client != nil && d.client.Configured()
}

func (d *Decider) report(err error) {
	if d == nil || err == nil || d.onError == nil {
		return
	}
	d.onError(err)
}

// Choose asks Jev to pick one option and returns the selection together with
// its confidence. When Jev is unavailable, or the answer falls below
// minConfidence, it returns fallback with confidence 0.
//
// Callers should treat a confidence of 0 as "decided by fallback" rather than
// as a low-quality Jev answer.
func (d *Decider) Choose(ctx context.Context, state any, instructions string, options map[string]string, minConfidence float64, fallback string) (string, float64) {
	if !d.Enabled() || len(options) == 0 {
		return fallback, 0
	}
	answer, err := d.client.SingleChoice(ctx, state, instructions, options)
	if err != nil {
		d.report(err)
		return fallback, 0
	}
	selected := strings.TrimSpace(answer.Choice)
	if _, ok := options[selected]; !ok || selected == "" {
		// An option we never offered is not usable; prefer the fallback.
		d.report(errUnknownChoice(selected))
		return fallback, 0
	}
	if minConfidence > 0 && answer.Confidence < minConfidence {
		return fallback, answer.Confidence
	}
	return selected, answer.Confidence
}

// Noul asks a yes/no question and thresholds the probability of yes.
//
// The three-band split follows the documented confidence pattern: at or above
// yesAt is a yes, at or below noAt is a no, and anything between the bands is
// genuinely uncertain and returns fallback. Keep fallback conservative — for a
// gate that means "do not act".
func (d *Decider) Noul(ctx context.Context, state any, instructions string, yesAt, noAt float64, fallback bool) (bool, float64) {
	if !d.Enabled() {
		return fallback, 0
	}
	answer, err := d.client.SingleNoul(ctx, state, instructions)
	if err != nil {
		d.report(err)
		return fallback, 0
	}
	probability := answer.Noul
	switch {
	case probability >= yesAt:
		return true, probability
	case probability <= noAt:
		return false, probability
	default:
		return fallback, probability
	}
}

// Answer returns the raw typed answer for one question, for callers that need
// a Score position or a Noul probability rather than a thresholded decision.
//
// It is the one method on Decider that can fail: Score and Noul answers carry
// no usable fallback (a made-up score is worse than no score), so an
// unreachable or disabled Jev surfaces as an error and the caller keeps its own
// default. Callers that need totality should prefer Choose or Noul.
func (d *Decider) Answer(ctx context.Context, state any, instructions string, levels []string) (Answer, error) {
	if !d.Enabled() {
		return Answer{}, errDisabled
	}
	var question Question
	if len(levels) > 0 {
		question = Score(instructions, levels)
	} else {
		question = Noul(instructions)
	}
	answers, _, err := d.client.Ask(ctx, state, map[string]Question{"question": question})
	if err != nil {
		d.report(err)
		return Answer{}, err
	}
	answer, ok := answers["question"]
	if !ok {
		err := errMissingAnswer
		d.report(err)
		return Answer{}, err
	}
	return answer, nil
}

// Rank asks Jev which named candidates fit the state and returns up to topN
// ordered by probability. When Jev is unavailable it returns nil, and callers
// must fall back to their existing lexical or plan-order behavior.
func (d *Decider) Rank(ctx context.Context, state any, instructions string, candidates []Candidate, topN int) []Ranked {
	if !d.Enabled() || len(candidates) == 0 {
		return nil
	}
	ranked, err := d.client.Rank(ctx, state, instructions, candidates, topN)
	if err != nil {
		d.report(err)
		return nil
	}
	return ranked
}

type unknownChoiceError string

func (e unknownChoiceError) Error() string {
	return "systemone returned an unsupported option: " + string(e)
}

func errUnknownChoice(choice string) error { return unknownChoiceError(choice) }

type sentinelError string

func (e sentinelError) Error() string { return string(e) }

const (
	errDisabled      = sentinelError("systemone is disabled")
	errMissingAnswer = sentinelError("systemone returned no answer")
)
