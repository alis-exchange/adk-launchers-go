package scheduler

import (
	"testing"

	pb "go.alis.build/common/alis/a2a/extension/scheduler/v1"
)

func TestOwnerFromCron(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		owner   string
		wantID  string
		wantErr bool
	}{
		{"empty owner", "", "", true},
		{"malformed owner", "users", "", true},
		{"non-users prefix", "groups/alice", "", true},
		{"missing users prefix", "/alice", "", true},
		{"valid owner", "users/alice", "alice", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := ownerFromCron(&pb.Cron{Name: "crons/x", Owner: tt.owner})
			if (err != nil) != tt.wantErr {
				t.Fatalf("ownerFromCron(%q) error = %v, wantErr %v", tt.owner, err, tt.wantErr)
			}
			if !tt.wantErr && id != tt.wantID {
				t.Fatalf("ownerFromCron(%q) = %q, want %q", tt.owner, id, tt.wantID)
			}
		})
	}
}

func TestMergeSessionID(t *testing.T) {
	t.Parallel()

	if got := mergeSessionID("keep", ""); got != "keep" {
		t.Fatalf("got %q", got)
	}
	if got := mergeSessionID("", "new"); got != "new" {
		t.Fatalf("got %q", got)
	}
}

func TestValidateCronForRun(t *testing.T) {
	t.Parallel()

	err := validateCronForRun(&pb.Cron{Name: "crons/x", Owner: "users/a", Prompt: "  "})
	if err == nil {
		t.Fatal("expected error for empty prompt")
	}
}
