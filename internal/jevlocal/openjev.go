package jevlocal

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/solosw/solcode/internal/systemone"
)

// questionPlan is one evaluated question with its option labels in logit order.
type questionPlan struct {
	ID           string
	Type         string
	Instructions string
	Options      []string // label order matching the pair group (choice keys / score levels / no|yes)
	OptionTexts  []string // text fed to the tokenizer for each option (parallel to Options)
}

// DecodeLogits turns OpenJev pair logits into systemone answers.
//
// Softmax is within each question's option group after dividing by temperature.
// choice → argmax + distribution; noul → p(yes) with options [no, yes];
// score → expected level index over ordered levels.
func DecodeLogits(plans []questionPlan, logits []float32, temperature float64) (systemone.Answers, error) {
	if temperature <= 0 {
		temperature = 1.05
	}
	need := 0
	for _, plan := range plans {
		need += len(plan.Options)
	}
	if len(logits) < need {
		return nil, fmt.Errorf("open-jev logits length %d < required pairs %d", len(logits), need)
	}

	out := make(systemone.Answers, len(plans))
	offset := 0
	for _, plan := range plans {
		n := len(plan.Options)
		slice := logits[offset : offset+n]
		offset += n
		probs := softmaxTemp(slice, temperature)
		switch plan.Type {
		case systemone.TypeChoice:
			best := 0
			for i := 1; i < len(probs); i++ {
				if probs[i] > probs[best] {
					best = i
				}
			}
			dist := make(map[string]float64, n)
			for i, name := range plan.Options {
				dist[name] = probs[i]
			}
			out[plan.ID] = systemone.Answer{
				Type:          systemone.TypeChoice,
				Choice:        plan.Options[best],
				Confidence:    probs[best],
				Probabilities: dist,
			}
		case systemone.TypeNoul:
			// OpenJev card uses options ["no", "yes"]; p(yes) is the noul.
			pYes := 0.0
			found := false
			for i, name := range plan.Options {
				if strings.EqualFold(name, "yes") || name == "true" {
					pYes = probs[i]
					found = true
					break
				}
			}
			if !found && len(probs) == 2 {
				// Fall back to the second slot when labels are unconventional.
				pYes = probs[1]
			}
			out[plan.ID] = systemone.Answer{Type: systemone.TypeNoul, Noul: pYes}
		case systemone.TypeScore:
			var expected float64
			for i, p := range probs {
				expected += float64(i) * p
			}
			best := 0
			for i := 1; i < len(probs); i++ {
				if probs[i] > probs[best] {
					best = i
				}
			}
			out[plan.ID] = systemone.Answer{
				Type:       systemone.TypeScore,
				Score:      expected,
				Confidence: probs[best],
				Legend:     append([]string(nil), plan.Options...),
			}
		default:
			return nil, fmt.Errorf("unsupported question type %q", plan.Type)
		}
	}
	return out, nil
}

func softmaxTemp(logits []float32, temperature float64) []float64 {
	out := make([]float64, len(logits))
	if len(logits) == 0 {
		return out
	}
	max := float64(logits[0]) / temperature
	for _, v := range logits[1:] {
		if x := float64(v) / temperature; x > max {
			max = x
		}
	}
	var sum float64
	for i, v := range logits {
		out[i] = math.Exp(float64(v)/temperature - max)
		sum += out[i]
	}
	if sum == 0 {
		for i := range out {
			out[i] = 1 / float64(len(out))
		}
		return out
	}
	for i := range out {
		out[i] /= sum
	}
	return out
}

// planQuestions flattens systemone questions into OpenJev option groups.
// Choice option order is sorted by name for determinism; score keeps level
// order; noul always emits [no, yes].
func planQuestions(questions map[string]systemone.Question) ([]questionPlan, error) {
	ids := make([]string, 0, len(questions))
	for id := range questions {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	plans := make([]questionPlan, 0, len(ids))
	for _, id := range ids {
		q := questions[id]
		plan := questionPlan{
			ID:           id,
			Type:         q.Type,
			Instructions: strings.TrimSpace(instructionText(q.Instructions)),
		}
		if plan.Instructions == "" {
			return nil, fmt.Errorf("question %q requires instructions", id)
		}
		switch q.Type {
		case systemone.TypeChoice:
			opts, texts, err := choiceLabelsAndTexts(q.Criteria)
			if err != nil {
				return nil, fmt.Errorf("question %q: %w", id, err)
			}
			plan.Options = opts
			plan.OptionTexts = texts
		case systemone.TypeScore:
			levels, err := scoreLabels(q.Criteria)
			if err != nil {
				return nil, fmt.Errorf("question %q: %w", id, err)
			}
			plan.Options = levels
			plan.OptionTexts = append([]string(nil), levels...)
		case systemone.TypeNoul:
			plan.Options = []string{"no", "yes"}
			plan.OptionTexts = []string{"no", "yes"}
		default:
			return nil, fmt.Errorf("question %q has unsupported type %q", id, q.Type)
		}
		plans = append(plans, plan)
	}
	return plans, nil
}

func choiceLabelsAndTexts(criteria any) (names, texts []string, err error) {
	switch typed := criteria.(type) {
	case map[string]string:
		names = make([]string, 0, len(typed))
		for name := range typed {
			names = append(names, name)
		}
		sort.Strings(names)
		if len(names) == 0 {
			return nil, nil, fmt.Errorf("choice requires criteria")
		}
		texts = make([]string, len(names))
		for i, name := range names {
			desc := strings.TrimSpace(typed[name])
			if desc != "" {
				texts[i] = desc
			} else {
				texts[i] = name
			}
		}
		return names, texts, nil
	case map[string]any:
		names = make([]string, 0, len(typed))
		for name := range typed {
			names = append(names, name)
		}
		sort.Strings(names)
		if len(names) == 0 {
			return nil, nil, fmt.Errorf("choice requires criteria")
		}
		texts = make([]string, len(names))
		for i, name := range names {
			desc := strings.TrimSpace(fmt.Sprint(typed[name]))
			if desc != "" && desc != "<nil>" {
				texts[i] = desc
			} else {
				texts[i] = name
			}
		}
		return names, texts, nil
	default:
		return nil, nil, fmt.Errorf("choice requires map criteria")
	}
}

func scoreLabels(criteria any) ([]string, error) {
	switch typed := criteria.(type) {
	case []string:
		if len(typed) == 0 {
			return nil, fmt.Errorf("score requires levels")
		}
		return append([]string(nil), typed...), nil
	case []any:
		if len(typed) == 0 {
			return nil, fmt.Errorf("score requires levels")
		}
		out := make([]string, 0, len(typed))
		for _, level := range typed {
			out = append(out, fmt.Sprint(level))
		}
		return out, nil
	default:
		return nil, fmt.Errorf("score requires level list criteria")
	}
}
