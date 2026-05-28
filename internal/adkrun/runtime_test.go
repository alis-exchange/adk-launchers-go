package adkrun

import (
	"encoding/json"
	"fmt"
	"iter"
	"testing"

	"google.golang.org/adk/agent"
	"google.golang.org/adk/cmd/launcher"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

// testRuntime returns a Runtime with a minimal no-op agent backed by in-memory sessions.
func testRuntime(t *testing.T) *Runtime {
	t.Helper()

	a, err := agent.New(agent.Config{
		Name:        "test-agent",
		Description: "no-op agent for unit tests",
		Run: func(agent.InvocationContext) iter.Seq2[*session.Event, error] {
			return func(yield func(*session.Event, error) bool) {}
		},
	})
	if err != nil {
		t.Fatalf("create test agent: %v", err)
	}

	rt, err := NewRuntime(&launcher.Config{
		AgentLoader:    agent.NewSingleLoader(a),
		SessionService: session.InMemoryService(),
	}, "test-agent")
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	return rt
}

func TestNewRuntime_validation(t *testing.T) {
	t.Parallel()

	a, _ := agent.New(agent.Config{Name: "a", Run: func(agent.InvocationContext) iter.Seq2[*session.Event, error] {
		return func(yield func(*session.Event, error) bool) {}
	}})

	tests := []struct {
		name   string
		config *launcher.Config
		app    string
	}{
		{"nil config", nil, "app"},
		{"empty app name", &launcher.Config{}, ""},
		{"nil AgentLoader", &launcher.Config{AgentLoader: nil, SessionService: nil}, "app"},
		{"nil SessionService", &launcher.Config{AgentLoader: agent.NewSingleLoader(a), SessionService: nil}, "app"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewRuntime(tt.config, tt.app)
			if err == nil {
				t.Fatalf("NewRuntime() expected error for %s", tt.name)
			}
		})
	}
}

func TestNewRuntime_appNameTrimmed(t *testing.T) {
	t.Parallel()

	a, _ := agent.New(agent.Config{Name: "app", Run: func(agent.InvocationContext) iter.Seq2[*session.Event, error] {
		return func(yield func(*session.Event, error) bool) {}
	}})
	rt, err := NewRuntime(&launcher.Config{
		AgentLoader:    agent.NewSingleLoader(a),
		SessionService: session.InMemoryService(),
	}, "  app  ")
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	if rt.AppName() != "app" {
		t.Fatalf("AppName() = %q, want %q", rt.AppName(), "app")
	}
}

