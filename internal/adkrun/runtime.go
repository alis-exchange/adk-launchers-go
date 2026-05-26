// Package adkrun runs ADK agents in-process from launchers (scheduler cron ticks, etc.).
//
// Construct a [Runtime] with [NewRuntime] and [launcher.Config], then call [Runtime.RunSSE]
// and range over the returned event iterator.
package adkrun

import (
	"context"
	"fmt"
	"iter"
	"slices"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/cmd/launcher"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

// Content and Part are the genai message types used in [RunRequest.NewMessage].
// Part supports text, inlineData, fileData, functionCall, functionResponse,
// executableCode, codeExecutionResult, toolCall, toolResponse, and related fields
// documented in the ADK REST API.
type (
	Content = genai.Content
	Part    = genai.Part
	// Event is a single ADK session event emitted while an agent runs.
	Event = session.Event
)

// RunRequest describes an agent run. Field names align with the ADK REST run API
// (https://adk.dev/api-reference/rest/#/default/run_agent_sse_run_sse_post) for parity
// with HTTP clients and tooling.
type RunRequest struct {
	// AppName is the ADK application name to run.
	AppName string `json:"appName,omitempty"`
	// UserID is the user ID to run the agent for.
	UserID string `json:"userId"`
	// SessionID identifies the ADK session to continue. When empty a new UUID is generated and returned.
	SessionID string `json:"sessionId"`
	// NewMessage is the user or model turn to append. Use genai helpers such as
	// [genai.NewContentFromText] or [genai.NewPartFromFunctionResponse] to build parts.
	NewMessage Content `json:"newMessage"`
	// Streaming enables partial SSE-style events from the model when true.
	Streaming bool `json:"streaming,omitempty"`
	// SaveInputBlobsAsArtifacts saves blob parts in NewMessage (images, files) as artifacts.
	SaveInputBlobsAsArtifacts bool `json:"saveInputBlobsAsArtifacts,omitempty"`
	// StateDelta merges into the session state before the run (ADK runner.WithStateDelta).
	StateDelta map[string]any `json:"stateDelta,omitempty"`
	// FunctionCallEventID resumes or continues a pending function call.
	// Not yet applied by the in-process runner; reserved for future ADK support.
	FunctionCallEventID string `json:"functionCallEventId,omitempty"`
	// InvocationID correlates the run with a prior invocation.
	// Not yet applied by the in-process runner; reserved for future ADK support.
	InvocationID string `json:"invocationId,omitempty"`
}

// UserTextMessage returns a user [Content] with a single text part.
func UserTextMessage(text string) Content {
	return *genai.NewContentFromText(text, genai.RoleUser)
}

// Runtime runs agents in-process using the same services wired into [launcher.Config].
type Runtime struct {
	launcherCfg *launcher.Config
	appName     string
}

// NewRuntime builds an in-process runner for appName using the ADK launcher config.
func NewRuntime(launcherCfg *launcher.Config, appName string) (*Runtime, error) {
	if launcherCfg == nil {
		return nil, fmt.Errorf("adkrun: launcher config is required")
	}
	if launcherCfg.AgentLoader == nil {
		return nil, fmt.Errorf("adkrun: AgentLoader is required")
	}
	if launcherCfg.SessionService == nil {
		return nil, fmt.Errorf("adkrun: SessionService is required")
	}
	appName = strings.TrimSpace(appName)
	if appName == "" {
		return nil, fmt.Errorf("adkrun: app name is required")
	}
	return &Runtime{launcherCfg: launcherCfg, appName: appName}, nil
}

// AppName returns the ADK application name this runtime executes.
func (rt *Runtime) AppName() string {
	return rt.appName
}

// RunSSE executes an agent turn and returns the session id plus an iterator of ADK
// session events. Callers range over events until the iterator completes or returns
// an error; set [RunRequest.Streaming] to receive partial model tokens.
func (rt *Runtime) RunSSE(ctx context.Context, req RunRequest) (string, iter.Seq2[*Event, error], error) {
	if strings.TrimSpace(req.UserID) == "" {
		return "", nil, fmt.Errorf("adkrun: user id is required")
	}
	if len(req.NewMessage.Parts) == 0 {
		return "", nil, fmt.Errorf("adkrun: newMessage.parts is required")
	}

	// Override appName from the request. Note: the same SessionService and
	// ArtifactService are used regardless of appName, so callers must ensure
	// these services are not app-scoped when using the override.
	appName := strings.TrimSpace(req.AppName)
	if appName == "" {
		appName = rt.appName
	}

	sessionID := req.SessionID
	if sessionID == "" {
		sessionID = uuid.NewString()
	}

	curAgent, err := rt.launcherCfg.AgentLoader.LoadAgent(appName)
	if err != nil {
		return "", nil, fmt.Errorf("adkrun: load agent %q: %w", appName, err)
	}

	// Per-request runner matches the stock ADK REST server pattern. The runner
	// walks the agent tree (parentmap.New) and rebuilds the plugin manager on
	// each call — intentional for isolation between concurrent requests.
	r, err := runner.New(runner.Config{
		AppName:           appName,
		Agent:             curAgent,
		SessionService:    rt.launcherCfg.SessionService,
		MemoryService:     rt.launcherCfg.MemoryService,
		ArtifactService:   rt.launcherCfg.ArtifactService,
		PluginConfig:      rt.launcherCfg.PluginConfig,
		AutoCreateSession: true,
	})
	if err != nil {
		return "", nil, fmt.Errorf("adkrun: create runner: %w", err)
	}

	streamingMode := agent.StreamingModeNone
	if req.Streaming {
		streamingMode = agent.StreamingModeSSE
	}
	runCfg := agent.RunConfig{
		StreamingMode:             streamingMode,
		SaveInputBlobsAsArtifacts: req.SaveInputBlobsAsArtifacts,
	}

	var opts []runner.RunOption
	if req.StateDelta != nil {
		opts = append(opts, runner.WithStateDelta(req.StateDelta))
	}

	msg := req.NewMessage
	msg.Parts = slices.Clone(req.NewMessage.Parts)
	return sessionID, r.Run(ctx, req.UserID, sessionID, &msg, runCfg, opts...), nil
}

// RunUserMessage runs the agent with a user text prompt and drains all events.
//
// sessionID is the ADK session to continue; when empty a new UUID is generated and returned.
// The returned sessionID should be persisted (e.g. in cron.context_id) for subsequent runs.
func (rt *Runtime) RunUserMessage(ctx context.Context, userID, sessionID, prompt string) (string, error) {
	if strings.TrimSpace(prompt) == "" {
		return "", fmt.Errorf("adkrun: prompt is required")
	}
	sessionID, events, err := rt.RunSSE(ctx, RunRequest{
		UserID:     userID,
		SessionID:  sessionID,
		NewMessage: UserTextMessage(prompt),
	})
	if err != nil {
		return "", err
	}
	for _, err := range events {
		if err != nil {
			return "", fmt.Errorf("adkrun: run agent: %w", err)
		}
	}
	return sessionID, nil
}
