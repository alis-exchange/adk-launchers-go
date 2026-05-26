package scheduler

import (
	"context"
	"fmt"
	"os"
	"strings"

	pb "go.alis.build/common/alis/a2a/extension/scheduler/v1"
	"go.alis.build/iam/v3"
)

// cronConfig holds runtime options for the Cloud Tasks cron handler and executeCron.
// Populated via [Option] functions on [NewLauncher].
type cronConfig struct {
	// systemIdentity is the IAM principal for SchedulerService GetCron and UpdateCron.
	// When nil, [defaultSystemIdentity] is used at handler construction time.
	systemIdentity *iam.Identity
	// syncExecution when true blocks the HTTP response until executeCron completes.
	syncExecution bool
	// observer receives lifecycle callbacks; nil disables observation.
	observer CronObserver
}

// resolveSystemIdentity returns the configured system identity or the environment default.
func (cfg *cronConfig) resolveSystemIdentity() *iam.Identity {
	if cfg.systemIdentity != nil {
		return cfg.systemIdentity
	}
	return defaultSystemIdentity()
}

// defaultSystemIdentity returns alis-build@$ALIS_OS_PROJECT.iam.gserviceaccount.com
// when ALIS_OS_PROJECT is set; otherwise nil and the handler will reject requests.
func defaultSystemIdentity() *iam.Identity {
	projectID := os.Getenv("ALIS_OS_PROJECT")
	if projectID == "" {
		return nil
	}
	email := fmt.Sprintf("alis-build@%s.iam.gserviceaccount.com", projectID)
	return &iam.Identity{ID: email, Email: email, Type: iam.ServiceAccount}
}

// cronRunner executes ADK user prompts during cron ticks.
// [*adkrun.Runtime] satisfies this interface in production; tests use fakes.
type cronRunner interface {
	// RunUserMessage runs one user text turn and returns the ADK session id to persist.
	RunUserMessage(ctx context.Context, userID, sessionID, prompt string) (string, error)
}

// cronResponse is the JSON body returned to Cloud Tasks for both success and failure.
type cronResponse struct {
	// Status is "OK" or "FAILED".
	Status string `json:"status"`
	// Error is set when Status is "FAILED".
	Error string `json:"error,omitempty"`
}

// CronObserver receives lifecycle notifications for in-process cron execution.
// Use [WithCronObserver] to register an implementation for metrics, tracing, or logging.
//
// Implementations must be safe for concurrent use: multiple cron goroutines may call
// these methods when [WithSynchronousExecution] is false (the default).
type CronObserver interface {
	// OnCronStarted is called at the beginning of executeCron, before any ADK run.
	OnCronStarted(ctx context.Context, cron *pb.Cron)
	// OnCronFinished is called when executeCron returns. err is nil if both the
	// agent run and cron persist succeeded; non-nil for agent or persist failures.
	OnCronFinished(ctx context.Context, cron *pb.Cron, err error)
}

// ownerFromCron extracts the user id from cron.owner, which must be users/{id}.
func ownerFromCron(cron *pb.Cron) (string, error) {
	owner := strings.TrimSpace(cron.GetOwner())
	id, ok := strings.CutPrefix(owner, "users/")
	if !ok || strings.TrimSpace(id) == "" {
		return "", fmt.Errorf("cron %s: invalid owner %q (expected users/{id})", cron.GetName(), owner)
	}
	return id, nil
}

// mergeSessionID returns the ADK session id to persist on the cron.
// A non-empty returned id from RunUserMessage wins; otherwise the existing id is kept.
func mergeSessionID(existing, returned string) string {
	if returned != "" {
		return returned
	}
	return existing
}

// validateCronForRun rejects crons that cannot produce a user message for the agent.
func validateCronForRun(cron *pb.Cron) error {
	if strings.TrimSpace(cron.GetPrompt()) == "" {
		return fmt.Errorf("cron %s: prompt is required", cron.GetName())
	}
	return nil
}
