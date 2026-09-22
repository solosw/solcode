package systemone

import (
	"context"
	"strings"
)

// Guardrail flags inputs that look unsafe before a tool runs.
//
// This is a defence-in-depth check, not a security boundary. It cannot see
// whether a command is authorised, it does not understand the sandbox, and it
// is a probabilistic model sitting on an external API. The permission service
// and the sandbox remain the controls that actually stop damage; the guardrail
// exists to catch the obvious case earlier and to route genuinely ambiguous
// inputs to a human.
//
// Accordingly every path fails closed: if Jev is unreachable the input is not
// silently allowed, it is returned as uncertain so the caller can escalate
// rather than proceed.
type Guardrail struct {
	Decider *Decider
	// YesAt is the probability above which the input is treated as unsafe.
	// Higher means fewer false alarms and more misses.
	YesAt float64
}

// Verdict is the outcome of a guardrail check.
type Verdict struct {
	// Unsafe is true when the check is confident the input is risky.
	Unsafe bool
	// Uncertain is true when the check could not be completed or the answer
	// sat in the middle band. Callers should treat it as "ask a human", not as
	// "allowed".
	Uncertain bool
	// Probability is the model's probability that the input is unsafe. Zero
	// when the check could not run.
	Probability float64
	// Reason names which check fired, for the audit trail.
	Reason string
}

// Allowed reports whether the input may proceed without escalation. Only a
// confident "not unsafe" answer is allowed through.
func (v Verdict) Allowed() bool {
	return !v.Unsafe && !v.Uncertain
}

func (g *Guardrail) yesAt() float64 {
	if g == nil || g.YesAt <= 0 || g.YesAt > 1 {
		return 0.8
	}
	return g.YesAt
}

// CheckToolInput inspects a tool call before it runs.
//
// kind and payload describe what is about to happen; payload is the raw tool
// input (a command, a diff, a file body). An empty payload returns an allowed
// verdict without a request.
func (g *Guardrail) CheckToolInput(ctx context.Context, toolName, payload string) Verdict {
	payload = strings.TrimSpace(payload)
	if g == nil || g.Decider == nil || !g.Decider.Enabled() || payload == "" {
		// No guardrail configured: this is not an uncertain result, it is the
		// absence of the check. The permission service still governs the call.
		return Verdict{}
	}

	state := map[string]any{
		"tool":    strings.TrimSpace(toolName),
		"payload": truncateRunes(payload, 8000),
	}

	// One Noul per hazard, asked together. Separate questions keep each
	// judgment about a single thing, which is what makes a Noul interpretable.
	hazards := []struct {
		id           string
		instructions string
	}{
		{"secrets", "Does `payload` contain a credential, API key, token, password, private key, or private customer data that would be exposed by running it?"},
		{"destructive", "Would running `payload` irreversibly destroy data or system state outside the project working directory, such as deleting user files, dropping a database, or rewriting disk partitions?"},
		{"exfiltration", "Does `payload` send project data, credentials, or user files to an external host?"},
		{"privilege", "Does `payload` escalate privileges or weaken security controls, such as disabling a firewall, changing permissions broadly, or modifying authentication?"},
	}

	questions := make(map[string]Question, len(hazards))
	for _, hazard := range hazards {
		questions[hazard.id] = Noul(hazard.instructions)
	}

	answers, _, err := g.Decider.rawAsk(ctx, state, questions)
	if err != nil {
		// The check could not run. Fail closed: escalate rather than allow.
		return Verdict{Uncertain: true, Reason: "guardrail unavailable: " + err.Error()}
	}

	verdict := Verdict{}
	for _, hazard := range hazards {
		answer, ok := answers[hazard.id]
		if !ok {
			verdict.Uncertain = true
			if verdict.Reason == "" {
				verdict.Reason = "guardrail returned no answer for " + hazard.id
			}
			continue
		}
		probability := answer.Noul
		if probability > verdict.Probability {
			verdict.Probability = probability
		}
		if probability >= g.yesAt() {
			verdict.Unsafe = true
			if verdict.Reason == "" {
				verdict.Reason = hazard.id
			}
		}
	}
	if !verdict.Unsafe && verdict.Probability > 1-g.yesAt() {
		// In the middle band: too probable to ignore, not probable enough to
		// call it unsafe. Escalate instead of guessing.
		verdict.Uncertain = true
		if verdict.Reason == "" {
			verdict.Reason = "hazard probability in the uncertain band"
		}
	}
	return verdict
}

// rawAsk exposes the client's Ask through the decider so guardrail and other
// multi-question callers share one cache and one error path.
func (d *Decider) rawAsk(ctx context.Context, state any, questions map[string]Question) (Answers, Usage, error) {
	if d == nil || d.client == nil {
		return nil, Usage{}, errDisabled
	}
	return d.client.Ask(ctx, state, questions)
}

func truncateRunes(text string, max int) string {
	if max <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max]) + "…"
}
