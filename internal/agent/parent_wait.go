package agent

import "context"

// WaitRunOrQuestion also wakes for a sibling question owned by this parent.
// It neither consumes the mailbox nor cancels the child.
func (c *Coordinator) WaitRunOrQuestion(ctx context.Context, id AgentID, runID string, scope Scope) (AgentResult, *ParentRequest, error) {
	for {
		wake, _, _ := c.ParentActivity(scope)
		c.mu.RLock()
		a := c.agents[id]
		live := a != nil && a.status.RunID == runID && active(a)
		c.mu.RUnlock()
		if !live {
			result, err := c.WaitRun(ctx, id, runID)
			return result, nil, err
		}
		questions, err := c.ParentQuestions(ctx, scope)
		if err != nil {
			return AgentResult{}, nil, err
		}
		if len(questions) > 0 {
			return AgentResult{}, &questions[0], nil
		}
		select {
		case <-ctx.Done():
			return AgentResult{}, nil, ctx.Err()
		case <-a.done:
			c.mu.RLock()
			result := a.result
			c.mu.RUnlock()
			return result, nil, nil
		case <-wake:
		}
	}
}
