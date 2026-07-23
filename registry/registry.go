package registry

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/peiblow/avm/agent"
	"github.com/peiblow/avm/database"
	"github.com/peiblow/avm/llm"
	goredis "github.com/redis/go-redis/v9"
)

type Registry interface {
	GetAgent(ctx context.Context, agentHash string) (*agent.AgentInfo, error)
}

type AgentRegistry struct {
	Agents    map[string]*agent.AgentInfo
	baseCtx   context.Context
	gate      *gateClient
	mu        sync.Mutex
	rdb       *database.RedisClient
	eeapiURL  string
	clientKey ed25519.PrivateKey
	http      *http.Client
}

func NewAgentRegistry(ctx context.Context, rdb *database.RedisClient) *AgentRegistry {
	eeapiURL := os.Getenv("EEAPI_URL")
	if eeapiURL == "" {
		eeapiURL = "http://localhost:8080"
	}
	clientKey := loadClientKey(os.Getenv("SYNX_PRIVATE_KEY"))

	return &AgentRegistry{
		Agents:    make(map[string]*agent.AgentInfo),
		baseCtx:   ctx,
		gate:      &gateClient{eeapiURL: eeapiURL, http: &http.Client{Timeout: 30 * time.Second}, clientKey: clientKey},
		rdb:       rdb,
		eeapiURL:  eeapiURL,
		clientKey: clientKey,
		http:      &http.Client{Timeout: 10 * time.Second},
	}
}

func (r *AgentRegistry) gateFor(agentHash string) gate {
	return &agentGate{client: r.gate, agentHash: agentHash}
}

func (r *AgentRegistry) Close() {}

func loadClientKey(derHex string) ed25519.PrivateKey {
	raw, err := hex.DecodeString(derHex)
	if err != nil || len(raw) < 32 {
		return nil
	}
	return ed25519.NewKeyFromSeed(raw[len(raw)-32:])
}

func mintClientJWT(key ed25519.PrivateKey) (string, error) {
	if key == nil {
		return "", fmt.Errorf("SYNX_PRIVATE_KEY not set; cannot authenticate to EEAPI")
	}
	b64 := func(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
	header, _ := json.Marshal(map[string]string{"alg": "EdDSA", "typ": "JWT"})
	now := time.Now().Unix()
	payload, _ := json.Marshal(map[string]int64{"iat": now, "exp": now + 300})
	signingInput := b64(header) + "." + b64(payload)
	sig := ed25519.Sign(key, []byte(signingInput))
	return signingInput + "." + b64(sig), nil
}

func (r *AgentRegistry) GetAgent(ctx context.Context, agentHash string) (*agent.AgentInfo, error) {
	if a := r.Agents[agentHash]; a != nil {
		return a, nil
	}

	def, err := r.getDefinition(ctx, agentHash)
	if err != nil {
		fmt.Println("Error getting agent definition:", err)
		return nil, err
	}

	g := r.gateFor(agentHash)

	agt := buildAgent(def, g, r, r.rdb)
	r.Agents[agentHash] = agt
	return agt, nil
}

func buildAgent(def Definition, g gate, reg Registry, rdb *database.RedisClient) *agent.AgentInfo {
	cfg := agent.AgentCfg{
		MaxSteps:    def.Behavior.MaxSteps,
		MaxTokens:   def.Model.MaxTokens,
		Temperature: def.Model.Temperature,
		OnFinish:    def.Behavior.OnFinish,
		OnDeny:      def.Behavior.OnDeny,
	}

	model, err := llm.NewModel(def.Model.Provider, def.Model.Name, cfg)
	if err != nil {
		panic(err)
	}

	tools := map[string]agent.Tool{}
	for _, td := range def.Tools {
		t := newContractTool(td, g, reg, rdb)
		tools[t.Spec().Name] = t
	}

	return agent.NewAgent(model, tools, cfg, def.Version, def.SystemPrompt)
}

func (r *AgentRegistry) getDefinition(ctx context.Context, hash string) (Definition, error) {
	raw, err := r.rdb.Get(ctx, "synx:agent:"+hash)
	if err != nil {
		if err == goredis.Nil {
			return r.fetchDefinitionFromEEAPI(ctx, hash)
		}
		return Definition{}, err
	}

	var def Definition
	if err := json.Unmarshal([]byte(raw), &def); err != nil {
		return Definition{}, err
	}

	return def, nil
}

func (r *AgentRegistry) fetchDefinitionFromEEAPI(ctx context.Context, hash string) (Definition, error) {
	url := r.eeapiURL + "/agent/" + hash + "/definition"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Definition{}, err
	}

	token, err := mintClientJWT(r.clientKey)
	if err != nil {
		return Definition{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := r.http.Do(req)
	if err != nil {
		return Definition{}, fmt.Errorf("failed to reach EEAPI: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Definition{}, fmt.Errorf("EEAPI returned %d for agent %s", resp.StatusCode, hash)
	}

	var def Definition
	if err := json.NewDecoder(resp.Body).Decode(&def); err != nil {
		return Definition{}, err
	}

	return def, nil
}