func TestRunRequestMarshal(t *testing.T) {
	req := RunRequest{
		AppName:   "my.agent",
		UserID:    "user-1",
		SessionID: "session-1",
		NewMessage: Content{
			Role: "user",
			Parts: []*Part{
				genai.NewPartFromText("hello"),
				genai.NewPartFromFunctionResponse("lookup", map[string]any{"id": 42}),
			},
		},
		Streaming:                 true,
		SaveInputBlobsAsArtifacts: true,
		StateDelta:                map[string]any{"key": "value"},
		FunctionCallEventID:       "evt-1",
		InvocationID:              "inv-1",
	}

	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	for _, key := range []string{
		"appName", "userId", "sessionId", "newMessage", "streaming",
		"saveInputBlobsAsArtifacts", "stateDelta", "functionCallEventId", "invocationId",
	} {
		if _, ok := got[key]; !ok {
			t.Errorf("missing top-level field %q in %s", key, string(body))
		}
	}

	newMessage, ok := got["newMessage"].(map[string]any)
	if !ok {
		t.Fatalf("newMessage type = %T", got["newMessage"])
	}
	parts, ok := newMessage["parts"].([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("parts = %#v", newMessage["parts"])
	}
	first, ok := parts[0].(map[string]any)
	if !ok || first["text"] != "hello" {
		t.Fatalf("first part = %#v", parts[0])
	}
	second, ok := parts[1].(map[string]any)
	if !ok {
		t.Fatalf("second part type = %T", parts[1])
	}
	if _, ok := second["functionResponse"]; !ok {
		t.Fatalf("second part missing functionResponse: %#v", second)
	}
}

func TestUserTextMessage(t *testing.T) {
	msg := UserTextMessage("ping")
	if msg.Role != string(genai.RoleUser) {
		t.Fatalf("role = %q", msg.Role)
	}
	if len(msg.Parts) != 1 || msg.Parts[0].Text != "ping" {
		t.Fatalf("parts = %#v", msg.Parts)
	}
}

func TestRunSSE_validation(t *testing.T) {
	t.Parallel()

	rt := testRuntime(t)

	_, events, err := rt.RunSSE(t.Context(), RunRequest{})
	if err == nil {
		t.Fatal("expected validation error for empty UserID")
	}
	if events != nil {
		t.Fatal("expected nil events iterator on validation error")
	}

	_, _, err = rt.RunSSE(t.Context(), RunRequest{UserID: "user"})
	if err == nil {
		t.Fatal("expected validation error for empty Parts")
	}
}

func TestRunSSE_happyPath(t *testing.T) {
	rt := testRuntime(t)

	sessionID, events, err := rt.RunSSE(t.Context(), RunRequest{
		UserID:     "user-1",
		NewMessage: UserTextMessage("hello"),
	})
	if err != nil {
		t.Fatalf("RunSSE: %v", err)
	}
	if sessionID == "" {
		t.Fatal("expected non-empty sessionID")
	}

	var count int
	for _, err := range events {
		if err != nil {
			t.Fatalf("event error: %v", err)
		}
		count++
	}
	// No-op agent produces no events; verify iteration completed without error.
	_ = count
}

func TestRunSSE_sessionIDPreserved(t *testing.T) {
	rt := testRuntime(t)

	sessionID, events, err := rt.RunSSE(t.Context(), RunRequest{
		UserID:     "user-1",
		SessionID:  "custom-session",
		NewMessage: UserTextMessage("hello"),
	})
	if err != nil {
		t.Fatalf("RunSSE: %v", err)
	}
	if sessionID != "custom-session" {
		t.Fatalf("sessionID = %q, want custom-session", sessionID)
	}
	for _, err := range events {
		if err != nil {
			t.Fatalf("event error: %v", err)
		}
	}
}

func TestRunSSE_partsNotMutated(t *testing.T) {
	rt := testRuntime(t)

	originalPart := genai.NewPartFromText("original")
	req := RunRequest{
		UserID:     "user-1",
		NewMessage: Content{Role: "user", Parts: []*Part{originalPart}},
	}

	_, events, err := rt.RunSSE(t.Context(), req)
	if err != nil {
		t.Fatalf("RunSSE: %v", err)
	}
	for _, err := range events {
		if err != nil {
			t.Fatalf("event error: %v", err)
		}
	}

	if req.NewMessage.Parts[0] != originalPart {
		t.Fatal("caller's Parts slice was mutated")
	}
	if req.NewMessage.Parts[0].Text != "original" {
		t.Fatalf("original part text = %q", req.NewMessage.Parts[0].Text)
	}
}

func TestRunUserMessage_happyPath(t *testing.T) {
	rt := testRuntime(t)

	sessionID, err := rt.RunUserMessage(t.Context(), "user-1", "", "hello")
	if err != nil {
		t.Fatalf("RunUserMessage: %v", err)
	}
	if sessionID == "" {
		t.Fatal("expected non-empty sessionID")
	}
}

func TestRunUserMessage_emptyPrompt(t *testing.T) {
	rt := testRuntime(t)

	for _, prompt := range []string{"", "   "} {
		t.Run(fmt.Sprintf("prompt=%q", prompt), func(t *testing.T) {
			_, err := rt.RunUserMessage(t.Context(), "user-1", "", prompt)
			if err == nil {
				t.Fatalf("expected error for prompt %q", prompt)
			}
		})
	}
}

func TestRunUserMessage_midStreamError(t *testing.T) {
	// Create an agent that yields an error mid-stream.
	a, err := agent.New(agent.Config{
		Name: "err-agent",
		Run: func(agent.InvocationContext) iter.Seq2[*session.Event, error] {
			return func(yield func(*session.Event, error) bool) {
				yield(nil, fmt.Errorf("mid-stream failure"))
			}
		},
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}

	rt, err := NewRuntime(&launcher.Config{
		AgentLoader:    agent.NewSingleLoader(a),
		SessionService: session.InMemoryService(),
	}, "err-agent")
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}

	_, err = rt.RunUserMessage(t.Context(), "user-1", "", "hello")
	if err == nil {
		t.Fatal("expected error from mid-stream failure")
	}
}
