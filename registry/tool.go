package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/peiblow/avm/agent"
	"github.com/peiblow/avm/smcp"
)

// maxDepth limita a profundidade da cadeia de delegação agente→agente. Mesmo
// sendo acíclica por design, protege contra contrato mal-configurado (A→B→A).
const maxDepth = 5

// contractTool une as metades de um step do contrato: o gate (function,
// decidido pelo synx-mcp) e o efeito executado pela AVM após APPROVED — uma
// action HTTP ou a delegação a outro agente.
type contractTool struct {
	spec     agent.ToolsSpec
	gateName string       // nome real no contrato/gate (ex: "credit.decision")
	steps    []ToolStep   // do contrato; cada um tem uma Action ou um Delegate
	bridge   *smcp.Bridge
	reg      Registry // resolve o agente alvo de uma delegação
	http     *http.Client
}

func newContractTool(def ToolDef, bridge *smcp.Bridge, reg Registry) *contractTool {
	return &contractTool{
		spec:     agent.ToolsSpec{Name: sanitizeName(def.Name), Description: def.Description},
		gateName: def.Name,
		steps:    def.Steps,
		bridge:   bridge,
		reg:      reg,
		http:     &http.Client{Timeout: 30 * time.Second},
	}
}

func (t *contractTool) Spec() agent.ToolsSpec { return t.spec }

// Run propõe ao gate; se APPROVED, executa a(s) action(s) e devolve o resultado
// real. Se REJECTED, devolve o motivo ao modelo sem executar nada.
func (t *contractTool) Run(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	res, err := t.bridge.Call(ctx, t.gateName, input)
	if err != nil {
		return nil, err
	}

	if res.Decision != "APPROVED" {
		return res.Raw, nil
	}

	outputs := make([]json.RawMessage, 0, len(t.steps))
	for _, step := range t.steps {
		var out json.RawMessage
		var err error
		switch {
		case step.Action != nil:
			out, err = t.executeAction(ctx, step.Action, input)
			if err != nil {
				return nil, fmt.Errorf("action %s %s: %w", step.Action.Method, step.Action.Url, err)
			}
		case step.Delegate != "":
			out, err = t.delegate(ctx, step.Delegate, input)
			if err != nil {
				return nil, fmt.Errorf("delegate %s: %w", step.Delegate, err)
			}
		default:
			continue
		}
		outputs = append(outputs, out)
	}

	return combine(res.Raw, outputs), nil
}

// delegate roda o agente alvo in-process (síncrono) e devolve a resposta final
// dele como resultado da tool. B não passa pelo consumer, então não tem memória
// de conversa própria — é uma função pura dentro do turno de A.
func (t *contractTool) delegate(ctx context.Context, target string, input json.RawMessage) (json.RawMessage, error) {
	depth := depthFrom(ctx)
	if depth >= maxDepth {
		return nil, fmt.Errorf("max delegation depth %d exceeded", maxDepth)
	}

	agt, err := t.reg.GetAgent(ctx, target)
	if err != nil {
		return nil, err
	}

	msgs, err := agt.Run(withDepth(ctx, depth+1), []agent.Message{
		{Role: "user", Content: string(input)},
	})
	if err != nil {
		return nil, err
	}

	final := ""
	if len(msgs) > 0 {
		final = msgs[len(msgs)-1].Content
	}
	out, _ := json.Marshal(struct {
		Agent  string `json:"agent"`
		Output string `json:"output"`
	}{Agent: target, Output: final})
	return out, nil
}

// executeAction faz a chamada HTTP da action, com os args do LLM no corpo.
// Auth fica deferida (futuro: env/secret por tenant, nunca no contrato).
func (t *contractTool) executeAction(ctx context.Context, action *ToolAction, input json.RawMessage) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, action.Method, action.Url, bytes.NewReader(input))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}
	return json.RawMessage(body), nil
}

// combine junta a decisão do gate com os resultados das actions num só payload
// pro modelo entender o que foi aprovado e o que a execução retornou.
func combine(decision json.RawMessage, outputs []json.RawMessage) json.RawMessage {
	out, err := json.Marshal(struct {
		Decision json.RawMessage   `json:"decision"`
		Actions  []json.RawMessage `json:"actions,omitempty"`
	}{Decision: decision, Actions: outputs})
	if err != nil {
		return decision
	}
	return out
}

// depthKey carrega a profundidade da cadeia de delegação no ctx.
type depthKey struct{}

func depthFrom(ctx context.Context) int {
	d, _ := ctx.Value(depthKey{}).(int)
	return d
}

func withDepth(ctx context.Context, d int) context.Context {
	return context.WithValue(ctx, depthKey{}, d)
}

// synx-mcp usa nomes com ponto (credit.decision); a maioria dos modelos exige
// ^[a-zA-Z0-9_-]+$. Sanitizamos pro modelo; o gateName guarda o nome real.
var nameRe = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

func sanitizeName(name string) string {
	return nameRe.ReplaceAllString(name, "_")
}
