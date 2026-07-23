package agent

import (
	"context"
	"errors"
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

		toolMsgs, denied := a.runCalls(ctx, out.ToolCalls)
		msgs = append(msgs, toolMsgs...)

		if terminal != "" && calledTool(out.ToolCalls, terminal) {
			return msgs, nil
		}

		if denied && a.denyHalts() {
			slog.Info("halting on gate denial", "step", step)
			break
		}
	}

	if terminal != "" {
		forced, err := a.forceFinish(ctx, msgs, terminal)
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, assistantMessage(forced))
		if len(forced.ToolCalls) > 0 {
			toolMsgs, _ := a.runCalls(ctx, forced.ToolCalls)
			msgs = append(msgs, toolMsgs...)
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

func (a *AgentInfo) runCalls(ctx context.Context, calls []ToolCall) ([]Message, bool) {
	results := make([]Message, len(calls))
	denied := make([]bool, len(calls))
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
			switch {
			case errors.Is(err, ErrDenied):
				results[i] = toolResult(call, res)
				denied[i] = true
			case err != nil:
				results[i] = toolError(call, err.Error())
			default:
				results[i] = toolResult(call, res)
			}
		}(i, call)
	}

	wg.Wait()

	for _, d := range denied {
		if d {
			return results, true
		}
	}
	return results, false
}

func (a *AgentInfo) denyHalts() bool {
	switch a.Cfg.OnDeny {
	case "halt":
		return true
	case "", "reflect":
		return false
	default:
		slog.Warn("unknown onDeny value, treating as reflect", "value", a.Cfg.OnDeny)
		return false
	}
}

func calledTool(calls []ToolCall, name string) bool {
	for _, c := range calls {
		if c.Name == name {
			return true
		}
	}
	return false
}
