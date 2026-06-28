package smcp

import (
	"context"
	"encoding/json"
	"fmt"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

// Bridge é o cliente MCP da AVM contra o synx-mcp (o gate). Ele só obtém a
// DECISÃO (APPROVED/REJECTED) de uma tool governada. Quem executa efeito
// externo (a action) é a AVM — synx-control-plane-only: o Synx só decide.
type Bridge struct {
	client *mcpclient.SSEMCPClient
}

// Result é a decisão devolvida pelo gate para uma chamada de tool.
type Result struct {
	ContextID string
	Decision  string          // "APPROVED" | "REJECTED"
	Raw       json.RawMessage // payload completo, para devolver ao modelo
}

// NewBridge conecta no synx-mcp e autentica com a license.
// url ex: https://mcp.synxhub.com.br/mcp/sse ; license = MCP_LICENSE (JWT EdDSA).
func NewBridge(ctx context.Context, url, license string) (*Bridge, error) {
	c, err := mcpclient.NewSSEMCPClient(url, mcpclient.WithHeaders(map[string]string{
		"Authorization": "Bearer " + license,
	}))
	if err != nil {
		return nil, fmt.Errorf("new sse client: %w", err)
	}

	if err := c.Start(ctx); err != nil {
		return nil, fmt.Errorf("start sse: %w", err)
	}

	var initReq mcp.InitializeRequest
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "avm", Version: "0.1.0"}
	if _, err := c.Initialize(ctx, initReq); err != nil {
		return nil, fmt.Errorf("initialize: %w", err)
	}

	return &Bridge{client: c}, nil
}

func (b *Bridge) Close() error {
	return b.client.Close()
}

// Call propõe a tool ao gate e devolve a decisão. gateName é o nome real da
// tool no contrato (ex: "credit.decision"). input são os args do LLM.
//
// REJECTED volta com IsError=true mas carrega uma decisão — é resultado de
// negócio legítimo que o modelo precisa ver, então devolvemos como dado.
// Só erro de transporte / sem decisão é tratado como falha de verdade.
func (b *Bridge) Call(ctx context.Context, gateName string, input json.RawMessage) (*Result, error) {
	var args map[string]any
	if len(input) > 0 {
		if err := json.Unmarshal(input, &args); err != nil {
			return nil, fmt.Errorf("invalid tool input: %w", err)
		}
	}

	var req mcp.CallToolRequest
	req.Params.Name = gateName
	req.Params.Arguments = args

	res, err := b.client.CallTool(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("call tool %s: %w", gateName, err)
	}

	text := firstText(res.Content)

	var decoded struct {
		ContextID string `json:"contextId"`
		Decision  string `json:"decision"`
	}
	_ = json.Unmarshal([]byte(text), &decoded)

	if res.IsError && decoded.Decision == "" {
		return nil, fmt.Errorf("tool %s failed: %s", gateName, text)
	}

	return &Result{
		ContextID: decoded.ContextID,
		Decision:  decoded.Decision,
		Raw:       json.RawMessage(text),
	}, nil
}

func firstText(content []mcp.Content) string {
	for _, c := range content {
		if tc, ok := mcp.AsTextContent(c); ok {
			return tc.Text
		}
	}
	return ""
}
