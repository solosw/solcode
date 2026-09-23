package jevlocal

import (
	"context"
	"fmt"

	"github.com/solosw/solcode/internal/systemone"
)

// ModelFamily packs Ask state/questions into ONNX tensors and decodes outputs.
// Each on-disk layout (OpenJev, Laya, …) implements this seam.
type ModelFamily interface {
	Name() string
	Build(ctx context.Context, state any, questions map[string]systemone.Question) (NamedTensors, []questionPlan, error)
	Decode(plans []questionPlan, outs NamedOutputs) (systemone.Answers, error)
}

func newFamily(arts Artifacts) (ModelFamily, error) {
	switch arts.Family {
	case FamilyOpenJev:
		return newOpenJevFamily(arts)
	case FamilyLaya:
		return newLayaFamily(arts)
	default:
		return nil, fmt.Errorf("unsupported local jev family %q", arts.Family)
	}
}
