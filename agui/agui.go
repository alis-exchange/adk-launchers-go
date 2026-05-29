package agui

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/types"
	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/encoding/sse"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"go.alis.build/adk/launchers/internal/adkrun"
	"go.alis.build/adk/launchers/internal/launcherutils"
	launchersweb "go.alis.build/adk/launchers/web"
	historyservice "go.alis.build/agui/history/service"
	"go.alis.build/iam/v3"
	alismux "go.alis.build/mux"
	"google.golang.org/adk/cmd/launcher"
	weblauncher "google.golang.org/adk/cmd/launcher/web"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

var (
	_ launchersweb.HostRouteSetup = (*aguiLauncher)(nil)
	_ weblauncher.Sublauncher     = &aguiLauncher{}
)

// CORSConfig controls Cross-Origin Resource Sharing headers on the AG-UI endpoint.
// Browser-based frontends (CopilotKit, custom Vue/React apps) require CORS
// because the frontend origin differs from the agent server origin.
type CORSConfig struct {
	// AllowedOrigins is the list of origins permitted to access the endpoint.
	// Use ["*"] to allow all origins (development only — not recommended for production).
	AllowedOrigins []string

	// AllowedHeaders lists HTTP headers the client may send.
	// Defaults to ["Content-Type", "Authorization"] if empty.
	AllowedHeaders []string

	// ExposeHeaders lists response headers the browser may read.
	// Defaults to none if empty.
	ExposeHeaders []string

	// AllowCredentials indicates whether the response to the request can include credentials.
	AllowCredentials bool
}

// GenAIPartConverter converts a genai.Part from an ADK session event into
// zero or more AG-UI events, allowing consumers to intercept, transform, or
// suppress specific parts before the default event mapping runs.
//
// Return a non-nil slice (including empty) to indicate the part was handled;
// the default processing is skipped and the returned events are emitted.
// Return (nil, nil) to fall through to the default handler for that part.
//
// This mirrors the adka2a.GenAIPartConverter pattern: nil returns mean
// "not handled, use default", while non-nil returns (even an empty slice)
// mean "handled, skip default".
type GenAIPartConverter func(ctx context.Context, adkEvent *session.Event, part *genai.Part) ([]events.Event, error)

// AGUIConfig holds configuration for the AG-UI sublauncher. It is populated
// by [NewLauncher] and functional options; fields are read when routes are
// registered in [SetupHostRoutes] and on each /run_sse request.
type AGUIConfig struct {
	// appName is the ADK runner AppName and used to distinguish the root agent
	// from sub-agent authors when emitting StepStarted/StepFinished events.
	appName string
	// pathPrefix is the HTTP path prefix for /run_sse and /capabilities (default "/agui").
	pathPrefix string
	// interceptors run Before/OnEmit/After hooks around each /run_sse request.
	interceptors []CallInterceptor
	// cors, when non-nil, enables CORS middleware on AG-UI routes.
	cors *CORSConfig
	// capabilities, when non-nil, enables GET /capabilities JSON discovery.
	capabilities *Capabilities
	// genAIPartConverter optionally overrides mapping of genai.Part to AG-UI events.
	genAIPartConverter GenAIPartConverter
	// threadService, when non-nil, enables thread metadata tracking and the
	// GET /threads listing endpoint.
	threadService *historyservice.ThreadService
}

// Option configures an [AGUIConfig] passed to [NewLauncher].
type Option func(*AGUIConfig)

// WithInterceptor adds a CallInterceptor to the AG-UI launcher.
// Interceptors are executed in the order they are added.
func WithInterceptor(interceptor CallInterceptor) Option {
	return func(c *AGUIConfig) {
		c.interceptors = append(c.interceptors, interceptor)
	}
}

