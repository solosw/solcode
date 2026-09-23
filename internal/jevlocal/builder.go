package jevlocal

import "fmt"

type qEnc struct {
	instr []int64
	opts  [][]int64 // parallel to plan.Options / OptionTexts
}

// buildOpenJevTensors tokenizes state + questions into OpenJev session tensors.
//
// Layout (model card):
//
//	[CLS] [STATE] state… [Q] instr… [OPT] opt… [OPT] … [Q] … [SEP]
//
// seg is -1 except option-token spans (pair index) and question-token spans
// (totalPairs + questionIndex). Truncation is best-effort: state is capped at
// max_state_tokens; if still over max_len, trailing options then trailing
// questions are dropped until it fits.
func buildOpenJevTensors(state any, plans []questionPlan, arts Artifacts, tok *openJevTokenizer) (TensorBundle, []questionPlan, error) {
	if tok == nil {
		return TensorBundle{}, nil, fmt.Errorf("%w: open-jev tokenizer not loaded", ErrEngineNotReady)
	}
	if len(plans) == 0 {
		return TensorBundle{}, nil, fmt.Errorf("open-jev requires at least one question")
	}

	maxLen := arts.Config.MaxLen
	if maxLen <= 0 {
		maxLen = 512
	}
	maxState := arts.Config.MaxStateTokens
	if maxState <= 0 {
		maxState = 256
	}

	stateText, err := stateToText(state)
	if err != nil {
		return TensorBundle{}, nil, err
	}
	stateIDs, err := tok.encodeText(stateText)
	if err != nil {
		return TensorBundle{}, nil, fmt.Errorf("tokenize state: %w", err)
	}
	if len(stateIDs) > maxState {
		stateIDs = stateIDs[:maxState]
	}

	encoded := make([]qEnc, len(plans))
	for i, plan := range plans {
		instr, err := tok.encodeText(plan.Instructions)
		if err != nil {
			return TensorBundle{}, nil, fmt.Errorf("tokenize question %q: %w", plan.ID, err)
		}
		encoded[i].instr = instr
		encoded[i].opts = make([][]int64, len(plan.OptionTexts))
		for j, text := range plan.OptionTexts {
			ids, err := tok.encodeText(text)
			if err != nil {
				return TensorBundle{}, nil, fmt.Errorf("tokenize option %q/%q: %w", plan.ID, plan.Options[j], err)
			}
			if len(ids) == 0 {
				ids = []int64{int64(openJevUnkID)}
			}
			encoded[i].opts[j] = ids
		}
		if len(encoded[i].opts) == 0 {
			return TensorBundle{}, nil, fmt.Errorf("question %q has no options", plan.ID)
		}
	}

	for {
		ids, seg, pairQ, pairOpt, kept, fits := assembleOpenJev(tok, stateIDs, plans, encoded, maxLen)
		if fits {
			mask := make([]int64, len(ids))
			for i := range mask {
				mask[i] = 1
			}
			return TensorBundle{
				InputIDs:      ids,
				AttentionMask: mask,
				Seg:           seg,
				PairQ:         pairQ,
				PairOpt:       pairOpt,
			}, kept, nil
		}
		if !shrinkEncoded(&encoded) {
			return TensorBundle{}, nil, fmt.Errorf("open-jev sequence exceeds max_len=%d even after truncation", maxLen)
		}
	}
}

// shrinkEncoded drops the last option of the last multi-option question; if
// every remaining question has a single option, drops the last question
// (unless only one question remains with one option — then fail).
func shrinkEncoded(enc *[]qEnc) bool {
	e := *enc
	for i := len(e) - 1; i >= 0; i-- {
		if len(e[i].opts) > 1 {
			e[i].opts = e[i].opts[:len(e[i].opts)-1]
			return true
		}
	}
	if len(e) > 1 {
		*enc = e[:len(e)-1]
		return true
	}
	return false
}

func assembleOpenJev(tok *openJevTokenizer, stateIDs []int64, plans []questionPlan, encoded []qEnc, maxLen int) (ids, seg, pairQ, pairOpt []int64, kept []questionPlan, ok bool) {
	n := len(encoded)
	if n > len(plans) {
		n = len(plans)
	}
	totalPairs := 0
	for i := 0; i < n; i++ {
		totalPairs += len(encoded[i].opts)
	}
	if totalPairs == 0 {
		return nil, nil, nil, nil, nil, false
	}

	ids = make([]int64, 0, maxLen)
	seg = make([]int64, 0, maxLen)
	appendTok := func(id, slot int64) {
		ids = append(ids, id)
		seg = append(seg, slot)
	}

	appendTok(int64(tok.clsID), -1)
	appendTok(int64(tok.stateID), -1)
	for _, id := range stateIDs {
		appendTok(id, -1)
	}

	pairQ = make([]int64, 0, totalPairs)
	pairOpt = make([]int64, 0, totalPairs)
	kept = make([]questionPlan, 0, n)
	pairIdx := 0
	qiKeep := 0
	for qi := 0; qi < n; qi++ {
		q := encoded[qi]
		if len(q.opts) == 0 {
			continue
		}
		qSlot := int64(totalPairs + qiKeep)
		appendTok(int64(tok.qID), -1)
		for _, id := range q.instr {
			appendTok(id, qSlot)
		}
		optLabels := make([]string, 0, len(q.opts))
		optTexts := make([]string, 0, len(q.opts))
		for oi, optIDs := range q.opts {
			appendTok(int64(tok.optID), -1)
			for _, id := range optIDs {
				appendTok(id, int64(pairIdx))
			}
			pairQ = append(pairQ, qSlot)
			pairOpt = append(pairOpt, int64(pairIdx))
			pairIdx++
			if oi < len(plans[qi].Options) {
				optLabels = append(optLabels, plans[qi].Options[oi])
			} else {
				optLabels = append(optLabels, fmt.Sprintf("opt_%d", oi))
			}
			if oi < len(plans[qi].OptionTexts) {
				optTexts = append(optTexts, plans[qi].OptionTexts[oi])
			} else {
				optTexts = append(optTexts, optLabels[len(optLabels)-1])
			}
		}
		kept = append(kept, questionPlan{
			ID:           plans[qi].ID,
			Type:         plans[qi].Type,
			Instructions: plans[qi].Instructions,
			Options:      optLabels,
			OptionTexts:  optTexts,
		})
		qiKeep++
	}
	appendTok(int64(tok.sepID), -1)

	if len(ids) > maxLen {
		return nil, nil, nil, nil, nil, false
	}
	return ids, seg, pairQ, pairOpt, kept, true
}
