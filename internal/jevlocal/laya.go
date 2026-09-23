package jevlocal

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"strings"

	"github.com/hashirmuzaffar/hfbpe"
	"github.com/solosw/solcode/internal/systemone"
)

const (
	layaQTypeChoice = 0
	layaQTypeScore  = 1
	layaQTypeNoul   = 2
	layaTempMin     = 0.5
	layaTempMax     = 5.0
)

type layaFamily struct {
	arts   Artifacts
	tok    *hfbpe.Tokenizer
	clsID  int64
	sepID  int64
	maskID int64
	padID  int64
}

func newLayaFamily(arts Artifacts) (ModelFamily, error) {
	tokPath := filepath.Join(arts.Dir, "tokenizer.json")
	tok, err := hfbpe.LoadFile(tokPath)
	if err != nil {
		return nil, fmt.Errorf("laya tokenizer: %w", err)
	}
	f := &layaFamily{arts: arts, tok: tok}
	var ok bool
	if f.clsID, ok = tokenID(tok, "[CLS]"); !ok {
		return nil, fmt.Errorf("laya tokenizer missing [CLS]")
	}
	if f.sepID, ok = tokenID(tok, "[SEP]"); !ok {
		return nil, fmt.Errorf("laya tokenizer missing [SEP]")
	}
	if f.maskID, ok = tokenID(tok, "[MASK]"); !ok {
		return nil, fmt.Errorf("laya tokenizer missing [MASK]")
	}
	if id, ok := tok.TokenToID("[PAD]"); ok {
		f.padID = int64(id)
	}
	return f, nil
}

func tokenID(tok *hfbpe.Tokenizer, piece string) (int64, bool) {
	id, ok := tok.TokenToID(piece)
	return int64(id), ok
}

func (f *layaFamily) Name() string { return FamilyLaya }

func (f *layaFamily) Build(ctx context.Context, state any, questions map[string]systemone.Question) (NamedTensors, []questionPlan, error) {
	if err := ctx.Err(); err != nil {
		return NamedTensors{}, nil, err
	}
	plans, err := planQuestions(questions)
	if err != nil {
		return NamedTensors{}, nil, err
	}
	if len(plans) == 0 {
		return NamedTensors{}, nil, fmt.Errorf("laya requires at least one question")
	}

	maxLen := f.arts.LayaConfig.MaxLen
	if maxLen <= 0 {
		maxLen = 512
	}
	headMax := f.arts.LayaConfig.HeadMaxLen
	if headMax <= 0 {
		headMax = 192
	}

	stateText, err := stateToText(state)
	if err != nil {
		return NamedTensors{}, nil, err
	}

	type seqItem struct {
		ids     []int64
		markers []int64
		qtype   int64
	}
	items := make([]seqItem, 0, len(plans))
	kept := make([]questionPlan, 0, len(plans))
	for _, plan := range plans {
		ids, markers, err := f.buildSequence(stateText, plan, maxLen, headMax)
		if err != nil {
			return NamedTensors{}, nil, err
		}
		if len(markers) != len(plan.Options) {
			return NamedTensors{}, nil, fmt.Errorf("question %q options exceed head_max_len=%d", plan.ID, headMax)
		}
		items = append(items, seqItem{ids: ids, markers: markers, qtype: layaQType(plan.Type)})
		kept = append(kept, plan)
	}

	n := len(items)
	L := maxLen // ONNX export reshapes assume max_len (e.g. 512), not ragged batch max.
	kmax := 0
	for _, it := range items {
		if len(it.ids) > L {
			return NamedTensors{}, nil, fmt.Errorf("laya sequence length %d exceeds max_len=%d", len(it.ids), L)
		}
		if len(it.markers) > kmax {
			kmax = len(it.markers)
		}
	}
	if L == 0 || kmax == 0 {
		return NamedTensors{}, nil, fmt.Errorf("laya packed empty tensors")
	}

	inputIDs := make([]int64, n*L)
	attn := make([]int64, n*L)
	mpos := make([]int64, n*kmax)
	mmask := make([]bool, n*kmax)
	qtypes := make([]int64, n)
	for i, it := range items {
		qtypes[i] = it.qtype
		row := i * L
		for j := 0; j < L; j++ {
			if j < len(it.ids) {
				inputIDs[row+j] = it.ids[j]
				attn[row+j] = 1
			} else {
				inputIDs[row+j] = f.padID
			}
		}
		mrow := i * kmax
		for j, m := range it.markers {
			mpos[mrow+j] = m
			mmask[mrow+j] = true
		}
	}

	t := NamedTensors{}.ensure()
	t.Int64["input_ids"] = Int64Tensor{Shape: []int64{int64(n), int64(L)}, Data: inputIDs}
	t.Int64["attention_mask"] = Int64Tensor{Shape: []int64{int64(n), int64(L)}, Data: attn}
	t.Int64["marker_pos"] = Int64Tensor{Shape: []int64{int64(n), int64(kmax)}, Data: mpos}
	t.Bool["marker_mask"] = BoolTensor{Shape: []int64{int64(n), int64(kmax)}, Data: mmask}
	t.Int64["qtype"] = Int64Tensor{Shape: []int64{int64(n)}, Data: qtypes}
	return t, kept, nil
}

