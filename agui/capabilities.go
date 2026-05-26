package agui

// Capabilities mirrors the AG-UI AgentCapabilities interface returned from
// GET /capabilities. Only populate fields the agent actually supports; omitted
// fields mean the capability is undeclared ("absent = unknown" to clients).
type Capabilities struct {
	Identity       *IdentityCapabilities       `json:"identity,omitempty"`
	Transport      *TransportCapabilities      `json:"transport,omitempty"`
	Tools          *ToolsCapabilities          `json:"tools,omitempty"`
	Output         *OutputCapabilities         `json:"output,omitempty"`
	State          *StateCapabilities          `json:"state,omitempty"`
	MultiAgent     *MultiAgentCapabilities     `json:"multiAgent,omitempty"`
	Reasoning      *ReasoningCapabilities      `json:"reasoning,omitempty"`
	Multimodal     *MultimodalCapabilities     `json:"multimodal,omitempty"`
	Execution      *ExecutionCapabilities      `json:"execution,omitempty"`
	HumanInTheLoop *HumanInTheLoopCapabilities `json:"humanInTheLoop,omitempty"`
	Custom         map[string]any              `json:"custom,omitempty"`
}

// IdentityCapabilities provides agent metadata for discovery UIs and routing.
type IdentityCapabilities struct {
	Name             *string        `json:"name,omitempty"`
	Type             *string        `json:"type,omitempty"`
	Description      *string        `json:"description,omitempty"`
	Version          *string        `json:"version,omitempty"`
	Provider         *string        `json:"provider,omitempty"`
	DocumentationURL *string        `json:"documentationUrl,omitempty"`
	Metadata         map[string]any `json:"metadata,omitempty"`
}

// TransportCapabilities declares supported transport mechanisms.
type TransportCapabilities struct {
	Streaming         *bool `json:"streaming,omitempty"`
	Websocket         *bool `json:"websocket,omitempty"`
	HTTPBinary        *bool `json:"httpBinary,omitempty"`
	PushNotifications *bool `json:"pushNotifications,omitempty"`
	Resumable         *bool `json:"resumable,omitempty"`
}

// Tool describes a tool the agent provides, using JSON Schema for parameters.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  any            `json:"parameters"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// ToolsCapabilities declares tool calling support.
type ToolsCapabilities struct {
	Supported      *bool  `json:"supported,omitempty"`
	Items          []Tool `json:"items,omitempty"`
	ParallelCalls  *bool  `json:"parallelCalls,omitempty"`
	ClientProvided *bool  `json:"clientProvided,omitempty"`
}

// OutputCapabilities declares output format support.
type OutputCapabilities struct {
	StructuredOutput   *bool    `json:"structuredOutput,omitempty"`
	SupportedMIMETypes []string `json:"supportedMimeTypes,omitempty"`
}

// StateCapabilities declares state synchronization support.
type StateCapabilities struct {
	Snapshots       *bool `json:"snapshots,omitempty"`
	Deltas          *bool `json:"deltas,omitempty"`
	Memory          *bool `json:"memory,omitempty"`
	PersistentState *bool `json:"persistentState,omitempty"`
}

// MultiAgentCapabilities declares multi-agent coordination support.
type MultiAgentCapabilities struct {
	Supported  *bool      `json:"supported,omitempty"`
	Delegation *bool      `json:"delegation,omitempty"`
	Handoffs   *bool      `json:"handoffs,omitempty"`
	SubAgents  []SubAgent `json:"subAgents,omitempty"`
}

// SubAgent describes a sub-agent available for delegation.
type SubAgent struct {
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
}

// ReasoningCapabilities declares reasoning/thinking support.
type ReasoningCapabilities struct {
	Supported *bool `json:"supported,omitempty"`
	Streaming *bool `json:"streaming,omitempty"`
	Encrypted *bool `json:"encrypted,omitempty"`
}

// MultimodalCapabilities declares multimodal input/output support.
type MultimodalCapabilities struct {
	Input  *MultimodalInputCapabilities  `json:"input,omitempty"`
	Output *MultimodalOutputCapabilities `json:"output,omitempty"`
}

// MultimodalInputCapabilities declares accepted input modalities.
type MultimodalInputCapabilities struct {
	Image *bool `json:"image,omitempty"`
	Audio *bool `json:"audio,omitempty"`
	Video *bool `json:"video,omitempty"`
	PDF   *bool `json:"pdf,omitempty"`
	File  *bool `json:"file,omitempty"`
}

// MultimodalOutputCapabilities declares produced output modalities.
type MultimodalOutputCapabilities struct {
	Image *bool `json:"image,omitempty"`
	Audio *bool `json:"audio,omitempty"`
}

// ExecutionCapabilities declares execution control and limits.
type ExecutionCapabilities struct {
	CodeExecution    *bool `json:"codeExecution,omitempty"`
	Sandboxed        *bool `json:"sandboxed,omitempty"`
	MaxIterations    *int  `json:"maxIterations,omitempty"`
	MaxExecutionTime *int  `json:"maxExecutionTime,omitempty"`
}

// HumanInTheLoopCapabilities declares human-in-the-loop support per the AG-UI
// capabilities spec. See https://docs.ag-ui.com/concepts/capabilities#human-in-the-loop.
type HumanInTheLoopCapabilities struct {
	Supported        *bool `json:"supported,omitempty"`
	Approvals        *bool `json:"approvals,omitempty"`
	Interventions    *bool `json:"interventions,omitempty"`
	Feedback         *bool `json:"feedback,omitempty"`
	Interrupts       *bool `json:"interrupts,omitempty"`
	ApproveWithEdits *bool `json:"approveWithEdits,omitempty"`
}

// MergeInterruptCapabilities ensures humanInTheLoop.interrupts and
// humanInTheLoop.approveWithEdits are set to true when left unset.
//
// This launcher always implements the AG-UI interrupt emit/resume path for ADK
// tool confirmations, so advertising those flags helps CopilotKit and other
// clients enable approval UI without each integrator remembering to set them.
// Callers who pass a fully custom [Capabilities] document via [WithCapabilities]
// still get this merge so discovery stays accurate; set Interrupts or
// ApproveWithEdits explicitly to false to opt out.
//
// MergeInterruptCapabilities does not set humanInTheLoop.supported or other
// HITL fields—only the interrupt-protocol-specific flags from the AG-UI spec.
func MergeInterruptCapabilities(caps *Capabilities) {
	if caps.HumanInTheLoop == nil {
		caps.HumanInTheLoop = &HumanInTheLoopCapabilities{}
	}
	if caps.HumanInTheLoop.Interrupts == nil {
		caps.HumanInTheLoop.Interrupts = new(true)
	}
	if caps.HumanInTheLoop.ApproveWithEdits == nil {
		caps.HumanInTheLoop.ApproveWithEdits = new(true)
	}
}

// DefaultInterruptCapabilities returns a minimal [Capabilities] value suitable
// for agents that only need to advertise AG-UI interrupt resume for ADK tool
// confirmations. Combine with other capability structs via manual field assignment
// if you need a fuller discovery document.
func DefaultInterruptCapabilities() Capabilities {
	caps := Capabilities{}
	MergeInterruptCapabilities(&caps)
	return caps
}
