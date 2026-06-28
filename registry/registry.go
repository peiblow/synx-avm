package registry

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/peiblow/avm/agent"
	"github.com/peiblow/avm/database"
	"github.com/peiblow/avm/llm"
	"github.com/peiblow/avm/smcp"
)

type Registry interface {
	GetAgent(ctx context.Context, agentHash string) (*agent.AgentInfo, error)
}

type AgentRegistry struct {
	Agents map[string]*agent.AgentInfo
	bridge *smcp.Bridge
	rdb    *database.RedisClient
}

func NewAgentRegistry(bridge *smcp.Bridge, rdb *database.RedisClient) *AgentRegistry {
	return &AgentRegistry{
		Agents: make(map[string]*agent.AgentInfo),
		bridge: bridge,
		rdb:    rdb,
	}
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

	agt := buildAgent(def, r.bridge, r)
	r.Agents[agentHash] = agt
	return agt, nil
}

func buildAgent(def Definition, bridge *smcp.Bridge, reg Registry) *agent.AgentInfo {
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
		t := newContractTool(td, bridge, reg)
		tools[t.Spec().Name] = t
	}

	return agent.NewAgent(model, tools, cfg, def.Version)
}

func (r *AgentRegistry) getDefinition(ctx context.Context, hash string) (Definition, error) {
	raw, err := r.rdb.Get(ctx, "synx:agent:"+hash)
	if err != nil {
		return Definition{}, err
	}

	var def Definition
	if err := json.Unmarshal([]byte(raw), &def); err != nil {
		return Definition{}, err
	}

	return def, nil
}