// WithCORS enables CORS headers on the AG-UI endpoint. This is required when
// browser-based frontends (CopilotKit, custom SPAs) call the endpoint from a
// different origin than the agent server. The middleware handles OPTIONS
// preflight requests and sets Access-Control-* headers on all responses.
func WithCORS(cors CORSConfig) Option {
	return func(c *AGUIConfig) {
		c.cors = &cors
	}
}

// WithCapabilities declares the agent's capabilities so clients can discover
// supported features via GET /capabilities and adapt their UI accordingly.
//
// It calls [MergeInterruptCapabilities] on the provided value so interrupt
// resume support is advertised by default (see that function for rationale).
// Pass humanInTheLoop.interrupts or approveWithEdits as false to opt out.
func WithCapabilities(caps Capabilities) Option {
	return func(c *AGUIConfig) {
		MergeInterruptCapabilities(&caps)
		c.capabilities = &caps
	}
}

// WithGenAIPartConverter registers a callback that intercepts genai.Part values
// from ADK session events before the default AG-UI event mapping runs.
//
// When the converter returns a non-nil slice, those events are emitted and the
// default handling for that part is skipped. When it returns (nil, nil), the
// default mapping (text streaming, tool calls, etc.) proceeds normally.
//
// This is the AG-UI equivalent of [adka2a.ExecutorConfig.GenAIPartConverter]:
// it lets consumers customize how specific parts (e.g. generative UI payloads,
// extension-specific function calls) are represented on the SSE stream without
// modifying the launcher itself.
func WithGenAIPartConverter(converter GenAIPartConverter) Option {
	return func(c *AGUIConfig) {
		c.genAIPartConverter = converter
	}
}

// WithThreadService enables thread metadata tracking backed by the given
// [historyservice.ThreadService]. When configured:
//
//   - GET {path_prefix}/threads is registered, returning thread listings with
//     unread/pinned state for the authenticated user.
//   - Each /run_sse request automatically creates or updates the thread's
//     metadata (run count, last activity time, display name on first run).
func WithThreadService(svc *historyservice.ThreadService) Option {
	return func(c *AGUIConfig) {
		c.threadService = svc
	}
}

// aguiLauncher implements [weblauncher.Sublauncher] for the AG-UI protocol.
// A single instance serves one root agent; see [SetupHostRoutes] for routing.
type aguiLauncher struct {
	flags          *flag.FlagSet
	config         *AGUIConfig
	runtime        *adkrun.Runtime
	sessionService session.Service // used for pending-interrupt persistence across runs

	hostSetupOnce sync.Once
	hostSetupErr  error
}

// NewLauncher creates a new AG-UI sublauncher. Register it with [web.NewLauncher]
// and activate it with the "agui" CLI keyword. The appName argument becomes the
// ADK runner's AppName and must match the root agent name for step event filtering.
func NewLauncher(appName string, opts ...Option) weblauncher.Sublauncher {
	config := &AGUIConfig{
		appName: appName,
	}
	for _, opt := range opts {
		opt(config)
	}

	fs := flag.NewFlagSet("agui", flag.ContinueOnError)
	fs.StringVar(&config.pathPrefix, "path_prefix", "/agui", "AG-UI API path prefix. Default is '/agui'.")

	return &aguiLauncher{
		flags:  fs,
		config: config,
	}
}

// Keyword returns the sublauncher keyword used for CLI dispatch.
func (l *aguiLauncher) Keyword() string {
	return "agui"
}

// Parse parses AG-UI-specific command-line flags from args and normalizes
// the path prefix to ensure it starts with "/" and has no trailing slash.
func (l *aguiLauncher) Parse(args []string) ([]string, error) {
	err := l.flags.Parse(args)
	if err != nil || !l.flags.Parsed() {
		return nil, fmt.Errorf("failed to parse agui flags: %v", err)
	}
	p := l.config.pathPrefix
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	l.config.pathPrefix = strings.TrimSuffix(p, "/")
	return l.flags.Args(), nil
}

