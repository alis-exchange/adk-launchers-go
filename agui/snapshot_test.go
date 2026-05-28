package agui

import (
	"context"
	"testing"

	"google.golang.org/adk/session"
)

func TestBuildStateSnapshot_OmitsInternalKeys(t *testing.T) {
	sess := &mockSession{
		id: "s1",
		state: map[string]any{
			"visible":                 "ok",
			pendingInterruptsStateKey: []any{"x"},
			"_agui_processed_resumes": true,
		},
	}
	snap := buildStateSnapshot(sess, map[string]any{"fromReq": 1})
	if snap["visible"] != "ok" {
		t.Errorf("visible = %v, want ok", snap["visible"])
	}
	if snap["fromReq"] != 1 {
		t.Errorf("fromReq = %v, want 1", snap["fromReq"])
	}
	if _, ok := snap[pendingInterruptsStateKey]; ok {
		t.Error("pending interrupts key should be omitted")
	}
	if _, ok := snap["_agui_processed_resumes"]; ok {
		t.Error("_agui_ keys should be omitted")
	}
}

func TestIsInternalStateKey(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{pendingInterruptsStateKey, true},
		{"_agui_foo", true},
		{"_agui_", true},
		{"userVisible", false},
		{"count", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			if got := isInternalStateKey(tt.key); got != tt.want {
				t.Errorf("isInternalStateKey(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

func TestEnsureSessionForSnapshot_CreatesWhenMissing(t *testing.T) {
	svc := session.InMemoryService()
	l := newTestLauncher("test-app", svc)
	ctx := context.Background()
	sess, err := l.ensureSessionForSnapshot(ctx, "user-1", "thread-1", map[string]any{"init": true})
	if err != nil {
		t.Fatalf("ensureSessionForSnapshot() error = %v", err)
	}
	if sess == nil {
		t.Fatal("expected session")
	}
	if sess.ID() != "thread-1" {
		t.Errorf("session ID = %q, want thread-1", sess.ID())
	}
}
