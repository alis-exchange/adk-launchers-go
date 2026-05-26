package scheduler

import (
	"testing"

	pb "go.alis.build/common/alis/a2a/extension/scheduler/v1"
)

func TestOwnerFromCron(t *testing.T) {
	t.Parallel()

	_, err := ownerFromCron(&pb.Cron{Name: "crons/x", Owner: ""})
	if err == nil {
		t.Fatal("expected error for empty owner")
	}

	_, err = ownerFromCron(&pb.Cron{Name: "crons/x", Owner: "users"})
	if err == nil {
		t.Fatal("expected error for malformed owner")
	}

	_, err = ownerFromCron(&pb.Cron{Name: "crons/x", Owner: "groups/alice"})
	if err == nil {
		t.Fatal("expected error for non-users/ prefix")
	}

	_, err = ownerFromCron(&pb.Cron{Name: "crons/x", Owner: "/alice"})
	if err == nil {
		t.Fatal("expected error for missing users prefix")
	}

	id, err := ownerFromCron(&pb.Cron{Name: "crons/x", Owner: "users/alice"})
	if err != nil || id != "alice" {
		t.Fatalf("owner = %q err = %v", id, err)
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
