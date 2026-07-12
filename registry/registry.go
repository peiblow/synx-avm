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
	"strings"
	"sync"
	"time"

	"github.com/peiblow/avm/agent"
	"github.com/peiblow/avm/database"
	"github.com/peiblow/avm/llm"
	"github.com/peiblow/avm/smcp"
	goredis "github.com/redis/go-redis/v9"
)

type Registry interface {
	GetAgent(ctx context.Context, agentHash string) (*agent.AgentInfo, error)
}

type AgentRegistry struct {
	Agents    map[string]*agent.AgentInfo
	baseCtx   context.Context
	mcpURL    string
	licenses  map[string]string
	bridges   map[string]*smcp.Bridge
	mu        sync.Mutex
	rdb       *database.RedisClient
	eeapiURL  string
	clientKey ed25519.PrivateKey
	http      *http.Client
}

func NewAgentRegistry(ctx context.Context, mcpURL string, licenses map[string]string, rdb *database.RedisClient) *AgentRegistry {
	eeapiURL := os.Getenv("EEAPI_URL")
	if eeapiURL == "" {
		eeapiURL = "http://localhost:8080"
	}

	return &AgentRegistry{
		Agents:    make(map[string]*agent.AgentInfo),
		baseCtx:   ctx,
		mcpURL:    mcpURL,
		licenses:  licenses,
		bridges:   make(map[string]*smcp.Bridge),
		rdb:       rdb,
		eeapiURL:  eeapiURL,
		clientKey: loadClientKey(os.Getenv("SYNX_PRIVATE_KEY")),
		http:      &http.Client{Timeout: 10 * time.Second},
	}
}

func (r *AgentRegistry) bridgeFor(agentHash string) (*smcp.Bridge, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if b, ok := r.bridges[agentHash]; ok {
		return b, nil
	}

	license, ok := r.licenses[agentHash]
	if !ok {
		return nil, fmt.Errorf("no MCP license configured for agent %s", agentHash)
	}

	b, err := smcp.NewBridge(r.baseCtx, r.mcpURL, license)
	if err != nil {
		return nil, err
	}
	r.bridges[agentHash] = b
	return b, nil
}

func (r *AgentRegistry) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, b := range r.bridges {
		_ = b.Close()
	}
}

func LoadLicenses() (map[string]string, error) {
	if path := os.Getenv("MCP_LICENSES_FILE"); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		return parseLicensesJSON(data)
	}
	if raw := os.Getenv("MCP_LICENSES"); raw != "" {
		return parseLicensesJSON([]byte(raw))
	}
	if single := os.Getenv("MCP_LICENSE"); single != "" {
		hash, err := agentHashFromJWT(single)
		if err != nil {
			return nil, err
		}
		return map[string]string{hash: single}, nil
	}
	return map[string]string{}, nil
}

func parseLicensesJSON(data []byte) (map[string]string, error) {
	m := map[string]string{}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("invalid licenses JSON: %w", err)
	}
	return m, nil
}

func agentHashFromJWT(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("malformed JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", err
	}
	var claims struct {
		AgentHash string `json:"agent_hash"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", err
	}
	if claims.AgentHash == "" {
		return "", fmt.Errorf("license has no agent_hash claim")
	}
	return claims.AgentHash, nil
}

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

	bridge, err := r.bridgeFor(agentHash)
	if err != nil {
		return nil, err
	}

	agt := buildAgent(def, bridge, r, r.rdb)
	r.Agents[agentHash] = agt
	return agt, nil
}

func buildAgent(def Definition, bridge *smcp.Bridge, reg Registry, rdb *database.RedisClient) *agent.AgentInfo {
	cfg := agent.AgentCfg{
		MaxSteps:    def.Behavior.MaxSteps,
		MaxTokens:   def.Model.MaxTokens,
		Temperature: def.Model.Temperature,
	}

	model, err := llm.NewModel(def.Model.Provider, def.Model.Name, cfg)
	if err != nil {
		panic(err)
	}

	tools := map[string]agent.Tool{}
	for _, td := range def.Tools {
		t := newContractTool(td, bridge, reg, rdb)
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