func (f *layaFamily) Decode(plans []questionPlan, outs NamedOutputs) (systemone.Answers, error) {
	logitsT, ok := outs.Float32["logits"]
	if !ok || len(logitsT.Data) == 0 {
		return nil, fmt.Errorf("laya: missing logits output")
	}
	n := len(plans)
	if n == 0 {
		return systemone.Answers{}, nil
	}
	kmax := 0
	if len(logitsT.Shape) >= 2 {
		kmax = int(logitsT.Shape[1])
	}
	if kmax == 0 {
		if len(logitsT.Data)%n != 0 {
			return nil, fmt.Errorf("laya logits length %d not divisible by %d questions", len(logitsT.Data), n)
		}
		kmax = len(logitsT.Data) / n
	}
	if len(logitsT.Data) < n*kmax {
		return nil, fmt.Errorf("laya logits length %d < %d", len(logitsT.Data), n*kmax)
	}

	answers := make(systemone.Answers, n)
	for i, plan := range plans {
		k := len(plan.Options)
		if k == 0 {
			return nil, fmt.Errorf("question %q has no options", plan.ID)
		}
		if k > kmax {
			return nil, fmt.Errorf("question %q has %d options > logits width %d", plan.ID, k, kmax)
		}
		row := logitsT.Data[i*kmax : i*kmax+k]
		temp := f.temperatureFor(plan.Type, k)
		probs := softmaxTemp(row, temp)
		switch plan.Type {
		case systemone.TypeChoice:
			best := 0
			for j := 1; j < len(probs); j++ {
				if probs[j] > probs[best] {
					best = j
				}
			}
			dist := make(map[string]float64, k)
			for j, name := range plan.Options {
				dist[name] = probs[j]
			}
			answers[plan.ID] = systemone.Answer{
				Type:          systemone.TypeChoice,
				Choice:        plan.Options[best],
				Confidence:    confidenceFromProbs(probs),
				Probabilities: dist,
			}
		case systemone.TypeScore:
			var expected float64
			for j, p := range probs {
				expected += float64(j) * p
			}
			best := 0
			for j := 1; j < len(probs); j++ {
				if probs[j] > probs[best] {
					best = j
				}
			}
			answers[plan.ID] = systemone.Answer{
				Type:       systemone.TypeScore,
				Score:      expected,
				Confidence: confidenceFromProbs(probs),
				Legend:     append([]string(nil), plan.Options...),
			}
		case systemone.TypeNoul:
			pYes := 0.0
			if len(probs) >= 2 {
				pYes = probs[1]
			}
			answers[plan.ID] = systemone.Answer{
				Type:       systemone.TypeNoul,
				Noul:       pYes,
				Confidence: math.Max(pYes, 1-pYes),
			}
		default:
			return nil, fmt.Errorf("unsupported question type %q", plan.Type)
		}
	}
	return answers, nil
}