// CommandLineSyntax returns a formatted string describing all available flags.
func (l *aguiLauncher) CommandLineSyntax() string {
	return launcherutils.FormatFlagUsage(l.flags)
}

// SimpleDescription returns a human-readable description of the sublauncher.
func (l *aguiLauncher) SimpleDescription() string {
	return "starts AG-UI protocol server for CopilotKit and other AG-UI compatible clients"
}

// SetupSubrouters is a no-op; all routes are registered on the host mux via
// [SetupHostRoutes]. The gorilla subrouter is not used.
func (l *aguiLauncher) SetupSubrouters(_ *mux.Router, _ *launcher.Config) error {
	return nil
}

// SetupHostRoutes registers all AG-UI routes on the process-wide host mux
// (go.alis.build/mux). It creates the ADK runtime and session service used
// by both /run_sse and thread message history.
//
// Routes registered:
//
//	POST {pathPrefix}/run_sse                        — SSE streaming endpoint (authenticated)
//	GET  {pathPrefix}/threads/{threadId}/messages     — thread message history (authenticated)
//	GET  {pathPrefix}/capabilities                    — capability discovery (public, if configured)
//	OPTIONS for each route above                      — CORS preflight (when WithCORS is set)
func (l *aguiLauncher) SetupHostRoutes(config *launcher.Config) error {
	l.hostSetupOnce.Do(func() {
		l.hostSetupErr = l.mountHostRoutes(config)
	})
	return l.hostSetupErr
}

func (l *aguiLauncher) mountHostRoutes(config *launcher.Config) error {
	rt, err := adkrun.NewRuntime(config, l.config.appName)
	if err != nil {
		return fmt.Errorf("failed to create ADK runtime: %w", err)
	}
	l.runtime = rt
	l.sessionService = config.SessionService

	// Build the CORS middleware once (nil slice when CORS is not configured).
	corsMW := l.buildCORSMiddleware()

	// POST /run_sse — SSE streaming endpoint for agent runs.
	ssePath := l.config.pathPrefix + "/run_sse"
	l.registerCORSPreflight(ssePath, "POST")
	alismux.AuthenticatedPost(ssePath, l.runSSEFunc(), corsMW...)

	// GET /threads/{threadId}/messages — thread message history.
	messagesPath := l.config.pathPrefix + "/threads/{threadId}/messages"
	l.registerCORSPreflight(messagesPath, "GET")
	alismux.AuthenticatedGet(messagesPath, l.threadMessagesFunc(), corsMW...)

	// Thread metadata endpoints (optional, requires WithThreadService).
	if l.config.threadService != nil {
		threadPath := l.config.pathPrefix + "/threads/{threadId}"
		l.registerCORSPreflight(threadPath, "GET, DELETE")
		alismux.AuthenticatedGet(threadPath, l.getThreadFunc(), corsMW...)
		alismux.AuthenticatedDelete(threadPath, l.deleteThreadFunc(), corsMW...)

		listPath := l.config.pathPrefix + "/threads"
		l.registerCORSPreflight(listPath, "GET")
		alismux.AuthenticatedGet(listPath, l.listThreadsFunc(), corsMW...)
	}

	// GET /capabilities — capability discovery (optional).
	if l.config.capabilities != nil {
		capsPath := l.config.pathPrefix + "/capabilities"
		l.registerCORSPreflight(capsPath, "GET")
		alismux.Get(capsPath, l.capabilitiesFunc(), corsMW...)
	}

	return nil
}

// UserMessage prints the AG-UI endpoint URLs to the console on startup.
func (l *aguiLauncher) UserMessage(webURL string, printer func(v ...any)) {
	printer(fmt.Sprintf("       agui:  AG-UI SSE endpoint is available at %s%s/run_sse", webURL, l.config.pathPrefix))
	printer(fmt.Sprintf("       agui:  thread messages at %s%s/threads/{threadId}/messages", webURL, l.config.pathPrefix))
	if l.config.threadService != nil {
		printer(fmt.Sprintf("       agui:  thread detail at %s%s/threads/{threadId}", webURL, l.config.pathPrefix))
		printer(fmt.Sprintf("       agui:  thread listing at %s%s/threads", webURL, l.config.pathPrefix))
	}
	if l.config.capabilities != nil {
		printer(fmt.Sprintf("       agui:  capabilities at %s%s/capabilities", webURL, l.config.pathPrefix))
	}
}

