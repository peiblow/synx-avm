package agent

import (
	"context"
	"sync"
)

func (a *AgentInfo) Run(ctx context.Context, msgs []Message) ([]Message, error) {
	if a.SystemPrompt != "" && (len(msgs) == 0 || msgs[0].Role != "system") {
		msgs = append([]Message{{Role: "system", Content: a.SystemPrompt}}, msgs...)
	}

	maxSteps := a.Cfg.MaxSteps
	if maxSteps <= 0 {
		maxSteps = 10
	}

	for step := 0; step < maxSteps; step++ {
		if step == maxSteps-1 {
			msgs = append(msgs, Message{
				Role:    "system",
				Content: "You have reached your step budget. Do not call any tool other than reply. Call reply now with your best answer from what you have already read.",
			})
		}

		out, err := a.Model.Complete(ctx, msgs, specs(a.Tools))
		if err != nil {
			return nil, err
		}

		msgs = append(msgs, assistantMessage(out))

		if len(out.ToolCalls) == 0 {
			return msgs, nil
		}

		results := make([]Message, len(out.ToolCalls))
		var wg sync.WaitGroup

		for i, call := range out.ToolCalls {
			tool, ok := a.Tools[call.Name]
			if !ok {
				results[i] = toolError(call, "tool not found: "+call.Name)
				continue
			}

			wg.Add(1)
			go func(i int, call ToolCall) {
				defer wg.Done()
				res, err := tool.Run(ctx, call.Input)
				if err != nil {
					results[i] = toolError(call, err.Error())
					return
				}

				results[i] = toolResult(call, res)
			}(i, call)
		}

		wg.Wait()
		msgs = append(msgs, results...)
	}

	return msgs, nil
}
