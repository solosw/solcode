package jevlocal

import (
	"context"
	"fmt"

	"github.com/solosw/solcode/internal/systemone"
)

type openJevFamily struct {
	arts Artifacts
	tok  *openJevTokenizer
}

func newOpenJevFamily(arts Artifacts) (ModelFamily, error) {
	tok, err := loadOpenJevTokenizer(arts.Dir)
	if err != nil {
		return nil, err
	}
	return &openJevFamily{arts: arts, tok: tok}, nil
}

func (f *openJevFamily) Name() string { return FamilyOpenJev }

func (f *openJevFamily) Build(ctx context.Context, state any, questions map[string]systemone.Question) (NamedTensors, []questionPlan, error) {
	if err := ctx.Err(); err != nil {
		return NamedTensors{}, nil, err
	}
	plans, err := planQuestions(questions)
	if err != nil {
		return NamedTensors{}, nil, err
	}
	bundle, kept, err := buildOpenJevTensors(state, plans, f.arts, f.tok)
	if err != nil {
		return NamedTensors{}, nil, err
	}
	return namedFromOpenJev(bundle), kept, nil
}

func (f *openJevFamily) Decode(plans []questionPlan, outs NamedOutputs) (systemone.Answers, error) {
	logits := openJevLogits(outs)
	if logits == nil {
		return nil, fmt.Errorf("open-jev: missing logits output")
	}
	return DecodeLogits(plans, logits, f.arts.Config.Temperature)
}