func (f *layaFamily) buildSequence(state string, plan questionPlan, maxLen, headMaxLen int) ([]int64, []int64, error) {
	opts := layaOptionTexts(plan)
	maskTok := "[MASK]"
	ins := strings.ReplaceAll(plan.Instructions, maskTok, " ")
	headPrefix := fmt.Sprintf("%s question: %s", plan.Type, ins)
	headIDs := f.encodePlain(headPrefix)

	optIDs := make([][]int64, len(opts))
	for i, opt := range opts {
		text := " " + strings.ReplaceAll(opt, maskTok, " ")
		ids := f.encodePlain(text)
		if len(ids) > 48 {
			ids = ids[:48]
		}
		optIDs[i] = append([]int64{f.maskID}, ids...)
	}
	optBudget := headMaxLen - sumLens(optIDs)
	if optBudget < 16 {
		per := (headMaxLen - 16) / max(1, len(optIDs))
		if per < 4 {
			per = 4
		}
		for i := range optIDs {
			if len(optIDs[i]) > per {
				optIDs[i] = optIDs[i][:per]
			}
		}
		optBudget = headMaxLen - sumLens(optIDs)
	}
	if optBudget < 8 {
		optBudget = 8
	}
	if len(headIDs) > optBudget {
		headIDs = headIDs[:optBudget]
	}

	ids := make([]int64, 0, maxLen)
	ids = append(ids, f.clsID)
	ids = append(ids, headIDs...)
	ids = append(ids, f.sepID)
	markers := make([]int64, 0, len(optIDs))
	for _, o := range optIDs {
		markers = append(markers, int64(len(ids)))
		ids = append(ids, o...)
	}
	ids = append(ids, f.sepID)
	room := maxLen - len(ids) - 1
	if room < 0 {
		room = 0
	}
	st := f.encodePlain(strings.ReplaceAll(state, maskTok, " "))
	if len(st) > room {
		st = st[:room]
	}
	ids = append(ids, st...)
	ids = append(ids, f.sepID)
	if len(ids) > maxLen {
		ids = ids[:maxLen]
	}
	keptMarkers := make([]int64, 0, len(markers))
	for _, m := range markers {
		if int(m) < len(ids) {
			keptMarkers = append(keptMarkers, m)
		}
	}
	return ids, keptMarkers, nil
}

func (f *layaFamily) encodePlain(text string) []int64 {
	enc := f.tok.Encode(text)
	out := make([]int64, len(enc.IDs))
	for i, id := range enc.IDs {
		out[i] = int64(id)
	}
	return out
}

func (f *layaFamily) temperatureFor(qtype string, k int) float64 {
	bucket := layaTempBucket(qtype, k)
	if v, ok := f.arts.LayaConfig.TemperatureByOptions[bucket]; ok {
		return clampLayaTemp(v)
	}
	idx := int(layaQType(qtype))
	temps := f.arts.LayaConfig.Temperature
	if idx >= 0 && idx < len(temps) {
		return clampLayaTemp(temps[idx])
	}
	return 1.0
}

func layaQType(t string) int64 {
	switch t {
	case systemone.TypeChoice:
		return layaQTypeChoice
	case systemone.TypeScore:
		return layaQTypeScore
	case systemone.TypeNoul:
		return layaQTypeNoul
	default:
		return layaQTypeChoice
	}
}

func layaTempBucket(qtype string, k int) string {
	size := "11+"
	switch {
	case k <= 2:
		size = "2"
	case k <= 5:
		size = "3-5"
	case k <= 10:
		size = "6-10"
	}
	return qtype + ":" + size
}

func clampLayaTemp(t float64) float64 {
	if math.IsNaN(t) || math.IsInf(t, 0) {
		return 1
	}
	if t < layaTempMin {
		return layaTempMin
	}
	if t > layaTempMax {
		return layaTempMax
	}
	return t
}

func layaOptionTexts(plan questionPlan) []string {
	switch plan.Type {
	case systemone.TypeChoice:
		out := make([]string, len(plan.Options))
		for i, name := range plan.Options {
			desc := ""
			if i < len(plan.OptionTexts) {
				desc = strings.TrimSpace(plan.OptionTexts[i])
			}
			if desc == "" || desc == name {
				out[i] = name
			} else {
				out[i] = name + ": " + desc
			}
		}
		return out
	case systemone.TypeScore:
		out := make([]string, len(plan.Options))
		for i, level := range plan.Options {
			out[i] = fmt.Sprintf("level %d: %s", i, level)
		}
		return out
	case systemone.TypeNoul:
		return []string{
			"false: no, the statement does not hold",
			"true: yes, the statement holds",
		}
	default:
		return append([]string(nil), plan.OptionTexts...)
	}
}

func confidenceFromProbs(p []float64) float64 {
	k := len(p)
	if k < 2 {
		return 1
	}
	var ent float64
	for _, v := range p {
		if v <= 0 {
			continue
		}
		ent -= v * math.Log(v)
	}
	return math.Max(0, math.Min(1, 1-ent/math.Log(float64(k))))
}

func sumLens(xs [][]int64) int {
	n := 0
	for _, x := range xs {
		n += len(x)
	}
	return n
}
