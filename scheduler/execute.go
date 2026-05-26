package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"go.alis.build/alog"
	pb "go.alis.build/common/alis/a2a/extension/scheduler/v1"
	"go.alis.build/iam/v3"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// cronHandler returns the HTTP handler mounted at schedulerext.HandlerPath.
//
// svc provides cron storage; rt runs the agent; cfg controls identity and sync behavior.
// The returned handler uses the system identity for SchedulerService RPCs and impersonates
// the cron owner inside executeCron.
func cronHandler(
	svc pb.SchedulerServiceServer,
	rt cronRunner,
	systemIdentity *iam.Identity,
	cfg *cronConfig,
) func(http.ResponseWriter, *http.Request) error {
	return func(w http.ResponseWriter, r *http.Request) error {
		ctx := incomingContext(r)
		// SchedulerService reads/writes use the system service account.
		ctx = systemIdentity.Context(ctx)

		var body struct {
			// ID is the cron resource id (without the "crons/" prefix).
			ID string `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return writeCronFailed(w, "decode request body")
		}
		if strings.TrimSpace(body.ID) == "" {
			return writeCronFailed(w, "cron id is required")
		}

		cron, err := svc.GetCron(ctx, &pb.GetCronRequest{Name: "crons/" + body.ID})
		if err != nil {
			return writeCronFailed(w, err.Error())
		}
		// Archived crons are acknowledged without re-running the agent.
		if cron.GetState() == pb.Cron_STATE_ARCHIVED {
			return writeCronOK(w)
		}

		ownerID, err := ownerFromCron(cron)
		if err != nil {
			return writeCronFailed(w, err.Error())
		}
		if err := validateCronForRun(cron); err != nil {
			return writeCronFailed(w, err.Error())
		}

		alog.Infof(ctx, "scheduler: executing cron %s", body.ID)

		// Detach from request cancellation so async runs and logging
		// complete after the HTTP response is sent.
		detachedCtx := context.WithoutCancel(ctx)

		if cfg.syncExecution {
			if err := executeCron(detachedCtx, svc, rt, cfg, cron, ownerID); err != nil {
				return writeCronFailed(w, err.Error())
			}
			return writeCronOK(w)
		}

		go func() {
			if err := executeCron(detachedCtx, svc, rt, cfg, cron, ownerID); err != nil {
				alog.Errorf(detachedCtx, "scheduler: cron %s: %v", cron.GetName(), err)
			}
		}()
		return writeCronOK(w)
	}
}

// executeCron runs the agent for one cron tick and updates cron metadata in Spanner.
//
// ctx must carry the system identity (for UpdateCron). ADK runs use userRunContext.
// Behavior matches the stock extension handler: initial_prompt seeding for TYPE_CRON,
// session reuse via context_id, and TYPE_AT archival after a successful run.
//
// Returns a non-nil error only for agent failures. UpdateCron failures are logged
// and reported to the [CronObserver] but do not cause a returned error, preventing
// Cloud Tasks from retrying an already-completed agent run.
func executeCron(
	ctx context.Context,
	svc pb.SchedulerServiceServer,
	rt cronRunner,
	cfg *cronConfig,
	cron *pb.Cron,
	ownerID string,
) error {
	if cfg.observer != nil {
		cfg.observer.OnCronStarted(ctx, cron)
	}
	var runErr error
	defer func() {
		if cfg.observer != nil {
			cfg.observer.OnCronFinished(ctx, cron, runErr)
		}
	}()

	// ADK runs as the cron owner; SchedulerService RPCs in this function use system ctx.
	runCtx := userRunContext(ctx, ownerID, cron.GetEmail())

	sessionID := cron.GetContextId()

	// Recurring crons: run initial_prompt once before the first regular prompt.
	if cron.GetType() == pb.Cron_TYPE_CRON && sessionID == "" && strings.TrimSpace(cron.GetInitialPrompt()) != "" {
		id, err := rt.RunUserMessage(runCtx, ownerID, "", cron.GetInitialPrompt())
		if err != nil {
			runErr = fmt.Errorf("initial run: %w", err)
			return runErr
		}
		sessionID = mergeSessionID(sessionID, id)
	}

	id, err := rt.RunUserMessage(runCtx, ownerID, sessionID, cron.GetPrompt())
	if err != nil {
		runErr = fmt.Errorf("run: %w", err)
		return runErr
	}
	sessionID = mergeSessionID(sessionID, id)

	now := timestamppb.Now()
	update := &pb.Cron{
		Name:        cron.GetName(),
		ContextId:   sessionID,
		LastRunTime: now,
	}
	paths := []string{"last_run_time"}
	if cron.GetContextId() != sessionID && sessionID != "" {
		paths = append(paths, "context_id")
	}
	if cron.GetType() == pb.Cron_TYPE_AT {
		update.State = pb.Cron_STATE_ARCHIVED
		update.ArchiveTime = now
		paths = append(paths, "state", "archive_time")
	}

	// Log UpdateCron failures instead of returning them. The agent run already
	// succeeded, so returning an error here would cause Cloud Tasks to retry
	// and duplicate the agent execution. The observer still sees the persist
	// error via runErr so operators can track persist failures separately.
	//
	// Trade-off: TYPE_AT crons will not be archived on persist failure, so
	// Cloud Tasks may re-invoke them. This is preferred over the alternative
	// (returning error → guaranteed retry → guaranteed duplicate execution).
	if _, err := svc.UpdateCron(ctx, &pb.UpdateCronRequest{
		Cron:       update,
		UpdateMask: &fieldmaskpb.FieldMask{Paths: paths},
	}); err != nil {
		runErr = fmt.Errorf("persist cron: %w", err)
		alog.Errorf(ctx, "scheduler: update cron %s after successful run: %v", cron.GetName(), err)
	}
	return nil
}

// userRunContext returns a context that runs ADK as the cron owner.
// Mirrors the stock handler's callAgent(userID, email) impersonation.
func userRunContext(parent context.Context, ownerID, email string) context.Context {
	if email == "" {
		email = ownerID
	}
	user := &iam.Identity{ID: ownerID, Email: email, Type: iam.User}
	if strings.HasSuffix(email, ".iam.gserviceaccount.com") {
		user.Type = iam.ServiceAccount
	}
	ctx := user.OutgoingMetadata(parent)
	return user.Context(ctx)
}

// incomingContext copies HTTP headers into gRPC incoming metadata for downstream RPCs.
func incomingContext(r *http.Request) context.Context {
	md := metadata.MD{}
	for k, vs := range r.Header {
		md[strings.ToLower(k)] = append([]string(nil), vs...)
	}
	return metadata.NewIncomingContext(r.Context(), md)
}

// writeCronOK writes a 200 JSON response acknowledging the cron invocation.
func writeCronOK(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(cronResponse{Status: "OK"})
	return nil
}

// writeCronFailed writes a 500 JSON response; Cloud Tasks may retry depending on queue config.
func writeCronFailed(w http.ResponseWriter, msg string) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	json.NewEncoder(w).Encode(cronResponse{Status: "FAILED", Error: msg})
	return nil
}
