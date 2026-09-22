package engine

import (
	"context"
	"sort"
	"strings"

	"github.com/solosw/solcode/internal/systemone"
	"github.com/solosw/solcode/internal/tool"
)

// WorkflowCandidate is a loaded workflow offered to a router or suggester.
type WorkflowCandidate struct {
	Name        string
	Description string
}

// Router adds a semantic layer on top of the lexical tool/skill selector.
//
// SelectToolsForTurn matches the prompt against tool names and descriptions
// token by token. That works when the user names the thing ("grep for TODO",
// "run the tests") and fails when they describe an outcome instead ("make this
// function less of a mess", "check whether anyone already solved this"). Those
// prompts score zero, no dynamic tools are enabled, and the model is left with
// only the core set.
//
// The router asks Jev one Choice question over the candidates that lexical
// matching could not resolve, and enables the winners. It only runs when
// lexical matching came back empty, so the common case keeps its current
// behavior and pays nothing.
type Router struct {
	Decider *systemone.Decider
	// MinConfidence is the floor for accepting a Jev routing decision.
	MinConfidence float64
	// TopN bounds how many candidates one routing decision may enable.
	TopN int
}

func (r *Router) enabled() bool {
	return r != nil && r.Decider != nil && r.Decider.Enabled()
}

func (r *Router) minConfidence() float64 {
	if r == nil || r.MinConfidence <= 0 || r.MinConfidence > 1 {
		return 0.6
	}
	return r.MinConfidence
}

func (r *Router) topN() int {
	if r == nil || r.TopN <= 0 {
		return 2
	}
	return r.TopN
}

// RouteTools picks tools that lexical matching missed. It returns tool names to
// make sticky for this run. A nil result means "no opinion" — the caller keeps
// the lexical selection untouched.
func (r *Router) RouteTools(ctx context.Context, query string, candidates []tool.Tool) []string {
	query = strings.TrimSpace(query)
	if !r.enabled() || query == "" || len(candidates) == 0 {
		return nil
	}
	ranked := r.Decider.Rank(ctx, query,
		"Which capability would best accomplish what `state` asks for? Choose the single best match, or none if no tool fits.",
		toolCandidates(candidates), r.topN())
	if len(ranked) == 0 {
		return nil
	}
	// `none` is a real answer: the strongest signal was that nothing fits, so
	// stop rather than falling through to a runner-up. It still has to clear the
	// floor, otherwise a barely-leading "none" would suppress a usable tool.
	if ranked[0].Name == routerNoneOption && ranked[0].Probability >= r.minConfidence() {
		return nil
	}
	return r.accepted(ranked)
}

// accepted applies the confidence floor to a ranked list, ordered by
// probability. The floor is on each candidate's own probability: the answer's
// confidence summarizes the whole distribution and is identical for every
// candidate in it, so it cannot rank or filter them.
func (r *Router) accepted(ranked []systemone.Ranked) []string {
	min := r.minConfidence()
	out := make([]string, 0, len(ranked))
	for _, candidate := range ranked {
		if candidate.Name == routerNoneOption {
			continue
		}
		probability := candidate.Probability
		if probability < min {
			break
		}
		out = append(out, candidate.Name)
	}
	return out
}

// RouteSkills picks the skill that best fits the request. It returns "" when
// Jev has no confident answer, so the caller can fall back to letting the model
// read the skill catalog itself.
func (r *Router) RouteSkills(ctx context.Context, query string, skills []SkillInfo) string {
	query = strings.TrimSpace(query)
	if !r.enabled() || query == "" || len(skills) == 0 {
		return ""
	}
	candidates := make([]systemone.Candidate, 0, len(skills)+1)
	for _, skill := range skills {
		name := strings.TrimSpace(skill.Name)
		if name == "" {
			continue
		}
		candidates = append(candidates, systemone.Candidate{
			Name:        name,
			Description: strings.TrimSpace(skill.Description),
		})
	}
	if len(candidates) == 0 {
		return ""
	}
	candidates = append(candidates, systemone.Candidate{
		Name:        routerNoneOption,
		Description: "No listed skill matches; handle the request with ordinary tools",
	})
	ranked := r.Decider.Rank(ctx, query,
		"Which skill should handle what `state` asks for? Choose none if no skill fits.",
		candidates, 1)
	if len(ranked) == 0 || ranked[0].Name == routerNoneOption {
		return ""
	}
	accepted := r.accepted(ranked)
	if len(accepted) == 0 {
		return ""
	}
	return accepted[0]
}

// routerNoneOption gives the model an explicit way to decline. Without it a
// Choice is forced to pick something, and every request would route.
const routerNoneOption = "none"

func toolCandidates(candidates []tool.Tool) []systemone.Candidate {
	out := make([]systemone.Candidate, 0, len(candidates)+1)
	for _, candidate := range candidates {
		if candidate == nil {
			continue
		}
		name := strings.TrimSpace(candidate.Name())
		if name == "" || hiddenFromModel[name] {
			continue
		}
		out = append(out, systemone.Candidate{
			Name:        name,
			Description: strings.TrimSpace(candidate.Description()),
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	// The decline option is always present, even when every candidate was
	// filtered out: the caller checks for it to detect a "nothing fits" answer.
	out = append(out, systemone.Candidate{
		Name:        routerNoneOption,
		Description: "No listed tool fits; do not enable anything extra",
	})
	return out
}

// routerMisses decides whether the semantic router should run, and over what.
//
// It returns the candidates only when lexical matching found nothing useful:
// no non-core tool was enabled and no dynamic match scored. When anything
// matched, the lexical result is already good and asking Jev would just spend a
// request to agree.
func routerMisses(all []tool.Tool, query string, enabled map[string]bool, selected []tool.Tool) []tool.Tool {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil
	}
	// Anything the model already has is a hit, not a miss.
	for name := range enabled {
		name = strings.TrimSpace(name)
		if name != "" && !coreToolNames[name] && !hiddenFromModel[name] {
			return nil
		}
	}
	selectedNames := make(map[string]bool, len(selected))
	for _, candidate := range selected {
		if candidate == nil {
			continue
		}
		name := candidate.Name()
		selectedNames[name] = true
		if !coreToolNames[name] && !hiddenFromModel[name] && tool.CapabilityScore(query, name, candidate.Description()) > 0 {
			return nil
		}
	}
	candidates := routeCandidates(all, selectedNames)
	if len(candidates) < 2 {
		// Routing over zero or one option is not a decision.
		return nil
	}
	return candidates
}

// routeCandidates are the tools lexical matching did not already select, minus
// the core and hidden sets. Routing over tools the model already has would spend
// a request to change nothing.
func routeCandidates(all []tool.Tool, selected map[string]bool) []tool.Tool {
	out := make([]tool.Tool, 0, len(all))
	for _, candidate := range all {
		if candidate == nil {
			continue
		}
		name := candidate.Name()
		if selected[name] || coreToolNames[name] || hiddenFromModel[name] {
			continue
		}
		out = append(out, candidate)
	}
	return out
}
