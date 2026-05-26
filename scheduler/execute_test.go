package scheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	pb "go.alis.build/common/alis/a2a/extension/scheduler/v1"
	"go.alis.build/iam/v3"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeScheduler struct {
	pb.UnimplementedSchedulerServiceServer

	cron      *pb.Cron
	update    *pb.UpdateCronRequest
	updateErr error
}

func (f *fakeScheduler) GetCron(context.Context, *pb.GetCronRequest) (*pb.Cron, error) {
	if f.cron == nil {
		return nil, status.Error(codes.NotFound, "cron not found")
	}
	return f.cron, nil
}

func (f *fakeScheduler) UpdateCron(_ context.Context, req *pb.UpdateCronRequest) (*pb.Cron, error) {
	f.update = req
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	return req.GetCron(), nil
}

type recordedRun struct {
	userID    string
	sessionID string
	prompt    string
}

type fakeRunner struct {
	runs      []recordedRun
	nextSess  string
	runErr    error
	done      chan struct{}
}

func (f *fakeRunner) RunUserMessage(_ context.Context, userID string, sessionID, prompt string) (string, error) {
	if f.runErr != nil {
		return "", f.runErr
	}
	f.runs = append(f.runs, recordedRun{userID: userID, sessionID: sessionID, prompt: prompt})
	if f.nextSess == "" {
		f.nextSess = "adk-session-1"
	}
	if f.done != nil {
		f.done <- struct{}{}
	}
	return f.nextSess, nil
}

type testObserver struct {
	onStarted  func(context.Context, *pb.Cron)
	onFinished func(context.Context, *pb.Cron, error)
}

func (o *testObserver) OnCronStarted(ctx context.Context, cron *pb.Cron) {
	if o.onStarted != nil {
		o.onStarted(ctx, cron)
	}
}

func (o *testObserver) OnCronFinished(ctx context.Context, cron *pb.Cron, err error) {
	if o.onFinished != nil {
		o.onFinished(ctx, cron, err)
	}
}