// capabilitiesFunc returns a [alismux.Func] that serves the agent's
// declared capabilities as JSON.
func (l *aguiLauncher) capabilitiesFunc() alismux.Func {
	return func(w http.ResponseWriter, r *http.Request) error {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(l.config.capabilities); err != nil {
			http.Error(w, "failed to encode capabilities", http.StatusInternalServerError)
		}
		return nil
	}
}

// buildCORSMiddleware returns a single [alismux.Middleware] that adds CORS
// response headers on actual (non-preflight) requests. Returns nil when CORS
// is not configured. OPTIONS preflight is handled by dedicated routes
// registered via [registerCORSPreflight].
//
// When AllowCredentials is true, the middleware echoes the request's Origin
// header instead of using "*", because the CORS spec forbids wildcard origins
// with credentialed requests. When AllowCredentials is false and the only
// configured origin is "*", it returns "*" directly.
func (l *aguiLauncher) buildCORSMiddleware() []alismux.Middleware {
	if l.config.cors == nil {
		return nil
	}

	exposeHeaders := ""
	if len(l.config.cors.ExposeHeaders) > 0 {
		exposeHeaders = strings.Join(l.config.cors.ExposeHeaders, ", ")
	}

	mw := func(w http.ResponseWriter, r *http.Request, handler alismux.Func) error {
		if l.setCORSOriginHeaders(w, r) && exposeHeaders != "" {
			w.Header().Set("Access-Control-Expose-Headers", exposeHeaders)
		}
		return handler(w, r)
	}

	return []alismux.Middleware{mw}
}

// setCORSOriginHeaders checks the request origin against the configured
// AllowedOrigins and sets Access-Control-Allow-Origin (and Allow-Credentials
// when applicable). Returns true if the origin was allowed.
func (l *aguiLauncher) setCORSOriginHeaders(w http.ResponseWriter, r *http.Request) bool {
	corsCfg := l.config.cors
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}

	allowed := false
	for _, a := range corsCfg.AllowedOrigins {
		if a == "*" || a == origin {
			allowed = true
			break
		}
	}
	if !allowed {
		return false
	}

	if corsCfg.AllowCredentials {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Credentials", "true")
	} else if len(corsCfg.AllowedOrigins) == 1 && corsCfg.AllowedOrigins[0] == "*" {
		w.Header().Set("Access-Control-Allow-Origin", "*")
	} else {
		w.Header().Set("Access-Control-Allow-Origin", origin)
	}
	return true
}

// convertMultimodalInput converts a typed AG-UI InputContent (image, audio,
// video, document) to a [genai.Part]. The AG-UI spec nests payload under
// source.{type,value,mimeType}; older clients may still send flat Data/URL fields.
func convertMultimodalInput(ic types.InputContent) (*genai.Part, error) {
	if ic.Source != nil {
		switch ic.Source.Type {
		case types.InputContentSourceTypeData:
			dataBytes, err := base64.StdEncoding.DecodeString(ic.Source.Value)
			if err != nil {
				return nil, fmt.Errorf("failed to decode base64 source data: %w", err)
			}
			return &genai.Part{
				InlineData: &genai.Blob{
					Data:     dataBytes,
					MIMEType: ic.Source.MimeType,
				},
			}, nil
		case types.InputContentSourceTypeURL:
			return &genai.Part{
				FileData: &genai.FileData{
					FileURI:  ic.Source.Value,
					MIMEType: ic.Source.MimeType,
				},
			}, nil
		default:
			return nil, fmt.Errorf("unsupported source type %q", ic.Source.Type)
		}
	}

	// Legacy flat fields (source.type = "data")
	if ic.Data != "" {
		dataBytes, err := base64.StdEncoding.DecodeString(ic.Data)
		if err != nil {
			return nil, fmt.Errorf("failed to decode base64 data: %w", err)
		}
		return &genai.Part{
			InlineData: &genai.Blob{
				Data:     dataBytes,
				MIMEType: ic.MimeType,
			},
		}, nil
	}

	// Legacy flat fields (source.type = "url")
	if ic.URL != "" {
		return &genai.Part{
			FileData: &genai.FileData{
				FileURI:  ic.URL,
				MIMEType: ic.MimeType,
			},
		}, nil
	}

	return nil, fmt.Errorf("no data, url, or source available")
}

