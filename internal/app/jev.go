package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/engine"
	"github.com/solosw/solcode/internal/jevlocal"
	"github.com/solosw/solcode/internal/memory"
	"github.com/solosw/solcode/internal/systemone"
	"github.com/solosw/solcode/internal/tool"
)

// buildJev wires the TypeSafe System One (Jev) decision layer.
//
// Jev answers typed questions with calibrated probabilities instead of
// generating text, so it can only ever advise an existing decision — it never
// becomes the thing that writes code or runs tools. Everything here is
// optional: when Jev is disabled or its backend is not ready this returns nil
// and the callers keep their deterministic behavior.
//
// type=api builds the hosted HTTP evaluator. type=local builds a LocalEvaluator
// over OpenJev/Laya artifacts. Local defaults to engine=ort (ONNX Runtime; auto-
// installs the CPU shared library into ~/.solcode/lib when missing). Explicit
// jev.engine=stub keeps the unimplemented backend so Ask fails into Decider
// fallbacks.
//
// Each subsystem is opt-in independently so a deployment can adopt routing
// without adopting the guardrail, or vice versa.
func buildJev(cfg config.Config) (*jevRuntime, error) {
	if !cfg.JevEnabled() {
		return nil, nil
	}
	var eval systemone.Evaluator
	switch cfg.JevType() {
	case config.JevBackendLocal:
		local, err := jevlocal.New(jevlocal.Options{
			ModelDir:   cfg.Jev.ModelDir,
			Model:      cfg.Jev.Model,
			DType:      cfg.Jev.DType,
			EngineName: cfg.Jev.Engine,
			ORTLib:     cfg.Jev.ORTLib,
		})
		if err != nil {
			// Artifacts disappeared between JevEnabled and here — stay silent.
			jevLog("jev local disabled: " + err.Error())
			return nil, nil
		}
		eval = local
	default:
		eval = systemone.NewClient(systemone.Options{
			BaseURL:    cfg.Jev.BaseURL,
			APIKey:     cfg.Jev.APIKey,
			Model:      cfg.Jev.Model,
			TimeoutSec: cfg.Jev.TimeoutSec,
		})
	}
	decider := systemone.NewDecider(eval).WithErrorHandler(func(err error) {
		if err != nil {
			jevLog("jev decision fell back: " + err.Error())
		}
	})
	return &jevRuntime{
		decider:        decider,
		routeMin:       cfg.Jev.RouteMinConfidence,
		routingEnabled: cfg.Jev.Routing,
		memoryEnabled:  cfg.Jev.MemoryJudge,
		guardEnabled:   cfg.Jev.Guardrail,
	}, nil
}

// jevNoneOption is the explicit "no match" choice offered on routing questions.
// Without it a Choice must select an option, so every request would route
// somewhere regardless of fit.
const jevNoneOption = "none"

// jevLog reports a Jev degradation on stderr. Jev failures are never fatal:
// every decision has a deterministic fallback, so the only thing worth doing is
// making the fallback visible.
func jevLog(message string) {
	fmt.Fprintf(os.Stderr, "jev: %s\n", message)
}

// jevRuntime holds the pieces of the Jev integration the app wires up. A nil
// *jevRuntime is valid and means "Jev is off".
type jevRuntime struct {
	decider        *systemone.Decider
	routeMin       float64
	routingEnabled bool
	memoryEnabled  bool
	guardEnabled   bool
}

// router builds the engine-facing semantic selector, or nil when routing is off.
func (j *jevRuntime) router() *engine.Router {
	if j == nil || !j.routingEnabled || j.decider == nil {
		return nil
	}
	return &engine.Router{Decider: j.decider, MinConfidence: j.routeMin}
}

// memoryJudge returns the Jev memory judge, or nil when memory judgement is off.
func (j *jevRuntime) memoryJudge() memory.Judge {
	if j == nil || !j.memoryEnabled || j.decider == nil {
		return nil
	}
	return memory.JevJudge{Decider: j.decider, MinConfidence: j.routeMin}
}

// memoryExtractor returns the Jev memory extractor, or nil when memory
// judgement is off.
func (j *jevRuntime) memoryExtractor() memory.Extractor {
	if j == nil || !j.memoryEnabled || j.decider == nil {
		return nil
	}
	return memory.JevExtractor{Judge: memory.JevJudge{Decider: j.decider, MinConfidence: j.routeMin}}
}

// guardrail returns the engine-facing tool guardrail, or nil when it is off.
func (j *jevRuntime) guardrail() engine.ToolGuardrail {
	if j == nil || !j.guardEnabled || j.decider == nil {
		return nil
	}
	return &jevGuardrail{guardrail: systemone.Guardrail{Decider: j.decider}}
}

