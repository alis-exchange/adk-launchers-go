package agui

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"reflect"
	"time"

	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/types"
	"google.golang.org/adk/session"
)

// pendingInterruptsStateKey is the ADK session state key used to persist open
// AG-UI interrupts between runs on the same thread. Clients must send
// RunAgentInput.resume addressing every id listed here before starting a normal
// user turn, or the server emits RunError per AG-UI contract rule 4.
const pendingInterruptsStateKey = "_agui_pending_interrupts"

// pendingInterruptRecord is the JSON-serializable subset of [types.Interrupt]
// stored in session state for server-side resume validation (schema, expiry).
type pendingInterruptRecord struct {
	ID             string         `json:"id"`
	Reason         string         `json:"reason"`
	ExpiresAt      string         `json:"expiresAt,omitempty"`
	ResponseSchema map[string]any `json:"responseSchema,omitempty"`
}

// toolConfirmationResponseSchema returns the JSON Schema advertised to clients
// on tool-bound interrupts. It follows the AG-UI recommended approve-with-edits
// shape: required approved boolean, optional editedArgs object (full arg replacement).
func toolConfirmationResponseSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"approved": map[string]any{"type": "boolean"},
			"editedArgs": map[string]any{
				"type":        "object",
				"description": "Full replacement of the tool args. Not merged.",
			},
		},
		"required": []any{"approved"},
	}
}

// pendingRecordsFromInterrupts copies interrupt fields needed for validation
// into the session-persisted form.
func pendingRecordsFromInterrupts(interrupts []types.Interrupt) []pendingInterruptRecord {
	out := make([]pendingInterruptRecord, len(interrupts))
	for i, intr := range interrupts {
		out[i] = pendingInterruptRecord{
			ID:             intr.ID,
			Reason:         intr.Reason,
			ExpiresAt:      intr.ExpiresAt,
			ResponseSchema: intr.ResponseSchema,
		}
	}
	return out
}

// getSession loads an ADK session by app/user/session id. Returns a nil
// session.Session (not an error) when the session service is unconfigured.
func (l *aguiLauncher) getSession(ctx context.Context, userID, sessionID string) (session.Session, error) {
	if l.sessionService == nil {
		return nil, nil
	}
	getResp, err := l.sessionService.Get(ctx, &session.GetRequest{
		AppName:   l.config.appName,
		UserID:    userID,
		SessionID: sessionID,
	})
	if err != nil {
		return nil, err
	}
	return getResp.Session, nil
}

// loadPendingInterrupts reads open interrupts from ADK session state for the
// given thread (session id).
//
// Returns (nil, nil) — not an error — when the session does not exist, the
// state key is absent, or a Get call fails. This fail-open design exists
// because ADK session.Service.Get returns an error for both "not found" and
// infrastructure failures (no sentinel not-found error), and because this
// function is called before ensureSessionForSnapshot, so first-run threads
// may not have a session yet.
//
// The only error returned is a decode failure on the stored records
// (indicating data corruption, not a missing session).
func (l *aguiLauncher) loadPendingInterrupts(ctx context.Context, userID, sessionID string) ([]pendingInterruptRecord, error) {
	sess, err := l.getSession(ctx, userID, sessionID)
	if err != nil {
		log.Printf("agui: loadPendingInterrupts: session.Get failed (treating as no pending): %v", err)
		return nil, nil
	}
	if sess == nil {
		return nil, nil
	}
	raw, err := sess.State().Get(pendingInterruptsStateKey)
	if err != nil {
		return nil, nil
	}
	return decodePendingInterruptRecords(raw)
}

// decodePendingInterruptRecords normalizes session state values that may have
// been stored as typed slices or generic JSON-decoded []any.
func decodePendingInterruptRecords(raw any) ([]pendingInterruptRecord, error) {
	switch v := raw.(type) {
	case []pendingInterruptRecord:
		return v, nil
	case []any:
		data, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		var out []pendingInterruptRecord
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, err
		}
		return out, nil
	default:
		data, err := json.Marshal(raw)
		if err != nil {
			return nil, fmt.Errorf("pending interrupts: unsupported type %T", raw)
		}
		var out []pendingInterruptRecord
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, err
		}
		return out, nil
	}
}

