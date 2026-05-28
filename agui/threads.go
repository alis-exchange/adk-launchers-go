package agui

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/types"
	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/encoding/sse"
	"go.alis.build/iam/v3"
	alismux "go.alis.build/mux"
	"google.golang.org/adk/session"
)

// threadMessagesHandler returns a handler that loads an ADK session and returns
// its conversation history as AG-UI messages. The response format is determined
// by the Accept header: SSE wraps messages in a run lifecycle; JSON returns a
// messages array with optional pagination cursor.
//
// Identity is extracted from the request context via [iam.FromContext]; the
// caller (SetupHostRoutes) is responsible for running authentication middleware.
func (l *aguiLauncher) threadMessagesHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		identity, identityErr := iam.FromContext(ctx)
		if identityErr != nil || identity == nil {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		userID := identity.ID

		if l.sessionService == nil {
			http.Error(w, "session service not configured", http.StatusServiceUnavailable)
			return
		}

		threadID := r.PathValue("threadId")
		if threadID == "" {
			http.Error(w, "missing thread ID", http.StatusBadRequest)
			return
		}

		var afterTime time.Time
		if afterStr := r.URL.Query().Get("after"); afterStr != "" {
			parsed, parseErr := time.Parse(time.RFC3339Nano, afterStr)
			if parseErr != nil {
				http.Error(w, "invalid 'after' parameter: expected RFC 3339 timestamp", http.StatusBadRequest)
				return
			}
			afterTime = parsed
		}

		var limit int
		if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
			n, parseErr := strconv.Atoi(limitStr)
			if parseErr != nil || n < 0 {
				http.Error(w, "invalid 'limit' parameter: expected non-negative integer", http.StatusBadRequest)
				return
			}
			limit = n
		}

		getReq := &session.GetRequest{
			AppName:   l.config.appName,
			UserID:    userID,
			SessionID: threadID,
		}
		if !afterTime.IsZero() {
			getReq.After = afterTime
		}
		getResp, getErr := l.sessionService.Get(ctx, getReq)
		if getErr != nil {
			log.Printf("agui: thread messages: session.Get failed for %s: %v", threadID, getErr)
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}

		var opts []ConvertOption
		if l.config.genAIPartConverter != nil {
			opts = append(opts, WithPartConverter(l.config.genAIPartConverter))
		}
		if !afterTime.IsZero() {
			opts = append(opts, WithConvertAfter(afterTime))
		}
		if limit > 0 {
			opts = append(opts, WithConvertLimit(limit))
		}

		messages, convertErr := ConvertSessionToMessages(ctx, getResp.Session, opts...)
		if convertErr != nil {
			log.Printf("agui: thread messages: convert failed for %s: %v", threadID, convertErr)
			http.Error(w, "failed to convert session events", http.StatusInternalServerError)
			return
		}

		if acceptsSSE(r) {
			serveThreadMessagesSSE(w, r, threadID, getResp.Session, messages)
		} else {
			serveThreadMessagesJSON(w, getResp.Session, messages, limit)
		}
	}
}

// threadMessagesResponse is the JSON envelope for the thread messages endpoint.
type threadMessagesResponse struct {
	Messages   []types.Message `json:"messages"`
	NextCursor string          `json:"nextCursor,omitempty"`
}

func serveThreadMessagesJSON(w http.ResponseWriter, sess session.Session, messages []types.Message, requestedLimit int) {
	var cursor string
	if requestedLimit > 0 && len(messages) >= requestedLimit && !sess.LastUpdateTime().IsZero() {
		cursor = sess.LastUpdateTime().Format(time.RFC3339Nano)
	}

	resp := threadMessagesResponse{
		Messages:   messages,
		NextCursor: cursor,
	}
	if resp.Messages == nil {
		resp.Messages = []types.Message{}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("agui: thread messages: failed to encode response: %v", err)
	}
}

func serveThreadMessagesSSE(w http.ResponseWriter, r *http.Request, threadID string, sess session.Session, messages []types.Message) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	rc := http.NewResponseController(w)
	if err := rc.Flush(); err != nil {
		http.Error(w, "failed to flush SSE headers", http.StatusInternalServerError)
		return
	}

	ctx := r.Context()
	writer := sse.NewSSEWriter()
	runID := events.GenerateRunID()

	_ = writer.WriteEvent(ctx, w, events.NewRunStartedEvent(threadID, runID))
	_ = writer.WriteEvent(ctx, w, events.NewMessagesSnapshotEvent(messages))

	stateSnapshot := buildStateSnapshot(sess, nil)
	if len(stateSnapshot) > 0 {
		_ = writer.WriteEvent(ctx, w, events.NewStateSnapshotEvent(stateSnapshot))
	}

	_ = writer.WriteEvent(ctx, w, events.NewRunFinishedEvent(threadID, runID))
}

// acceptsSSE reports whether the request's Accept header includes text/event-stream.
func acceptsSSE(r *http.Request) bool {
	for _, v := range r.Header.Values("Accept") {
		if strings.Contains(v, "text/event-stream") {
			return true
		}
	}
	return false
}

// wrapCORS wraps handler with CORS middleware and registers an OPTIONS preflight
// handler for the given path when CORS is configured. Returns the handler unchanged
// when CORS is not enabled.
func (l *aguiLauncher) wrapCORS(path string, handler http.Handler) http.Handler {
	if l.config.cors == nil {
		return handler
	}
	alismux.HandleHTTP("OPTIONS "+path, l.corsOptionsHandler())
	return l.corsMiddleware(handler)
}

// corsOptionsHandler returns a handler for CORS preflight requests.
func (l *aguiLauncher) corsOptionsHandler() http.Handler {
	corsCfg := l.config.cors

	allowedHeaders := "Content-Type, Authorization"
	if len(corsCfg.AllowedHeaders) > 0 {
		allowedHeaders = strings.Join(corsCfg.AllowedHeaders, ", ")
	}

	isAllowedOrigin := func(origin string) bool {
		for _, allowed := range corsCfg.AllowedOrigins {
			if allowed == "*" || allowed == origin {
				return true
			}
		}
		return false
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && isAllowedOrigin(origin) {
			if corsCfg.AllowCredentials {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
			} else if len(corsCfg.AllowedOrigins) == 1 && corsCfg.AllowedOrigins[0] == "*" {
				w.Header().Set("Access-Control-Allow-Origin", "*")
			} else {
				w.Header().Set("Access-Control-Allow-Origin", origin)
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", allowedHeaders)
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
