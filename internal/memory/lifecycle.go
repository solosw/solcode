package memory

import (
	"time"
)

type LifecycleConfig struct {
	M1TTL                    time.Duration
	M2TTL                    time.Duration
	PromotionAccessThreshold int
	PromotionConfidence      float64
}

type Lifecycle struct {
	Config LifecycleConfig
}

func DefaultLifecycle() Lifecycle {
	return Lifecycle{Config: LifecycleConfig{
		M1TTL:                    12 * time.Hour,
		M2TTL:                    72 * time.Hour,
		PromotionAccessThreshold: 3,
		PromotionConfidence:      0.75,
	}}
}

func (l Lifecycle) Normalize() Lifecycle {
	cfg := l.Config
	if cfg.M1TTL <= 0 {
		cfg.M1TTL = 12 * time.Hour
	}
	if cfg.M2TTL <= 0 {
		cfg.M2TTL = 72 * time.Hour
	}
	if cfg.PromotionAccessThreshold <= 0 {
		cfg.PromotionAccessThreshold = 3
	}
	if cfg.PromotionConfidence <= 0 {
		cfg.PromotionConfidence = 0.75
	}
	return Lifecycle{Config: cfg}
}

func (l Lifecycle) Apply(item Item, now time.Time) Item {
	l = l.Normalize()
	if now.IsZero() {
		now = time.Now()
	}
	item.RetentionScore = EvolvedImportance(item, now, 0.05, 0.1)
	if promoted, ok := l.promote(item, now); ok {
		item.Tier = promoted
		item.PromotionCount++
		item.UpdatedAt = now
	}
	if decayed, ok := l.decay(item, now); ok {
		item.Tier = decayed
		item.UpdatedAt = now
	}
	return item
}

func (l Lifecycle) promote(item Item, now time.Time) (Tier, bool) {
	cfg := l.Normalize().Config
	if item.Kind == KindWorkflow && item.Tier != TierProcedural {
		return TierProcedural, true
	}
	reference := item.LastReinforcedAt
	if reference.IsZero() {
		reference = item.LastAccessedAt
	}
	if reference.IsZero() {
		reference = item.CreatedAt
	}
	if reference.IsZero() {
		reference = item.UpdatedAt
	}
	age := now.Sub(reference)
	switch item.Tier {
	case TierSensory:
		if item.Confidence >= 0.8 || item.AccessCount >= 2 || item.DerivedFromSummary {
			return TierWorking, true
		}
	case TierWorking:
		if age > cfg.M2TTL {
			return "", false
		}
		if item.AccessCount >= cfg.PromotionAccessThreshold || item.DerivedFromSummary || item.RetentionScore >= 0.75 {
			return TierShortTerm, true
		}
	case TierShortTerm:
		if item.DerivedFromSummary || item.Kind == KindTask || item.Scope == ScopeSession {
			return "", false
		}
		if item.AccessCount < cfg.PromotionAccessThreshold || item.Confidence < cfg.PromotionConfidence || item.RetentionScore < 0.55 {
			return "", false
		}
		signals := 0
		if item.AccessCount >= cfg.PromotionAccessThreshold+1 {
			signals++
		}
		if item.Confidence >= cfg.PromotionConfidence+0.1 {
			signals++
		}
		if item.Scope == ScopeGlobal {
			signals++
		}
		if item.Kind == KindPreference || item.Kind == KindConstraint {
			signals++
		}
		if age >= 24*time.Hour {
			signals++
		}
		if item.PromotionCount > 0 {
			signals++
		}
		if signals >= 2 {
			return TierLongTerm, true
		}
	}
	return "", false
}

func (l Lifecycle) decay(item Item, now time.Time) (Tier, bool) {
	cfg := l.Normalize().Config
	reference := item.LastReinforcedAt
	if reference.IsZero() {
		reference = item.LastAccessedAt
	}
	if reference.IsZero() {
		reference = item.UpdatedAt
	}
	if reference.IsZero() {
		reference = item.CreatedAt
	}
	age := now.Sub(reference)
	switch item.Tier {
	case TierSensory:
		if age > cfg.M1TTL {
			return Tier(""), false
		}
	case TierWorking:
		if age > cfg.M2TTL {
			return TierSensory, true
		}
	case TierShortTerm:
		if age > cfg.M2TTL*2 && item.AccessCount <= 1 {
			return TierWorking, true
		}
	}
	return "", false
}

func (l Lifecycle) ShouldDelete(item Item, now time.Time) bool {
	l = l.Normalize()
	if now.IsZero() {
		now = time.Now()
	}
	reference := item.LastReinforcedAt
	if reference.IsZero() {
		reference = item.LastAccessedAt
	}
	if reference.IsZero() {
		reference = item.UpdatedAt
	}
	if reference.IsZero() {
		reference = item.CreatedAt
	}
	if item.Tier == TierSensory && now.Sub(reference) > l.Config.M1TTL*2 {
		return true
	}
	return false
}
