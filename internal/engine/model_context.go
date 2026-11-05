package engine

// modelContextResolver freezes settings for background task graphs. Do not bind
// their callback to the main run, whose config refreshes on model boundaries.
func (e *Engine) modelContextResolver() func(string) int64 {
	resolve, fallback := e.config.ModelContextLimit, e.config.MaxContextTokens
	return func(model string) int64 {
		if resolve != nil {
			if limit := resolve(model); limit > 0 {
				return limit
			}
		}
		return fallback
	}
}

func (e *Engine) modelContextLimit(model string) int64 {
	if e.config.ModelContextLimit != nil {
		if limit := e.config.ModelContextLimit(model); limit > 0 {
			return limit
		}
	}
	return e.config.MaxContextTokens
}
