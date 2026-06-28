package registry

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

type ModelSpec struct {
	Provider    string  `json:"provider"`
	Name        string  `json:"name"`
	Temperature float64 `json:"temperature"`
	MaxTokens   int     `json:"max_tokens"`
}

type BehaviorSpec struct {
	MaxSteps int    `json:"max_steps"`
	OnDeny   string `json:"on_deny"`
	OnError  string `json:"on_error"`
}

type ToolDef struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Steps       []ToolStep `json:"steps"`
}

type ToolStepInput struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type ToolStep struct {
	Function string          `json:"function"`
	Input    []ToolStepInput `json:"input,omitempty"`
	Action   *ToolAction     `json:"action,omitempty"`
	Delegate string          `json:"delegate,omitempty"`
}

type ToolAction struct {
	Method string `json:"method"`
	Url    string `json:"url"`
}

type SkillDef struct {
	Name    string   `json:"name"`
	Content string   `json:"content"`
	Uses    []string `json:"uses"`
}
