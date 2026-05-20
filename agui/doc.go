// Package agui implements an ADK web sublauncher for the AG-UI protocol. It bridges
// ADK agent execution to AG-UI Server-Sent Events (SSE), enabling CopilotKit and
// other AG-UI-compatible frontends to stream agent responses in real time.
//
// # Role in the ADK web launcher
//
// The ADK web launcher composes one or more sublaunchers, each activated by a CLI
// keyword. This package registers the keyword "agui" and mounts AG-UI HTTP routes on
// the shared gorilla/mux router started by google.golang.org/adk/cmd/launcher/web.
//
// # Agent binding
//
// NewLauncher requires an app name string used as the ADK runner AppName and to
// distinguish the root agent from sub-agent step events on the SSE stream.
//
// At setup time, SetupSubrouters creates a single runner.Runner bound to
// config.AgentLoader.RootAgent(). One aguiLauncher instance therefore serves exactly
// one agent. To expose multiple agents, deploy one launcher per agent (the same
// pattern as the A2A sublauncher) or extend routing to load agents per request.
//
// Conversation continuity uses a 1:1 mapping between AG-UI threadId and the ADK
// session ID. The launcher enables AutoCreateSession on the runner so the first
// request for a thread creates the session automatically.
//
// # HTTP routes
//
// Routes are mounted under a configurable path prefix (default "/agui"):
//
//	{path_prefix}/run_sse       POST  — SSE streaming endpoint for agent runs
//	{path_prefix}/capabilities  GET   — capability discovery (only if configured)
//
// When CORS is enabled via WithCORS, OPTIONS preflight is handled for the registered
// routes in addition to POST and GET.
//
// The /run_sse handler accepts a JSON [types.RunAgentInput] body. It extracts the
// latest user message from the request (full history may be sent, but ADK session
// service maintains authoritative history via threadId). Optional request state is
// forwarded into the ADK session via runner.WithStateDelta.
//
// Errors before SSE headers are committed return standard HTTP status codes. After
// the stream starts (RunStartedEvent emitted), errors are delivered as RunErrorEvent
// on the SSE connection.
//
// # Configuration
//
// Options apply when calling NewLauncher:
//
//   - WithInterceptor — add [CallInterceptor] hooks (auth, logging, event mutation).
//   - WithCORS — enable CORS middleware for browser-based frontends.
//   - WithCapabilities — expose GET /capabilities for client discovery.
//   - WithGenAIPartConverter — customize how [genai.Part] values map to AG-UI events.
//
// CLI flags (after the "agui" keyword on the web command line):
//
//   - -path_prefix — URL prefix for AG-UI routes (default "/agui").
//
// The app name is set only via NewLauncher's first argument; there is no CLI flag
// for it. Path prefix can be overridden at runtime via -path_prefix even when
// defaults were set at construction.
//
// # Usage
//
// Programmatic defaults:
//
//	streaming := true
//	web.NewLauncher(
//	    agui.NewLauncher(
//	        "my-agent",
//	        agui.WithCORS(agui.CORSConfig{
//	            AllowedOrigins: []string{"http://localhost:3000"},
//	        }),
//	        agui.WithCapabilities(agui.Capabilities{
//	            Transport: &agui.TransportCapabilities{Streaming: &streaming},
//	        }),
//	    ),
//	)
//
// CLI example:
//
//	adk web --port 8080 agui -path_prefix=/api/agui
//
// On startup, UserMessage prints the full /run_sse URL (for example
// http://localhost:8080/agui/run_sse).
//
// # Call interceptors
//
// [CallInterceptor] runs around each /run_sse request:
//
//   - Before — validate or enrich the request; return an error to reject before SSE starts.
//   - OnEmit — observe or modify each AG-UI event before it is written to the wire.
//   - After — cleanup; runs in reverse order for interceptors whose Before succeeded.
//
// Interceptors should populate [CallContext.User] in Before; the handler requires a
// non-empty user name (defaults to "agui-user" if none is set). Embed
// [PassthroughInterceptor] to implement only the hooks you need.
//
// # Event mapping and part conversion
//
// During a run, ADK session events are translated into AG-UI protocol events on the
// SSE stream: text streaming, tool calls, reasoning, sub-agent steps, interrupts
// (human-in-the-loop confirmations), and run lifecycle (RunStarted, RunFinished,
// RunError). Partial streaming deltas are folded into final messages before emission.
//
// [GenAIPartConverter] mirrors the adka2a pattern: return a non-nil slice (including
// empty) to handle a part and skip default mapping; return (nil, nil) to use the
// default handler. The same converter can be passed to [ConvertSessionToMessages] via
// [WithPartConverter] for consistent history replay.
//
// # Session history conversion
//
// [ConvertSessionToMessages] converts stored ADK session events into AG-UI
// [types.Message] values for MESSAGES_SNAPSHOT payloads or direct JSON responses.
// It skips partial (in-flight) events and supports cursor pagination via
// [WithConvertAfter] and [WithConvertLimit]. This function does not require the
// sublauncher to be running; use it from custom HTTP handlers or tooling that need
// AG-UI-shaped history without a live SSE run.
//
// # Capabilities
//
// When [WithCapabilities] is set, GET /capabilities returns the declared
// [Capabilities] document as JSON. Only fields the agent actually supports should be
// populated; omitted fields mean the capability is undeclared ("absent = unknown").
// Clients use this endpoint to adapt UI features (tools, multimodal input, streaming,
// human-in-the-loop, and so on).
//
// # CORS
//
// Browser frontends (CopilotKit, Vue/React SPAs) typically call the agent server from
// a different origin. WithCORS wraps handlers with Access-Control-* headers and
// handles OPTIONS preflight. When AllowCredentials is true, the middleware echoes the
// request Origin instead of using "*", per the CORS specification.
//
// # Protocol dependencies
//
// Streaming and event types come from the AG-UI community Go SDK
// (github.com/ag-ui-protocol/ag-ui/sdks/community/go). See https://docs.ag-ui.com
// for the protocol specification.
//
// # Limitations
//
// This package does not implement multi-agent path routing, AG-UI interrupt resume
// (emit is supported; resume awaits SDK RunAgentInput.Resume), or an initial
// StateSnapshotEvent at run start. It does not register ADK tools or plugins; it only
// mounts HTTP endpoints and maps ADK execution to AG-UI SSE.
package agui
