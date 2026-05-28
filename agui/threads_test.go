package agui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.alis.build/iam/v3"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

func testIdentity(id string) *iam.Identity {
	return &iam.Identity{
		Type: iam.User,
		ID:   id,
	}
}

func setupThreadTestLauncher(t *testing.T) (*aguiLauncher, session.Service) {
	t.Helper()
	svc := session.InMemoryService()
	l := newTestLauncher("test-app", svc)
	return l, svc
}

func createSessionWithEvents(t *testing.T, ctx context.Context, svc session.Service, userID, sessionID string, events []*session.Event, state map[string]any) {
	t.Helper()
	createResp, err := svc.Create(ctx, &session.CreateRequest{
		AppName:   "test-app",
		UserID:    userID,
		SessionID: sessionID,
		State:     state,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	for _, ev := range events {
		if err := svc.AppendEvent(ctx, createResp.Session, ev); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}
}

func callThreadHandler(l *aguiLauncher, identity *iam.Identity, threadID string, query string, accept string) *httptest.ResponseRecorder {
	handler := l.threadMessagesHandler()

	path := "/agui/threads/" + threadID + "/messages"
	if query != "" {
		path += "?" + query
	}
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.SetPathValue("threadId", threadID)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}

	ctx := req.Context()
	if identity != nil {
		ctx = identity.Context(ctx)
	}
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestThreadMessages_ErrorCases(t *testing.T) {
	tests := []struct {
		name       string
		launcher   func(t *testing.T) *aguiLauncher
		identity   *iam.Identity
		threadID   string
		wantStatus int
	}{
		{
			name:       "missing identity",
			launcher:   func(t *testing.T) *aguiLauncher { l, _ := setupThreadTestLauncher(t); return l },
			identity:   nil,
			threadID:   "thread-1",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "nil session service",
			launcher:   func(t *testing.T) *aguiLauncher { return newTestLauncher("test-app") },
			identity:   testIdentity("user-1"),
			threadID:   "thread-1",
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name:       "missing thread ID",
			launcher:   func(t *testing.T) *aguiLauncher { l, _ := setupThreadTestLauncher(t); return l },
			identity:   testIdentity("user-1"),
			threadID:   "",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "unknown session",
			launcher:   func(t *testing.T) *aguiLauncher { l, _ := setupThreadTestLauncher(t); return l },
			identity:   testIdentity("user-1"),
			threadID:   "nonexistent",
			wantStatus: http.StatusNotFound,
		},
		{
			name: "wrong user",
			launcher: func(t *testing.T) *aguiLauncher {
				l, svc := setupThreadTestLauncher(t)
				createSessionWithEvents(t, context.Background(), svc, "user-1", "thread-1", nil, nil)
				return l
			},
			identity:   testIdentity("user-2"),
			threadID:   "thread-1",
			wantStatus: http.StatusNotFound,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := callThreadHandler(tt.launcher(t), tt.identity, tt.threadID, "", "")
			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
		})
	}
}

func TestThreadMessages_InvalidParams(t *testing.T) {
	tests := []struct {
		name  string
		query string
	}{
		{"invalid after", "after=not-a-timestamp"},
		{"invalid limit", "limit=abc"},
		{"negative limit", "limit=-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l, _ := setupThreadTestLauncher(t)
			rec := callThreadHandler(l, testIdentity("user-1"), "thread-1", tt.query, "")
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestThreadMessages_EmptySession(t *testing.T) {
	l, svc := setupThreadTestLauncher(t)
	ctx := context.Background()
	createSessionWithEvents(t, ctx, svc, "user-1", "thread-1", nil, nil)

	rec := callThreadHandler(l, testIdentity("user-1"), "thread-1", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp threadMessagesResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error = %v", err)
	}
	if len(resp.Messages) != 0 {
		t.Errorf("messages count = %d, want 0", len(resp.Messages))
	}
	if resp.NextCursor != "" {
		t.Errorf("nextCursor = %q, want empty", resp.NextCursor)
	}
}

func TestThreadMessages_HappyPath(t *testing.T) {
	l, svc := setupThreadTestLauncher(t)
	ctx := context.Background()

	ev1 := session.NewEvent("ev1")
	ev1.Content = genai.NewContentFromText("Hello", genai.RoleUser)

	ev2 := session.NewEvent("ev2")
	ev2.Content = genai.NewContentFromText("Hi there!", genai.RoleModel)

	createSessionWithEvents(t, ctx, svc, "user-1", "thread-1", []*session.Event{ev1, ev2}, nil)

	rec := callThreadHandler(l, testIdentity("user-1"), "thread-1", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp threadMessagesResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error = %v", err)
	}
	if len(resp.Messages) != 2 {
		t.Fatalf("messages count = %d, want 2", len(resp.Messages))
	}
	if resp.Messages[0].Role != "user" {
		t.Errorf("messages[0].Role = %q, want user", resp.Messages[0].Role)
	}
	if resp.Messages[1].Role != "assistant" {
		t.Errorf("messages[1].Role = %q, want assistant", resp.Messages[1].Role)
	}
	contentStr, ok := resp.Messages[1].ContentString()
	if !ok || contentStr != "Hi there!" {
		t.Errorf("messages[1].Content = %v, want 'Hi there!'", resp.Messages[1].Content)
	}
}

func TestThreadMessages_Limit(t *testing.T) {
	l, svc := setupThreadTestLauncher(t)
	ctx := context.Background()

	ev1 := session.NewEvent("ev1")
	ev1.Content = genai.NewContentFromText("msg1", genai.RoleUser)

	ev2 := session.NewEvent("ev2")
	ev2.Content = genai.NewContentFromText("msg2", genai.RoleModel)

	ev3 := session.NewEvent("ev3")
	ev3.Content = genai.NewContentFromText("msg3", genai.RoleUser)

	createSessionWithEvents(t, ctx, svc, "user-1", "thread-1", []*session.Event{ev1, ev2, ev3}, nil)

	rec := callThreadHandler(l, testIdentity("user-1"), "thread-1", "limit=2", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp threadMessagesResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error = %v", err)
	}
	if len(resp.Messages) != 2 {
		t.Fatalf("messages count = %d, want 2", len(resp.Messages))
	}
	if resp.NextCursor == "" {
		t.Error("expected nextCursor to be set when limit reached")
	}
}

func TestThreadMessages_SSE(t *testing.T) {
	l, svc := setupThreadTestLauncher(t)
	ctx := context.Background()

	ev1 := session.NewEvent("ev1")
	ev1.Content = genai.NewContentFromText("Hello", genai.RoleUser)

	createSessionWithEvents(t, ctx, svc, "user-1", "thread-1", []*session.Event{ev1}, map[string]any{"key": "value"})

	rec := callThreadHandler(l, testIdentity("user-1"), "thread-1", "", "text/event-stream")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	body := rec.Body.String()
	for _, want := range []string{"RUN_STARTED", "MESSAGES_SNAPSHOT", "STATE_SNAPSHOT", "RUN_FINISHED"} {
		if !strings.Contains(body, want) {
			t.Errorf("SSE response missing %s", want)
		}
	}
}

func TestThreadMessages_SSE_EmptyState(t *testing.T) {
	l, svc := setupThreadTestLauncher(t)
	ctx := context.Background()

	ev1 := session.NewEvent("ev1")
	ev1.Content = genai.NewContentFromText("Hello", genai.RoleUser)

	createSessionWithEvents(t, ctx, svc, "user-1", "thread-1", []*session.Event{ev1}, nil)

	rec := callThreadHandler(l, testIdentity("user-1"), "thread-1", "", "text/event-stream")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	if strings.Contains(rec.Body.String(), "STATE_SNAPSHOT") {
		t.Error("SSE response should not contain STATE_SNAPSHOT when state is empty")
	}
}

func TestThreadMessages_SSE_FiltersInternalState(t *testing.T) {
	l, svc := setupThreadTestLauncher(t)
	ctx := context.Background()

	ev1 := session.NewEvent("ev1")
	ev1.Content = genai.NewContentFromText("Hello", genai.RoleUser)

	createSessionWithEvents(t, ctx, svc, "user-1", "thread-1", []*session.Event{ev1}, map[string]any{
		"visible":                 "yes",
		pendingInterruptsStateKey: []any{},
		"_agui_internal":          "hidden",
	})

	rec := callThreadHandler(l, testIdentity("user-1"), "thread-1", "", "text/event-stream")
	body := rec.Body.String()

	checks := []struct {
		substr string
		want   bool
	}{
		{"visible", true},
		{pendingInterruptsStateKey, false},
		{"_agui_internal", false},
	}
	for _, c := range checks {
		if strings.Contains(body, c.substr) != c.want {
			if c.want {
				t.Errorf("SSE should include %q", c.substr)
			} else {
				t.Errorf("SSE should filter %q", c.substr)
			}
		}
	}
}

func TestAcceptsSSE(t *testing.T) {
	tests := []struct {
		accept string
		want   bool
	}{
		{"text/event-stream", true},
		{"text/event-stream, application/json", true},
		{"application/json", false},
		{"*/*", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.accept, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.accept != "" {
				r.Header.Set("Accept", tt.accept)
			}
			if got := acceptsSSE(r); got != tt.want {
				t.Errorf("acceptsSSE(%q) = %v, want %v", tt.accept, got, tt.want)
			}
		})
	}
}
