// Package scheduler is an ADK web sublauncher for the A2A scheduler extension
// (go.alis.build/a2a/extension/scheduler).
//
// It registers two HTTP surfaces on go.alis.build/mux (via [go.alis.build/adk/launchers/web]):
//
//   - JSON-RPC at /alis.a2a.extension.v1.SchedulerService for cron CRUD (authenticated POST + OPTIONS).
//   - Cloud Tasks callback at /alis.a2a.extension.v1.SchedulerService/handler for cron execution.
//
// When Cloud Tasks invokes the handler, this package runs the configured ADK app
// in-process through [go.alis.build/adk/launchers/internal/adkrun] instead of the
// stock extension handler that loops back over A2A gRPC.
//
// # Host responsibilities
//
// The sublauncher does not construct infrastructure. The host must:
//
//   - Build [schedulerservice.SchedulerService] (Spanner, Cloud Tasks queue, TargetUrl, etc.).
//   - Pass the service and ADK app name to [NewLauncher].
//   - Register gRPC: schedulerext.RegisterGRPC(grpcServer, l.SchedulerService()).
//   - Compose the launcher: launchersweb.NewLauncher(..., scheduler.NewLauncher(...)).
//   - Provide [WithCronIdentity] (recommended) or set ALIS_OS_PROJECT for the default SA.
//
// # Execution model
//
// On each cron tick the handler:
//
//  1. Uses a system IAM identity for GetCron and UpdateCron.
//  2. Impersonates the cron owner for ADK runs (user id + cron email).
//  3. Optionally runs initial_prompt once for new recurring crons, then prompt.
//  4. Persists context_id (ADK session id), last_run_time, and archives TYPE_AT crons.
//
// Default HTTP behavior matches the stock extension: return 200 immediately and run
// asynchronously. [WithSynchronousExecution] blocks until the ADK run completes;
// agent failures return 500 (Cloud Tasks may retry), but cron persist failures
// (UpdateCron) are logged and return 200 to prevent duplicate agent execution.
//
// Unlike the stock extension handler, this package applies stricter validation:
// cron prompt and initial_prompt are trimmed before use (whitespace-only is rejected),
// and owner must have an explicit "users/" prefix.
//
// # Options
//
//   - [WithCronIdentity] — system principal for SchedulerService RPCs in the handler.
//   - [WithJSONRPCOptions] — forwarded to the extension JSON-RPC handler (e.g. CORS).
//   - [WithSynchronousExecution] — sync ADK run; 500 on agent failure, 200 on persist failure.
//   - [WithCronObserver] — lifecycle hooks around in-process execution.
//
// # Example
//
//	import (
//	    schedulerext "go.alis.build/a2a/extension/scheduler"
//	    "go.alis.build/adk/launchers/scheduler"
//	    launchersweb "go.alis.build/adk/launchers/web"
//	    "go.alis.build/iam/v3"
//	)
//
//	sched := scheduler.NewLauncher(svc, "my.agent",
//	    scheduler.WithCronIdentity(&iam.Identity{
//	        ID:    "alis-build@my-project.iam.gserviceaccount.com",
//	        Email: "alis-build@my-project.iam.gserviceaccount.com",
//	        Type:  iam.ServiceAccount,
//	    }),
//	)
//	launchersweb.NewLauncher(webapi.NewLauncher(), sched)
//	schedulerext.RegisterGRPC(grpcServer, sched.SchedulerService())
//
// CLI: adk web --port 8080 api scheduler -app_name=my.agent
package scheduler
