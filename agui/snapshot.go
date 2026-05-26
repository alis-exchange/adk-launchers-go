package agui

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/types"
	"google.golang.org/adk/session"
)

// isInternalStateKey reports session state keys managed by the launcher and omitted
// from client-visible StateSnapshot payloads.
func isInternalStateKey(key string) bool {
	return key == pendingInterruptsStateKey || strings.HasPrefix(key, "_agui_")
}

// loadSessionForSnapshot loads an existing ADK session for snapshot emission.
// Returns (nil, false, nil) when the session does not exist or session service
// is unset. Get errors are logged but treated as "session missing" because ADK
// has no sentinel not-found error (see loadPendingInterrupts for rationale).
func (l *aguiLauncher) loadSessionForSnapshot(ctx context.Context, userID, sessionID string) (session.Session, bool, error) {
	sess, err := l.getSession(ctx, userID, sessionID)
	if err != nil {
		log.Printf("agui: loadSessionForSnapshot: session.Get failed (treating as missing): %v", err)
		return nil, false, nil
	}
	if sess == nil {
		return nil, false, nil
	}
	return sess, true, nil
}

// ensureSessionForSnapshot returns an existing session or creates one so a run-start
// StateSnapshot can be emitted before runner.Run (AutoCreateSession otherwise runs
// only inside the runner).
func (l *aguiLauncher) ensureSessionForSnapshot(ctx context.Context, userID, sessionID string, initialState map[string]any) (session.Session, error) {
	sess, ok, _ := l.loadSessionForSnapshot(ctx, userID, sessionID)
	if ok {
		return sess, nil
	}
	if l.sessionService == nil {
		return nil, nil
	}
	createResp, err := l.sessionService.Create(ctx, &session.CreateRequest{
		AppName:   l.config.appName,
		UserID:    userID,
		SessionID: sessionID,
		State:     initialState,
	})
	if err != nil {
		return nil, fmt.Errorf("create session for snapshot: %w", err)
	}
	return createResp.Session, nil
}

// buildStateSnapshot merges persisted session state with optional request state from
// RunAgentInput, omitting launcher-internal keys.
func buildStateSnapshot(sess session.Session, reqState map[string]any) map[string]any {
	out := make(map[string]any)
	if sess != nil {
		for key, val := range sess.State().All() {
			if !isInternalStateKey(key) {
				out[key] = val
			}
		}
	}
	for key, val := range reqState {
		if !isInternalStateKey(key) {
			out[key] = val
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// buildMessagesSnapshot converts ADK session history to AG-UI messages for
// MESSAGES_SNAPSHOT events. Today only interrupt boundaries emit them; see
// TODO(messages-snapshot-run-start) in agui.go for possible run-start emission.
func (l *aguiLauncher) buildMessagesSnapshot(ctx context.Context, sess session.Session) ([]types.Message, error) {
	if sess == nil {
		return nil, nil
	}
	var opts []ConvertOption
	if l.config.genAIPartConverter != nil {
		opts = append(opts, WithPartConverter(l.config.genAIPartConverter))
	}
	return ConvertSessionToMessages(ctx, sess, opts...)
}

// emitStateSnapshotIfNonEmpty emits a StateSnapshotEvent when snapshot has keys.
func emitStateSnapshotIfNonEmpty(e *emitter, snapshot map[string]any) {
	if len(snapshot) > 0 {
		e.emit(events.NewStateSnapshotEvent(snapshot))
	}
}

// emitMessagesSnapshotIfNonEmpty emits a MessagesSnapshotEvent when messages exist.
func emitMessagesSnapshotIfNonEmpty(e *emitter, messages []types.Message) {
	if len(messages) > 0 {
		e.emit(events.NewMessagesSnapshotEvent(messages))
	}
}
