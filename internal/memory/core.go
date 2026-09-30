package memory

import (
	"sort"
	"strings"
	"time"
)

// Core memory mirrors Letta/MemGPT "always-in-context" blocks: durable
// preferences and constraints that should ride every prompt without a
// retrieval round-trip. Everything else stays in archival storage and is
// fetched through ReadMemory / bootstrap retrieval.
//
// Mapping onto existing tiers:
//   - Core  ≈ high-confidence preference/constraint at M3–M5 (and global scope)
//   - Working ≈ M2 (session-local scratch)
//   - Recall / episodic ≈ sessionmemory solcode.md
//   - Archival ≈ M3–M5 facts/workflows retrieved on demand

// CoreBlockLabel names one in-context memory block.
type CoreBlockLabel string

const (
	CoreBlockUser    CoreBlockLabel = "user"
	CoreBlockProject CoreBlockLabel = "project"
	CoreBlockPersona CoreBlockLabel = "persona"
)

// CoreBlock is one labeled block of always-on memory text.
type CoreBlock struct {
	Label   CoreBlockLabel
	Content string
	Items   []Item
}

// CoreSelectionOptions bounds how much core memory is assembled.
type CoreSelectionOptions struct {
	// MaxBlocks caps how many distinct blocks are returned (default 3).
	MaxBlocks int
	// MaxItemsPerBlock caps entries inside one block (default 4).
	MaxItemsPerBlock int
	// MaxTotalItems hard-caps items across all blocks (default 8).
	MaxTotalItems int
	// SessionID, when set, prefers same-session working notes lightly.
	SessionID string
}

func (o CoreSelectionOptions) normalize() CoreSelectionOptions {
	if o.MaxBlocks <= 0 {
		o.MaxBlocks = 3
	}
	if o.MaxItemsPerBlock <= 0 {
		o.MaxItemsPerBlock = 4
	}
	if o.MaxTotalItems <= 0 {
		o.MaxTotalItems = 8
	}
	return o
}

// IsCoreCandidate reports whether an item belongs in always-on core memory.
// Preferences and constraints that have graduated past sensory/working scratch
// are treated as core; global-scope durable facts may also qualify.
func IsCoreCandidate(item Item) bool {
	text := strings.TrimSpace(item.Text)
	if text == "" || item.Tier == TierSensory {
		return false
	}
	if !item.IsActive(time.Time{}) {
		return false
	}
	switch item.Kind {
	case KindPreference, KindConstraint:
		return item.Tier == TierShortTerm || item.Tier == TierLongTerm || item.Tier == TierProcedural || item.Tier == TierWorking
	case KindWorkflow:
		return item.Tier == TierProcedural
	case KindFact:
		return item.Scope == ScopeGlobal && (item.Tier == TierLongTerm || item.Tier == TierShortTerm) && item.Confidence >= 0.8
	default:
		return false
	}
}

// SelectCoreBlocks partitions archival items into Letta-style core blocks.
func SelectCoreBlocks(items []Item, opts CoreSelectionOptions) []CoreBlock {
	opts = opts.normalize()
	type scored struct {
		item  Item
		score float64
	}
	candidates := make([]scored, 0, len(items))
	now := time.Now()
	for _, item := range items {
		if !IsCoreCandidate(item) {
			continue
		}
		score := EvolvedImportance(item, now, 0.03, 0.12)
		if item.Kind == KindPreference || item.Kind == KindConstraint {
			score += 8
		}
		if item.Scope == ScopeGlobal {
			score += 4
		}
		if item.Tier == TierLongTerm || item.Tier == TierProcedural {
			score += 3
		}
		if item.Confidence > 0 {
			score += item.Confidence * 2
		}
		candidates = append(candidates, scored{item: item, score: score})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].score == candidates[j].score {
			return candidates[i].item.UpdatedAt.After(candidates[j].item.UpdatedAt)
		}
		return candidates[i].score > candidates[j].score
	})

	byLabel := map[CoreBlockLabel][]Item{}
	total := 0
	for _, c := range candidates {
		if total >= opts.MaxTotalItems {
			break
		}
		label := coreLabelFor(c.item)
		if len(byLabel[label]) >= opts.MaxItemsPerBlock {
			continue
		}
		byLabel[label] = append(byLabel[label], c.item)
		total++
	}

	order := []CoreBlockLabel{CoreBlockUser, CoreBlockProject, CoreBlockPersona}
	out := make([]CoreBlock, 0, opts.MaxBlocks)
	for _, label := range order {
		if len(out) >= opts.MaxBlocks {
			break
		}
		list := byLabel[label]
		if len(list) == 0 {
			continue
		}
		out = append(out, CoreBlock{
			Label:   label,
			Content: formatCoreItemList(list),
			Items:   list,
		})
	}
	return out
}

func coreLabelFor(item Item) CoreBlockLabel {
	switch item.Kind {
	case KindPreference:
		if item.Scope == ScopeGlobal {
			return CoreBlockUser
		}
		return CoreBlockProject
	case KindConstraint:
		if item.Scope == ScopeGlobal {
			return CoreBlockPersona
		}
		return CoreBlockProject
	case KindWorkflow:
		return CoreBlockProject
	default:
		if item.Scope == ScopeGlobal {
			return CoreBlockUser
		}
		return CoreBlockProject
	}
}

func formatCoreItemList(items []Item) string {
	lines := make([]string, 0, len(items))
	for _, item := range items {
		text := strings.TrimSpace(item.Text)
		if text == "" {
			continue
		}
		kind := strings.TrimSpace(string(item.Kind))
		if kind != "" {
			lines = append(lines, "- ["+kind+"] "+text)
		} else {
			lines = append(lines, "- "+text)
		}
	}
	return strings.Join(lines, "\n")
}

// FormatCoreBlocks renders core blocks for prompt injection.
func FormatCoreBlocks(blocks []CoreBlock) string {
	if len(blocks) == 0 {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		content := strings.TrimSpace(block.Content)
		if content == "" {
			continue
		}
		label := strings.TrimSpace(string(block.Label))
		if label == "" {
			label = "core"
		}
		parts = append(parts, "### "+label+"\n"+content)
	}
	if len(parts) == 0 {
		return ""
	}
	return "Core memory (always-on):\n" + strings.Join(parts, "\n\n")
}

// CoreItems flattens selected core blocks back into items (for Touch/indexing).
func CoreItems(blocks []CoreBlock) []Item {
	var out []Item
	seen := map[string]bool{}
	for _, block := range blocks {
		for _, item := range block.Items {
			if item.ID == "" || seen[item.ID] {
				continue
			}
			seen[item.ID] = true
			out = append(out, item)
		}
	}
	return out
}
