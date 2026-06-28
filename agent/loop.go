package agent

import (
	"context"
	"sync"
)

func (a *AgentInfo) Run(ctx context.Context, msgs []Message) ([]Message, error) {
	for {
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
}
