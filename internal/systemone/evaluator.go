package systemone

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Backend identifiers returned by Evaluator.Backend.
const (
	BackendAPI   = "api"
	BackendLocal = "local"
)

// Evaluator answers typed System One questions against a state.
//
// *Client is the hosted API implementation. A local ONNX evaluator can satisfy
// the same interface without changing Decider or any routing/guardrail caller.
type Evaluator interface {
	Ask(ctx context.Context, state any, questions map[string]Question) (Answers, Usage, error)
	Configured() bool
	Model() string
	Backend() string
}

// Backend reports that *Client talks to the hosted evaluation API.
func (c *Client) Backend() string { return BackendAPI }

// Compile-time check: the hosted HTTP client is an Evaluator.
var _ Evaluator = (*Client)(nil)

func singleChoice(ctx context.Context, eval Evaluator, state any, instructions string, options map[string]string) (Answer, error) {
	if eval == nil {
		return Answer{}, fmt.Errorf("systemone evaluator is nil")
	}
	answers, _, err := eval.Ask(ctx, state, map[string]Question{
		"question": Choice(instructions, options),
	})
	if err != nil {
		return Answer{}, err
	}
	answer, ok := answers["question"]
	if !ok {
		return Answer{}, fmt.Errorf("systemone returned no answer")
	}
	return answer, nil
}

func singleNoul(ctx context.Context, eval Evaluator, state any, instructions string) (Answer, error) {
	if eval == nil {
		return Answer{}, fmt.Errorf("systemone evaluator is nil")
	}
	answers, _, err := eval.Ask(ctx, state, map[string]Question{
		"question": Noul(instructions),
	})
	if err != nil {
		return Answer{}, err
	}
	answer, ok := answers["question"]
	if !ok {
		return Answer{}, fmt.Errorf("systemone returned no answer")
	}
	return answer, nil
}

func rank(ctx context.Context, eval Evaluator, state any, instructions string, candidates []Candidate, topN int) ([]Ranked, error) {
	if eval == nil {
		return nil, fmt.Errorf("systemone evaluator is nil")
	}
	options := make(map[string]string, len(candidates))
	names := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		name := strings.TrimSpace(candidate.Name)
		if name == "" {
			continue
		}
		if _, dup := options[name]; dup {
			continue
		}
		options[name] = strings.TrimSpace(candidate.Description)
		names = append(names, name)
	}
	if len(names) == 0 {
		return nil, nil
	}
	answers, _, err := eval.Ask(ctx, state, map[string]Question{
		"ranking": Choice(instructions, options),
	})
	if err != nil {
		return nil, err
	}
	answer, ok := answers["ranking"]
	if !ok {
		return nil, fmt.Errorf("systemone ranking returned no answer")
	}
	ranked := make([]Ranked, 0, len(names))
	for _, name := range names {
		ranked = append(ranked, Ranked{
			Name:         name,
			Probability:  answer.Probabilities[name],
			Confidence:   answer.Confidence,
			Distribution: answer.Probabilities,
		})
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].Probability != ranked[j].Probability {
			return ranked[i].Probability > ranked[j].Probability
		}
		return ranked[i].Name < ranked[j].Name
	})
	if topN > 0 && len(ranked) > topN {
		ranked = ranked[:topN]
	}
	return ranked, nil
}