// writePendingInterruptsState updates session state via AppendEvent with a state
// delta. ADK merges StateDelta into the session on append (see session.updateSessionState).
func (l *aguiLauncher) writePendingInterruptsState(ctx context.Context, userID, sessionID string, records []pendingInterruptRecord) error {
	sess, err := l.getSession(ctx, userID, sessionID)
	if err != nil {
		return fmt.Errorf("load session for pending interrupts: %w", err)
	}
	if sess == nil {
		return nil
	}
	ev := session.NewEvent("")
	ev.Author = "agui"
	ev.Actions.StateDelta = map[string]any{
		pendingInterruptsStateKey: records,
	}
	return l.sessionService.AppendEvent(ctx, sess, ev)
}

// persistPendingInterrupts saves interrupts emitted at the end of a run so the
// next request on this thread can be validated against AG-UI resume rules.
func (l *aguiLauncher) persistPendingInterrupts(ctx context.Context, userID, sessionID string, interrupts []types.Interrupt) error {
	return l.writePendingInterruptsState(ctx, userID, sessionID, pendingRecordsFromInterrupts(interrupts))
}

// clearPendingInterrupts removes pending interrupt state after a successful
// non-interrupt run (agent completed without pausing for user input).
func (l *aguiLauncher) clearPendingInterrupts(ctx context.Context, userID, sessionID string) error {
	return l.writePendingInterruptsState(ctx, userID, sessionID, nil)
}

// validateResumeAgainstPending enforces AG-UI interrupt contract rules when
// session state contains pending interrupts, and performs basic shape checks
// for resume-only requests without stored state.
//
// When pending is non-empty:
//   - resume must be non-empty (rule 4),
//   - every pending id must appear exactly once in resume,
//   - every resume id must match a pending interrupt,
//   - expired interrupts are rejected,
//   - resolved payloads are checked against the stored responseSchema.
//
// Validation errors are returned to the caller; runSSEHandler emits them as
// RunError after RunStarted so clients receive protocol-level errors on SSE.
func validateResumeAgainstPending(entries []types.ResumeEntry, pending []pendingInterruptRecord, now time.Time) error {
	pendingByID := make(map[string]pendingInterruptRecord, len(pending))
	for _, p := range pending {
		pendingByID[p.ID] = p
	}

	if len(pending) > 0 {
		if len(entries) == 0 {
			return fmt.Errorf("thread has %d pending interrupt(s); resume is required", len(pending))
		}
		resumeIDs := make(map[string]struct{}, len(entries))
		for i, entry := range entries {
			if entry.InterruptID == "" {
				return fmt.Errorf("resume[%d]: interruptId is required", i)
			}
			if _, dup := resumeIDs[entry.InterruptID]; dup {
				return fmt.Errorf("resume[%d]: duplicate interruptId %q", i, entry.InterruptID)
			}
			resumeIDs[entry.InterruptID] = struct{}{}

			rec, ok := pendingByID[entry.InterruptID]
			if !ok {
				return fmt.Errorf("resume[%d]: unknown interruptId %q", i, entry.InterruptID)
			}
			if rec.ExpiresAt != "" && isInterruptExpired(rec.ExpiresAt, now) {
				return fmt.Errorf("resume[%d]: interrupt %q has expired", i, entry.InterruptID)
			}
			if entry.Status == types.ResumeStatusResolved {
				payload, err := resumePayloadMap(entry.Payload)
				if err != nil {
					return fmt.Errorf("resume[%d]: %w", i, err)
				}
				if err := validatePayloadAgainstSchema(payload, rec.ResponseSchema); err != nil {
					return fmt.Errorf("resume[%d]: %w", i, err)
				}
			} else if entry.Status != types.ResumeStatusCancelled {
				return fmt.Errorf("resume[%d]: unsupported status %q", i, entry.Status)
			} else if entry.Payload != nil {
				return fmt.Errorf("resume[%d]: cancelled resume must not include payload", i)
			}
		}
		// Rule 3: cover all open interrupts in one resume array.
		for id := range pendingByID {
			if _, ok := resumeIDs[id]; !ok {
				return fmt.Errorf("resume missing interruptId %q", id)
			}
		}
		return nil
	}

	// No tracked pending state: reject resume-only requests (AG-UI rule 2).
	if len(entries) > 0 {
		return fmt.Errorf("resume is not valid: no pending interrupts for this thread")
	}
	return nil
}