// SuggestWorkflow picks the workflow that best matches a request.
//
// Workflows are user-authored Task graphs invoked explicitly, so this only
// ranks the ones already loaded — it never invents or runs a workflow. An empty
// name means Jev had no confident match, which is also what happens when Jev is
// off; callers should fall back to listing the workflows for the user.
func (j *jevRuntime) SuggestWorkflow(ctx context.Context, request string, workflows []engine.WorkflowCandidate) string {
	request = strings.TrimSpace(request)
	if j == nil || !j.routingEnabled || j.decider == nil || request == "" || len(workflows) == 0 {
		return ""
	}
	candidates := make([]systemone.Candidate, 0, len(workflows)+1)
	for _, workflow := range workflows {
		name := strings.TrimSpace(workflow.Name)
		if name == "" {
			continue
		}
		candidates = append(candidates, systemone.Candidate{
			Name:        name,
			Description: strings.TrimSpace(workflow.Description),
		})
	}
	if len(candidates) == 0 {
		return ""
	}
	candidates = append(candidates, systemone.Candidate{
		Name:        jevNoneOption,
		Description: "No workflow matches; use ordinary tools instead",
	})
	ranked := j.decider.Rank(ctx, request,
		"Which workflow should handle what `state` asks for? Choose none if no workflow fits.",
		candidates, 1)
	if len(ranked) == 0 || ranked[0].Name == jevNoneOption {
		return ""
	}
	if ranked[0].Confidence < j.routeConfidence() {
		return ""
	}
	return ranked[0].Name
}

func (j *jevRuntime) routeConfidence() float64 {
	if j == nil || j.routeMin <= 0 || j.routeMin > 1 {
		return 0.6
	}
	return j.routeMin
}

// jevGuardrail adapts systemone.Guardrail to engine.ToolGuardrail.
//
// It only inspects calls that mutate something. Read-only tools cannot damage
// state, so spending a request on them would cost latency for no safety gain.
type jevGuardrail struct {
	guardrail systemone.Guardrail
}

// guardedTools are the tools whose input is worth inspecting. The set is
// deliberately narrow: Bash can run anything, and the write/edit family changes
// files.
var guardedTools = map[string]bool{
	tool.BashToolName:        true,
	tool.WriteToolName:       true,
	tool.MultiWriteToolName:  true,
	tool.EditToolName:        true,
	tool.MultiEditToolName:   true,
	tool.PatchToolName:       true,
	tool.ComputerUseToolName: true,
}

// CheckToolCall implements engine.ToolGuardrail. It returns a message to block
// the call, or "" to let it proceed.
func (g *jevGuardrail) CheckToolCall(ctx context.Context, toolName string, input json.RawMessage) string {
	if g == nil || !guardedTools[toolName] {
		return ""
	}
	payload := strings.TrimSpace(guardrailPayload(toolName, input))
	if payload == "" {
		return ""
	}
	verdict := g.guardrail.CheckToolInput(ctx, toolName, payload)
	if verdict.Allowed() {
		return ""
	}
	if verdict.Unsafe {
		return "blocked by the Jev safety guardrail (" + verdict.Reason + "). " +
			"Rewrite the call so it does not touch credentials, destroy data outside the working directory, or exfiltrate data."
	}
	return "held by the Jev safety guardrail for review (" + verdict.Reason + "). " +
		"Confirm the intent with the user before retrying."
}

// guardrailPayload picks the field that carries the risk for each tool, so the
// model judges the command or the content rather than a JSON envelope.
func guardrailPayload(toolName string, input json.RawMessage) string {
	switch toolName {
	case tool.BashToolName:
		var params tool.BashParams
		if json.Unmarshal(input, &params) == nil {
			return params.Command
		}
	case tool.WriteToolName:
		var params tool.WriteParams
		if json.Unmarshal(input, &params) == nil {
			return params.Path + "\n" + params.Content
		}
	case tool.EditToolName:
		var params tool.EditParams
		if json.Unmarshal(input, &params) == nil {
			return params.Path + "\n" + params.OldString + "\n" + params.NewString
		}
	case tool.MultiEditToolName:
		var params tool.MultiEditParams
		if json.Unmarshal(input, &params) == nil {
			var b strings.Builder
			for _, edit := range params.Edits {
				b.WriteString(edit.Path)
				b.WriteString("\n")
				b.WriteString(edit.OldString)
				b.WriteString("\n")
				b.WriteString(edit.NewString)
				b.WriteString("\n")
			}
			return b.String()
		}
	case tool.MultiWriteToolName:
		var params tool.MultiWriteParams
		if json.Unmarshal(input, &params) == nil {
			var b strings.Builder
			for _, file := range params.Files {
				b.WriteString(file.Path)
				b.WriteString("\n")
				b.WriteString(file.Content)
				b.WriteString("\n")
			}
			return b.String()
		}
	case tool.PatchToolName:
		var params tool.PatchParams
		if json.Unmarshal(input, &params) == nil {
			return params.Path + "\n" + params.PatchText
		}
	case tool.ComputerUseToolName:
		return string(input)
	}
	return string(input)
}