// extractLastUserMessage returns the latest user turn from an AG-UI message history.
// Clients often send the full transcript; ADK session service already stores
// history keyed by threadId, so only the newest user message is passed to adkrun.RunSSE.
func extractLastUserMessage(messages []types.Message) (*genai.Content, error) {
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		if message.Role != types.RoleUser {
			continue
		}

		if inputContents, ok := message.ContentInputContents(); ok && len(inputContents) > 0 {
			parts := make([]*genai.Part, len(inputContents))
			for j, inputContent := range inputContents {
				switch inputContent.Type {
				case types.InputContentTypeText:
					parts[j] = genai.NewPartFromText(inputContent.Text)
				case types.InputContentTypeBinary:
					dataBytes, err := base64.StdEncoding.DecodeString(inputContent.Data)
					if err != nil {
						return nil, fmt.Errorf("failed to decode base64 binary data: %w", err)
					}
					parts[j] = &genai.Part{
						InlineData: &genai.Blob{
							Data:        dataBytes,
							MIMEType:    inputContent.MimeType,
							DisplayName: inputContent.Filename,
						},
					}
				default:
					part, err := convertMultimodalInput(inputContent)
					if err != nil {
						return nil, fmt.Errorf("unsupported content type %q: %w", inputContent.Type, err)
					}
					parts[j] = part
				}
			}
			return genai.NewContentFromParts(parts, genai.RoleUser), nil
		}

		if contentStr, ok := message.ContentString(); ok && contentStr != "" {
			return genai.NewContentFromText(contentStr, genai.RoleUser), nil
		}

		return nil, fmt.Errorf("unsupported content type: %T", message.Content)
	}

	return nil, fmt.Errorf("no user message found in payload")
}

