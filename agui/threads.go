package agui

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/types"
	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/encoding/sse"
	pb "go.alis.build/common/alis/agui/history/v1"
	historyservice "go.alis.build/agui/history/service"
	"go.alis.build/iam/v3"
	alismux "go.alis.build/mux"
	"google.golang.org/adk/session"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	grpcstatus "google.golang.org/grpc/status"
)

// threadMessagesFunc returns a handler that loads an ADK session and returns
// its conversation history as AG-UI messages. The response format is determined
// by the Accept header: SSE wraps messages in a run lifecycle; JSON returns a
// messages array with optional pagination cursor.
//
// Identity is extracted from the request context via [iam.FromContext]; the
// caller (SetupHostRoutes) is responsible for running authentication middleware.
func (l *aguiLauncher) threadMessagesFunc() alismux.Func {
	return func(w http.ResponseWriter, r *http.Request) error {
		ctx := r.Context()

		identity, identityErr := iam.FromContext(ctx)
		if identityErr != nil || identity == nil {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return nil
		}
		userID := identity.ID

		if l.sessionService == nil {
			http.Error(w, "session service not configured", http.StatusServiceUnavailable)
			return nil
		}

		threadID := r.PathValue("threadId")
		if threadID == "" {
			http.Error(w, "missing thread ID", http.StatusBadRequest)
			return nil
		}

		var afterTime time.Time
		if afterStr := r.URL.Query().Get("after"); afterStr != "" {
			parsed, parseErr := time.Parse(time.RFC3339Nano, afterStr)
			if parseErr != nil {
				http.Error(w, "invalid 'after' parameter: expected RFC 3339 timestamp", http.StatusBadRequest)
				return nil
			}
			afterTime = parsed
		}

		var limit int
		if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
			n, parseErr := strconv.Atoi(limitStr)
			if parseErr != nil || n < 0 {
				http.Error(w, "invalid 'limit' parameter: expected non-negative integer", http.StatusBadRequest)
				return nil
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
			return nil
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
			return nil
		}

		if acceptsSSE(r) {
			serveThreadMessagesSSE(w, r, threadID, getResp.Session, messages)
		} else {
			serveThreadMessagesJSON(w, getResp.Session, messages, limit)
		}
		return nil
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

// registerCORSPreflight registers an OPTIONS preflight handler for path with
// the given allowed methods. No-op when CORS is not configured.
func (l *aguiLauncher) registerCORSPreflight(path string, methods string) {
	if l.config.cors == nil {
		return
	}
	corsCfg := l.config.cors

	allowedHeaders := "Content-Type, Authorization"
	if len(corsCfg.AllowedHeaders) > 0 {
		allowedHeaders = strings.Join(corsCfg.AllowedHeaders, ", ")
	}

	allowMethods := methods + ", OPTIONS"

	alismux.Options(path, func(w http.ResponseWriter, r *http.Request) error {
		if l.setCORSOriginHeaders(w, r) {
			w.Header().Set("Access-Control-Allow-Methods", allowMethods)
			w.Header().Set("Access-Control-Allow-Headers", allowedHeaders)
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
}

// getThreadFunc returns a [alismux.Func] that fetches a single thread's metadata
// from the configured ThreadService.
func (l *aguiLauncher) getThreadFunc() alismux.Func {
	return func(w http.ResponseWriter, r *http.Request) error {
		ctx := r.Context()

		identity, identityErr := iam.FromContext(ctx)
		if identityErr != nil || identity == nil {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return nil
		}

		threadID := r.PathValue("threadId")
		if threadID == "" {
			http.Error(w, "missing thread ID", http.StatusBadRequest)
			return nil
		}

		svcCtx := injectGrpcMetadata(ctx, identity, pb.ThreadService_GetThread_FullMethodName)
		thread, getErr := l.config.threadService.GetThread(svcCtx, &pb.GetThreadRequest{
			Name: "threads/" + threadID,
		})
		if getErr != nil {
			log.Printf("agui: get thread failed for %s: %v", threadID, getErr)
			grpcToHTTP(w, getErr, "failed to get thread", http.StatusInternalServerError)
			return nil
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(thread); err != nil {
			log.Printf("agui: get thread: failed to encode response: %v", err)
		}
		return nil
	}
}

// deleteThreadFunc returns a [alismux.Func] that deletes a thread via the configured
// ThreadService. Requires roles/thread.owner on the thread's IAM policy.
func (l *aguiLauncher) deleteThreadFunc() alismux.Func {
	return func(w http.ResponseWriter, r *http.Request) error {
		ctx := r.Context()

		identity, identityErr := iam.FromContext(ctx)
		if identityErr != nil || identity == nil {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return nil
		}

		threadID := r.PathValue("threadId")
		if threadID == "" {
			http.Error(w, "missing thread ID", http.StatusBadRequest)
			return nil
		}

		svcCtx := injectGrpcMetadata(ctx, identity, pb.ThreadService_DeleteThread_FullMethodName)
		if _, deleteErr := l.config.threadService.DeleteThread(svcCtx, &pb.DeleteThreadRequest{
			Name: "threads/" + threadID,
		}); deleteErr != nil {
			log.Printf("agui: delete thread failed for %s: %v", threadID, deleteErr)
			grpcToHTTP(w, deleteErr, "failed to delete thread", http.StatusInternalServerError)
			return nil
		}

		w.WriteHeader(http.StatusNoContent)
		return nil
	}
}

// listThreadsFunc returns a [alismux.Func] that lists threads with per-user metadata
// (unread, pinned) from the configured ThreadService.
func (l *aguiLauncher) listThreadsFunc() alismux.Func {
	return func(w http.ResponseWriter, r *http.Request) error {
		ctx := r.Context()

		identity, identityErr := iam.FromContext(ctx)
		if identityErr != nil || identity == nil {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return nil
		}

		agentID := r.URL.Query().Get("agentId")
		var pageSize int32 = 100
		if ps := r.URL.Query().Get("pageSize"); ps != "" {
			n, parseErr := strconv.Atoi(ps)
			if parseErr != nil || n < 0 || n > math.MaxInt32 {
				http.Error(w, "invalid 'pageSize' parameter", http.StatusBadRequest)
				return nil
			}
			pageSize = int32(n)
		}
		pageToken := r.URL.Query().Get("pageToken")

		svcCtx := injectGrpcMetadata(ctx, identity, pb.ThreadService_ListThreads_FullMethodName)
		resp, listErr := l.config.threadService.ListThreads(svcCtx, &pb.ListThreadsRequest{
			AgentId:   agentID,
			PageSize:  pageSize,
			PageToken: pageToken,
		})
		if listErr != nil {
			log.Printf("agui: list threads failed: %v", listErr)
			grpcToHTTP(w, listErr, "failed to list threads", http.StatusInternalServerError)
			return nil
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			log.Printf("agui: list threads: failed to encode response: %v", err)
		}
		return nil
	}
}

// upsertThreadMetadata creates or updates thread metadata via the ThreadService.
// Best-effort: failures are logged, not returned as errors.
func (l *aguiLauncher) upsertThreadMetadata(ctx context.Context, identity *iam.Identity, threadID string, req *types.RunAgentInput) {
	var userText string
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role != types.RoleUser {
			continue
		}
		if s, ok := req.Messages[i].ContentString(); ok && s != "" {
			userText = s
			break
		}
	}

	// GetThread_FullMethodName is used because CreateOrUpdateThread is not a
	// proto-defined RPC and has no dedicated constant. The method name is only
	// used for identity extraction, not authorization.
	svcCtx := injectGrpcMetadata(ctx, identity, pb.ThreadService_GetThread_FullMethodName)
	if err := l.config.threadService.CreateOrUpdateThread(svcCtx, &historyservice.CreateOrUpdateThreadRequest{
		ThreadID:         threadID,
		AgentID:          l.config.appName,
		AgentDisplayName: l.config.appName,
		UserMessageText:  userText,
	}); err != nil {
		log.Printf("agui: thread metadata upsert failed for %s: %v", threadID, err)
	}
}

// injectGrpcMetadata creates a context with gRPC incoming metadata so the
// ThreadService's IAM authorizer can find the caller identity. Required
// because in-process calls bypass the gRPC transport layer.
func injectGrpcMetadata(ctx context.Context, identity *iam.Identity, method string) context.Context {
	if identity == nil {
		return ctx
	}
	md := metadata.MD{
		"x-alis-identity": {string(identity.Marshal())},
	}
	ctx = grpc.NewContextWithServerTransportStream(ctx, &grpcMethodStream{
		method: fmt.Sprintf("/%s/%s", pb.ThreadService_ServiceDesc.ServiceName, extractMethodName(method)),
	})
	return metadata.NewIncomingContext(ctx, md)
}

func extractMethodName(fullMethod string) string {
	parts := strings.Split(fullMethod, "/")
	return parts[len(parts)-1]
}

type grpcMethodStream struct {
	method string
}

func (s *grpcMethodStream) Method() string                 { return s.method }
func (s *grpcMethodStream) SetHeader(_ metadata.MD) error  { return nil }
func (s *grpcMethodStream) SendHeader(_ metadata.MD) error { return nil }
func (s *grpcMethodStream) SetTrailer(_ metadata.MD) error { return nil }

func grpcToHTTP(w http.ResponseWriter, err error, fallbackMsg string, fallbackCode int) {
	st, ok := grpcstatus.FromError(err)
	if !ok {
		http.Error(w, fallbackMsg, fallbackCode)
		return
	}
	switch st.Code() {
	case codes.NotFound:
		http.Error(w, st.Message(), http.StatusNotFound)
	case codes.PermissionDenied:
		http.Error(w, st.Message(), http.StatusForbidden)
	case codes.InvalidArgument:
		http.Error(w, st.Message(), http.StatusBadRequest)
	case codes.Unauthenticated:
		http.Error(w, st.Message(), http.StatusUnauthorized)
	default:
		http.Error(w, fallbackMsg, fallbackCode)
	}
}
