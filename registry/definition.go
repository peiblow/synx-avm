package registry

// Definition espelha a projeção do contrato no Postgres
// (contract_agents + agent_tools + agent_skills). É exatamente o que um
// scan dessas tabelas produz. Hoje vem de um fake (mock.go); amanhã, do PSQL.
// O artifact .snxb continua sendo a fonte canônica e imutável: hash diferente
// => Definition diferente. Por isso o registry pode cachear por hash.
type Definition struct {
	Hash         string       `json:"hash"`          // contract_agents._hash
	Name         string       `json:"name"`          // contract_agents.name
	Version      string       `json:"version"`       // contract_agents.version
	Purpose      string       `json:"purpose"`       // contract_agents.purpose
	SystemPrompt string       `json:"system_prompt"` // contract_agents.system_prompt (.md embarcado)
	Model        ModelSpec    `json:"model"`         // contract_agents.model (JSONB)
	Behavior     BehaviorSpec `json:"behavior"`      // contract_agents.behavior (JSONB)
	Tools        []ToolDef    `json:"tools"`         // agent_tools
	Skills       []SkillDef   `json:"skills"`        // agent_skills
}

// ModelSpec = contract_agents.model JSONB: { provider, name, temperature, max_tokens }
type ModelSpec struct {
	Provider    string  `json:"provider"`
	Name        string  `json:"name"`
	Temperature float64 `json:"temperature"`
	MaxTokens   int     `json:"max_tokens"`
}

// BehaviorSpec = contract_agents.behavior JSONB: { max_steps, on_deny, on_error }
type BehaviorSpec struct {
	MaxSteps int    `json:"max_steps"`
	OnDeny   string `json:"on_deny"`
	OnError  string `json:"on_error"`
}

// ToolDef = uma linha de agent_tools. Steps é o JSONB steps.
type ToolDef struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Steps       []ToolStep `json:"steps"`
}

// ToolStep / ToolAction espelham o formato do contrato:
// { function, action: { method, url } }. É o que a Tool concreta vai usar
// pra montar a proposta ao gate (a divergência do Synx).
type ToolStep struct {
	Function string      `json:"function"`
	Action   *ToolAction `json:"action,omitempty"`
	// Delegate é o agente alvo (hash/nome) a rodar após aprovação, em vez de
	// uma action HTTP. É a delegação acíclica agente→agente, síncrona e in-process.
	Delegate string `json:"delegate,omitempty"`
}

type ToolAction struct {
	Method string `json:"method"`
	Url    string `json:"url"`
}

// SkillDef = uma linha de agent_skills.
type SkillDef struct {
	Name    string   `json:"name"`
	Content string   `json:"content"`
	Uses    []string `json:"uses"`
}