// runSSEFunc returns the handler for the AG-UI /run_sse endpoint.
//
// The handler has two phases separated by the SSE commitment point:
//   - Pre-SSE: request parsing, interceptors, validation.
//     Errors in this phase return standard HTTP error responses.
//   - Post-SSE: after SSE headers are written and RunStartedEvent is emitted.
//     Errors in this phase are delivered as RunErrorEvent on the SSE stream.
func (l *aguiLauncher) runSSEFunc() alismux.Func {
	return func(w http.ResponseWriter, r *http.Request) error {
		// Pre-SSE phase: errors use http.Error.

		var req types.RunAgentInput
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			log.Printf("agui: error decoding request: %v", err)
			http.Error(w, "Invalid AGUI request payload", http.StatusBadRequest)
			return nil
		}
		defer r.Body.Close()

		state := &streamState{}

		// Use client-provided IDs when available; generate otherwise.
		state.runID = req.RunID
		if state.runID == "" {
			state.runID = events.GenerateRunID()
		}
		state.threadID = req.ThreadID
		if state.threadID == "" {
			state.threadID = uuid.New().String()
		}

		// ADK session ID maps 1:1 with AG-UI threadID for conversation continuity.
		sessionID := state.threadID

		ctx := r.Context()

		// Populate user from mux IAM identity. Interceptors may override.
		callCtx := &CallContext{User: &User{}}
		if identity, identityErr := iam.FromContext(ctx); identityErr == nil && identity != nil {
			callCtx.User.Name = identity.ID
			callCtx.User.Authenticated = true
		}

		// Run Before interceptors, tracking how many succeeded so After
		// only runs for those (prevents calling After for interceptors
		// whose Before never ran or failed).
		var handlerErr error
		var succeeded int
		defer func() {
			for i := succeeded - 1; i >= 0; i-- {
				if afterErr := l.config.interceptors[i].After(ctx, callCtx, handlerErr); afterErr != nil {
					log.Printf("agui: After interceptor error: %v", afterErr)
				}
			}
		}()
		for i, interceptor := range l.config.interceptors {
			var err error
			ctx, err = interceptor.Before(ctx, callCtx, &req, r)
			if err != nil {
				http.Error(w, "Interceptor rejected request: "+err.Error(), http.StatusInternalServerError)
				return nil
			}
			succeeded = i + 1
		}

		// Interceptors may populate callCtx.User; validate it's set.
		if callCtx == nil || callCtx.User == nil || callCtx.User.Name == "" {
			http.Error(w, "userID is required", http.StatusBadRequest)
			return nil
		}
		userID := callCtx.User.Name

		// Upsert thread metadata after interceptors succeed and userID is
		// validated, so rejected requests don't increment run_count.
		// Re-extract identity from the post-interceptor context so it
		// reflects any identity changes made by interceptors.
		if l.config.threadService != nil && state.threadID != "" && userID != "" {
			if iamIdentity, identityErr := iam.FromContext(ctx); identityErr == nil && iamIdentity != nil {
				l.upsertThreadMetadata(ctx, iamIdentity, state.threadID, &req)
			}
		}

		// Resume runs carry FunctionResponses instead of a new user text turn.
		isResumeRun := len(req.Resume) > 0

		// SSE commitment point: after headers flush and RunStarted, protocol errors
		// must be RunError on the stream (not HTTP 4xx), per AG-UI error handling.

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		rc := http.NewResponseController(w)
		if err := rc.Flush(); err != nil {
			handlerErr = fmt.Errorf("failed to flush SSE headers: %w", err)
			http.Error(w, handlerErr.Error(), http.StatusInternalServerError)
			return nil
		}

		e := newEmitter(ctx, w, sse.NewSSEWriter(), l.config.interceptors[:succeeded], callCtx)

		// emitError sends a RunErrorEvent on the SSE stream and marks the run as
		// finalized so that RunFinishedEvent is not also emitted. If a terminal
		// event was already sent, it only records the error (no duplicate emit).
		emitError := func(err error, opts ...events.RunErrorOption) {
			handlerErr = err
			if state.runFinalized {
				return
			}
			finalizeLifecycle(e, state)
			opts = append([]events.RunErrorOption{events.WithRunID(state.runID)}, opts...)
			e.emit(events.NewRunErrorEvent(err.Error(), opts...))
			state.runFinalized = true
		}

		e.emit(events.NewRunStartedEvent(state.threadID, state.runID))
		if e.err != nil {
			handlerErr = fmt.Errorf("failed to write RunStartedEvent: %w", e.err)
			return nil
		}

		// Load interrupts left open on a prior run for this thread (session id).
		pending, err := l.loadPendingInterrupts(ctx, userID, sessionID)
		if err != nil {
			emitError(fmt.Errorf("failed to load pending interrupts: %w", err))
			return nil
		}

		// Enforce AG-UI rules: pending threads require resume; cover all ids; schema/expiry.
		if err := validateResumeAgainstPending(req.Resume, pending, time.Now()); err != nil {
			emitError(err)
			return nil
		}

		var reqState map[string]any
		if stateMap, ok := req.State.(map[string]any); ok {
			reqState = stateMap
		}
		state.userID = userID
		state.runCtx = ctx
		state.reqState = reqState

		// Baseline state before deltas (AG-UI spec). Create session when missing so
		// first-turn runs can still emit a snapshot before adkrun.RunSSE.
		snapSess, snapErr := l.ensureSessionForSnapshot(ctx, userID, sessionID, reqState)
		if snapErr != nil {
			emitError(fmt.Errorf("failed to prepare session for state snapshot: %w", snapErr))
			return nil
		}
		emitStateSnapshotIfNonEmpty(e, buildStateSnapshot(snapSess, reqState))
		// TODO(messages-snapshot-run-start): AG-UI recommends MESSAGES_SNAPSHOT when
		// initializing or resyncing a conversation (see
		// https://docs.ag-ui.com/concepts/messages#complete-snapshots) but only
		// requires it before interrupt RunFinished (see
		// https://docs.ag-ui.com/concepts/interrupts#state-at-the-interrupt-boundary).
		// We emit MessagesSnapshot at interrupt boundaries only (stream.go emitInterrupt).
		// Add run-start emission here if clients need full history on RunStarted without
		// relying on prior interrupt snapshots or TEXT_MESSAGE_* streaming.

		var msg *genai.Content
		if isResumeRun {
			// Map AG-UI resume[] → ADK adk_request_confirmation FunctionResponses.
			msg, err = resumeEntriesToConfirmationContent(req.Resume)
			if err != nil {
				emitError(fmt.Errorf("invalid resume payload: %w", err))
				return nil
			}
		} else {
			msg, err = extractLastUserMessage(req.Messages)
			if err != nil {
				emitError(err)
				return nil
			}
		}

		// Stream ADK events, mapping each to AG-UI protocol events.
		runReq := adkrun.RunRequest{
			UserID:                    userID,
			SessionID:                 sessionID,
			NewMessage:                *msg,
			Streaming:                 true,
			SaveInputBlobsAsArtifacts: false,
		}
		if stateMap, ok := req.State.(map[string]any); ok && len(stateMap) > 0 {
			runReq.StateDelta = stateMap
		}

		_, adkEvents, err := l.runtime.RunSSE(ctx, runReq)
		if err != nil {
			emitError(err)
			return nil
		}

		for ev, err := range adkEvents {
			if err != nil {
				emitError(err)
				break
			}
			if ev == nil {
				continue
			}

			if ev.ErrorMessage != "" {
				var opts []events.RunErrorOption
				if ev.ErrorCode != "" {
					opts = append(opts, events.WithErrorCode(ev.ErrorCode))
				}
				emitError(fmt.Errorf("%s", ev.ErrorMessage), opts...)
				break
			}

			done, err := l.processEvent(e, ev, state)
			if err != nil {
				emitError(err)
				break
			}
			if done {
				break
			}
		}

		// Close any open lifecycle events before finalizing the run.
		finalizeLifecycle(e, state)

		// Emit RunFinishedEvent only if no terminal event (RunError) was already sent.
		if !state.runFinalized {
			e.emit(events.NewRunFinishedEventWithOptions(
				state.threadID,
				state.runID,
				events.WithSuccessOutcome(),
			))
			state.runFinalized = true
		}

		// Persist or clear pending interrupts for the next run on this thread.
		// The terminal SSE event has already been emitted, so failures here are
		// logged rather than sent as RunError (which would duplicate the terminal).
		switch {
		case len(state.emittedInterrupts) > 0:
			if err := l.persistPendingInterrupts(ctx, userID, sessionID, state.emittedInterrupts); err != nil {
				log.Printf("agui: failed to persist pending interrupts: %v", err)
				handlerErr = err
			}
		case handlerErr == nil:
			if err := l.clearPendingInterrupts(ctx, userID, sessionID); err != nil {
				log.Printf("agui: failed to clear pending interrupts: %v", err)
				handlerErr = err
			}
		}
		return nil
	}
}
