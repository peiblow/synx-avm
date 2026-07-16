package agent

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

func (a *AgentInfo) Run(ctx context.Context, msgs []Message) ([]Message, error) {
	if a.SystemPrompt != "" && (len(msgs) == 0 || msgs[0].Role != "system") {
		msgs = append([]Message{{Role: "system", Content: a.SystemPrompt}}, msgs...)
	}

	maxSteps := a.Cfg.MaxSteps
	if maxSteps <= 0 {
		maxSteps = 10
	}
	terminal := a.finishTool()

	for step := 0; step < maxSteps; step++ {
		llmStart := time.Now()
		out, err := a.Model.Complete(ctx, msgs, specs(a.Tools), ChoiceAuto)
		slog.Info("llm call", "step", step, "ms", time.Since(llmStart).Milliseconds())
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, assistantMessage(out))

		if len(out.ToolCalls) == 0 {
			if terminal == "" {
				return msgs, nil
			}
			forced, err := a.forceFinish(ctx, msgs, terminal)
			if err != nil {
				return nil, err
			}
			msgs = append(msgs, assistantMessage(forced))
			if len(forced.ToolCalls) == 0 {
				return msgs, nil
			}
			out = forced
		}

		msgs = append(msgs, a.runCalls(ctx, out.ToolCalls)...)

		if terminal != "" && calledTool(out.ToolCalls, terminal) {
			return msgs, nil
		}
	}

	if terminal != "" {
		forced, err := a.forceFinish(ctx, msgs, terminal)
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, assistantMessage(forced))
		if len(forced.ToolCalls) > 0 {
			msgs = append(msgs, a.runCalls(ctx, forced.ToolCalls)...)
		}
	}
	return msgs, nil
}

func (a *AgentInfo) finishTool() string {
	name := a.Cfg.OnFinish
	if name == "" {
		return ""
	}
	if _, ok := a.Tools[name]; !ok {
		return ""
	}
	return name
}

func (a *AgentInfo) forceFinish(ctx context.Context, msgs []Message, terminal string) (Completion, error) {
	nudged := append(msgs[:len(msgs):len(msgs)], Message{
		Role:    "system",
		Content: "You must finish now by calling the " + terminal + " tool with your final answer.",
	})
	only := map[string]Tool{terminal: a.Tools[terminal]}
	llmStart := time.Now()
	out, err := a.Model.Complete(ctx, nudged, specs(only), ChoiceRequired)
	slog.Info("llm call", "step", "finish", "ms", time.Since(llmStart).Milliseconds())
	return out, err
}

func (a *AgentInfo) runCalls(ctx context.Context, calls []ToolCall) []Message {
	results := make([]Message, len(calls))
	var wg sync.WaitGroup

	for i, call := range calls {
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
	return results
}

func calledTool(calls []ToolCall, name string) bool {
	for _, c := range calls {
		if c.Name == name {
			return true
		}
	}
	return false
}