// isInterruptExpired reports whether expiresAt (ISO 8601) is in the past relative to now.
// Unparseable timestamps are treated as non-expired to avoid false rejections.
func isInterruptExpired(expiresAt string, now time.Time) bool {
	t, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		t, err = time.Parse(time.RFC3339Nano, expiresAt)
	}
	if err != nil {
		return false
	}
	return !t.After(now)
}

// validatePayloadAgainstSchema performs a minimal subset of JSON Schema validation
// for interrupt resume payloads: required keys, types, and enum constraints.
// Full JSON Schema validation is left to clients; the server checks enough to
// satisfy AG-UI contract rule 6 (payload validation); see
// https://docs.ag-ui.com/concepts/interrupts#contract-rules.
func validatePayloadAgainstSchema(payload map[string]any, schema map[string]any) error {
	if schema == nil {
		return nil
	}
	required := stringSliceFromSchema(schema["required"])
	for _, key := range required {
		if _, ok := payload[key]; !ok {
			return fmt.Errorf("payload missing required field %q", key)
		}
	}
	props, _ := schema["properties"].(map[string]any)
	if props == nil {
		return nil
	}
	for propName, propSchema := range props {
		propMap, ok := propSchema.(map[string]any)
		if !ok {
			continue
		}
		val, exists := payload[propName]
		if !exists {
			continue
		}
		if err := validateJSONSchemaValue(val, propMap, propName); err != nil {
			return err
		}
	}
	return nil
}

// validateJSONSchemaValue checks type and enum for a property value against its schema fragment.
func validateJSONSchemaValue(value any, schema map[string]any, field string) error {
	if err := validateJSONSchemaType(value, schema, field); err != nil {
		return err
	}
	return validateJSONSchemaEnum(value, schema, field)
}

// stringSliceFromSchema extracts required field names from a JSON Schema fragment.
// Go SDK and AG-UI may encode required as []string or []any after JSON round-trips.
func stringSliceFromSchema(v any) []string {
	switch req := v.(type) {
	case []string:
		return req
	case []any:
		out := make([]string, 0, len(req))
		for _, item := range req {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

// validateJSONSchemaType checks a single property's type keyword when present.
func validateJSONSchemaType(value any, schema map[string]any, field string) error {
	want, _ := schema["type"].(string)
	if want == "" {
		return nil
	}
	switch want {
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s must be a boolean", field)
		}
	case "object":
		if _, ok := value.(map[string]any); !ok {
			return fmt.Errorf("%s must be an object", field)
		}
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("%s must be a string", field)
		}
	case "integer":
		switch v := value.(type) {
		case int, int32, int64:
		case float64:
			if v != math.Trunc(v) {
				return fmt.Errorf("%s must be an integer", field)
			}
		default:
			return fmt.Errorf("%s must be an integer", field)
		}
	case "number":
		switch value.(type) {
		case int, int32, int64, float32, float64:
		default:
			return fmt.Errorf("%s must be a number", field)
		}
	}
	// Recurse into nested object properties when present.
	if want == "object" {
		if obj, ok := value.(map[string]any); ok {
			if nestedProps, ok := schema["properties"].(map[string]any); ok {
				for nestedName, nestedSchema := range nestedProps {
					nestedMap, ok := nestedSchema.(map[string]any)
					if !ok {
						continue
					}
					nestedVal, exists := obj[nestedName]
					if !exists {
						continue
					}
					if err := validateJSONSchemaValue(nestedVal, nestedMap, field+"."+nestedName); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

// validateJSONSchemaEnum checks that value is one of schema.enum when present.
func validateJSONSchemaEnum(value any, schema map[string]any, field string) error {
	enumRaw, ok := schema["enum"]
	if !ok {
		return nil
	}
	enumSlice, ok := enumRaw.([]any)
	if !ok {
		return nil
	}
	for _, allowed := range enumSlice {
		if reflect.DeepEqual(allowed, value) {
			return nil
		}
	}
	return fmt.Errorf("%s must be one of the allowed enum values", field)
}
