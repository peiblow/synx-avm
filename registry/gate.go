package registry

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type Result struct {
	ContextID string
	Decision  string
	Raw       json.RawMessage
	Steps     []StepResult
}

type StepResult struct {
	Function string      `json:"function"`
	Status   string      `json:"status"`
	Events   []StepEvent `json:"events"`
}

type StepEvent struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

type gateClient struct {
	eeapiURL  string
	http      *http.Client
	clientKey ed25519.PrivateKey
}

func (c *gateClient) call(ctx context.Context, agentHash, toolName string, input json.RawMessage) (*Result, error) {
	if len(input) == 0 {
		input = []byte("{}")
	}

	url := c.eeapiURL + "/agent/" + agentHash + "/gate/" + toolName
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(input))
	if err != nil {
		return nil, err
	}

	token, err := mintClientJWT(c.clientKey)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("gate %s/%s: status %d: %s", agentHash, toolName, resp.StatusCode, string(body))
	}

	var decoded struct {
		ContextID string       `json:"contextId"`
		Decision  string       `json:"decision"`
		Steps     []StepResult `json:"steps"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("gate response: %w (%s)", err, string(body))
	}

	return &Result{
		ContextID: decoded.ContextID,
		Decision:  decoded.Decision,
		Raw:       json.RawMessage(body),
		Steps:     decoded.Steps,
	}, nil
}

type agentGate struct {
	client    *gateClient
	agentHash string
}

func (g *agentGate) Call(ctx context.Context, gateName string, input json.RawMessage) (*Result, error) {
	return g.client.call(ctx, g.agentHash, gateName, input)
}