// callCronHandler builds a cronHandler and invokes it with the given cron ID.
func callCronHandler(t *testing.T, svc *fakeScheduler, runner *fakeRunner, cfg *cronConfig, cronID string) *httptest.ResponseRecorder {
	t.Helper()
	handler := cronHandler(svc, runner, cfg.systemIdentity, cfg)
	body, _ := json.Marshal(map[string]string{"id": cronID})
	req := httptest.NewRequest(http.MethodPost, "/handler", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	if err := handler(rec, req); err != nil {
		t.Fatalf("handler: %v", err)
	}
	return rec
}

func testCronConfig() *cronConfig {
	return &cronConfig{
		systemIdentity: &iam.Identity{
			ID:    "system@test",
			Email: "system@test",
			Type:  iam.ServiceAccount,
		},
	}
}

// TestSmoke_executeCron runs cron tick → ADK prompts → UpdateCron (in-process smoke).
func TestSmoke_executeCron(t *testing.T) {
	svc := &fakeScheduler{
		cron: &pb.Cron{
			Name:    "crons/smoke-1",
			Owner:   "users/alice",
			Email:   "alice@example.com",
			Prompt:  "daily check-in",
			Type:    pb.Cron_TYPE_CRON,
			ContextId: "",
		},
	}
	runner := &fakeRunner{nextSess: "sess-smoke"}

	if err := executeCron(context.Background(), svc, runner, testCronConfig(), svc.cron, "alice"); err != nil {
		t.Fatalf("executeCron: %v", err)
	}

	if len(runner.runs) != 1 || runner.runs[0].prompt != "daily check-in" {
		t.Fatalf("runs = %#v", runner.runs)
	}
	if runner.runs[0].userID != "alice" {
		t.Fatalf("userID = %q, want alice", runner.runs[0].userID)
	}
	if svc.update == nil {
		t.Fatal("expected UpdateCron")
	}
	if svc.update.GetCron().GetContextId() != "sess-smoke" {
		t.Fatalf("context_id = %q", svc.update.GetCron().GetContextId())
	}
	if svc.update.GetCron().GetLastRunTime() == nil {
		t.Fatal("expected last_run_time")
	}
	if !slices.Contains(svc.update.GetUpdateMask().GetPaths(), "context_id") {
		t.Fatalf("update mask = %v", svc.update.GetUpdateMask().GetPaths())
	}
}

func TestSmoke_executeCron_initialPromptThenMain(t *testing.T) {
	svc := &fakeScheduler{
		cron: &pb.Cron{
			Name:          "crons/recurring",
			Owner:         "users/bob",
			Prompt:        "tick",
			InitialPrompt: "bootstrap",
			Type:          pb.Cron_TYPE_CRON,
		},
	}
	runner := &fakeRunner{nextSess: "sess-recur"}

	if err := executeCron(context.Background(), svc, runner, testCronConfig(), svc.cron, "bob"); err != nil {
		t.Fatalf("executeCron: %v", err)
	}
	if len(runner.runs) != 2 {
		t.Fatalf("runs = %#v", runner.runs)
	}
	if runner.runs[0].prompt != "bootstrap" || runner.runs[1].prompt != "tick" {
		t.Fatalf("unexpected prompts: %#v", runner.runs)
	}
}

func TestSmoke_executeCron_typeAtArchives(t *testing.T) {
	svc := &fakeScheduler{
		cron: &pb.Cron{
			Name:   "crons/once",
			Owner:  "users/alice",
			Prompt: "run once",
			Type:   pb.Cron_TYPE_AT,
		},
	}
	runner := &fakeRunner{nextSess: "sess-at"}

	if err := executeCron(context.Background(), svc, runner, testCronConfig(), svc.cron, "alice"); err != nil {
		t.Fatalf("executeCron: %v", err)
	}
	if svc.update.GetCron().GetState() != pb.Cron_STATE_ARCHIVED {
		t.Fatalf("state = %v", svc.update.GetCron().GetState())
	}
	paths := svc.update.GetUpdateMask().GetPaths()
	for _, want := range []string{"state", "archive_time"} {
		if !slices.Contains(paths, want) {
			t.Fatalf("update mask missing %q: %v", want, paths)
		}
	}
}

func TestCronHandler_sync_smoke(t *testing.T) {
	svc := &fakeScheduler{
		cron: &pb.Cron{
			Name:   "crons/handler-1",
			Owner:  "users/alice",
			Prompt: "hello",
			Type:   pb.Cron_TYPE_CRON,
		},
	}
	runner := &fakeRunner{nextSess: "sess-handler"}
	cfg := testCronConfig()
	cfg.syncExecution = true

	rec := callCronHandler(t, svc, runner, cfg, "handler-1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if len(runner.runs) != 1 {
		t.Fatalf("runs = %#v", runner.runs)
	}
	if runner.runs[0].userID != "alice" {
		t.Fatalf("userID = %q, want alice", runner.runs[0].userID)
	}
	if svc.update == nil {
		t.Fatal("expected UpdateCron after sync handler")
	}

	var resp cronResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "OK" {
		t.Fatalf("response = %#v", resp)
	}
}

func TestCronHandler_archivedCronSkipsRun(t *testing.T) {
	svc := &fakeScheduler{
		cron: &pb.Cron{
			Name:   "crons/archived",
			Owner:  "users/alice",
			Prompt: "noop",
			State:  pb.Cron_STATE_ARCHIVED,
		},
	}
	runner := &fakeRunner{}
	cfg := testCronConfig()
	cfg.syncExecution = true

	rec := callCronHandler(t, svc, runner, cfg, "archived")
	_ = rec
	if len(runner.runs) != 0 {
		t.Fatalf("expected no runs, got %#v", runner.runs)
	}
	if svc.update != nil {
		t.Fatal("expected no UpdateCron for archived cron")
	}
}

func TestSmoke_executeCron_updateCronFailsReturnsNil(t *testing.T) {
	svc := &fakeScheduler{
		cron: &pb.Cron{
			Name:   "crons/persist-fail",
			Owner:  "users/alice",
			Prompt: "hello",
			Type:   pb.Cron_TYPE_CRON,
		},
		updateErr: status.Error(codes.Unavailable, "spanner transient"),
	}
	runner := &fakeRunner{nextSess: "sess-persist"}

	var observedErr error
	cfg := testCronConfig()
	cfg.observer = &testObserver{onFinished: func(_ context.Context, _ *pb.Cron, err error) {
		observedErr = err
	}}

	err := executeCron(context.Background(), svc, runner, cfg, svc.cron, "alice")
	if err != nil {
		t.Fatalf("executeCron should return nil on persist failure, got %v", err)
	}
	if len(runner.runs) != 1 {
		t.Fatalf("agent should have run, runs = %#v", runner.runs)
	}
	if observedErr == nil {
		t.Fatal("observer should see persist error")
	}
}

func TestCronHandler_sync_updateCronFailsReturns200(t *testing.T) {
	svc := &fakeScheduler{
		cron: &pb.Cron{
			Name:   "crons/sync-persist",
			Owner:  "users/alice",
			Prompt: "hello",
			Type:   pb.Cron_TYPE_CRON,
		},
		updateErr: status.Error(codes.Unavailable, "spanner transient"),
	}
	runner := &fakeRunner{nextSess: "sess-sync-persist"}
	cfg := testCronConfig()
	cfg.syncExecution = true

	rec := callCronHandler(t, svc, runner, cfg, "sync-persist")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 even on persist failure", rec.Code)
	}
}

func TestCronHandler_async_smoke(t *testing.T) {
	svc := &fakeScheduler{
		cron: &pb.Cron{
			Name:   "crons/async-1",
			Owner:  "users/alice",
			Prompt: "async hello",
			Type:   pb.Cron_TYPE_CRON,
		},
	}
	done := make(chan struct{}, 1)
	runner := &fakeRunner{nextSess: "sess-async", done: done}
	cfg := testCronConfig()

	rec := callCronHandler(t, svc, runner, cfg, "async-1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	// Wait for the async goroutine to complete.
	<-done

	if len(runner.runs) != 1 {
		t.Fatalf("runs = %#v", runner.runs)
	}
	if runner.runs[0].userID != "alice" {
		t.Fatalf("userID = %q, want alice", runner.runs[0].userID)
	}
}

var _ pb.SchedulerServiceServer = (*fakeScheduler)(nil)
