# adk-launchers-go

Go modules that extend [Google ADK](https://google.golang.org/adk) with optional **web sublaunchers**. Each sublauncher plugs into `google.golang.org/adk/cmd/launcher/web` and adds HTTP routes or protocols on top of the standard ADK web server.

Use this repository when you need extra capabilities beyond the core ADK launchers—for example streaming to AG-UI frontends, resuming long-running operations from Cloud Tasks, or running scheduled agent prompts in-process.

## Packages

| Package                            | CLI keyword | Purpose                                                                                                        |
| ---------------------------------- | ----------- | -------------------------------------------------------------------------------------------------------------- |
| [`agui`](./agui) | `agui` | [AG-UI](https://docs.ag-ui.com) SSE endpoint for CopilotKit and other AG-UI clients |
| [`lro`](./lro)   | `lro`  | HTTP resume routes for [go.alis.build/lro/v2](https://pkg.go.dev/go.alis.build/lro/v2) long-running operations |
| [`scheduler`](./scheduler) | `scheduler` | [A2A scheduler](https://pkg.go.dev/go.alis.build/a2a/extension/scheduler) cron JSON-RPC and Cloud Tasks callback (in-process ADK runner) |

## Quick start

Import the sublaunchers you need and pass them to [`web.NewLauncher`](./web):

```go
import (
    schedulerext "go.alis.build/a2a/extension/scheduler"
    schedulerservice "go.alis.build/a2a/extension/scheduler/service"

    "go.alis.build/adk/launchers/agui"
    "go.alis.build/adk/launchers/lro"
    "go.alis.build/adk/launchers/scheduler"
    launchersweb "go.alis.build/adk/launchers/web"
    "go.alis.build/iam/v3"
    weblauncher "google.golang.org/adk/cmd/launcher/web"
)

// Host constructs SchedulerService (Spanner, Cloud Tasks, etc.) — see scheduler/doc.go.
schedSvc, err := schedulerservice.NewSchedulerService(ctx, &schedulerservice.SchedulerServiceConfig{ /* ... */ })
if err != nil {
    log.Fatal(err)
}

web := launchersweb.NewLauncher(
    weblauncher.NewLauncher(),
    agui.NewLauncher("my-agent", agui.WithCORS(agui.CORSConfig{
        AllowedOrigins: []string{"http://localhost:3000"},
    })),
    lro.NewLauncher(lro.WithServiceID("my-service")),
    scheduler.NewLauncher(schedSvc, "my-agent",
        scheduler.WithCronIdentity(&iam.Identity{
            ID:    "alis-build@my-project.iam.gserviceaccount.com",
            Email: "alis-build@my-project.iam.gserviceaccount.com",
            Type:  iam.ServiceAccount,
        }),
    ),
)

// Register scheduler gRPC on your server (not done by the sublauncher).
schedulerext.RegisterGRPC(grpcServer, schedSvc)
```

At runtime, activate sublaunchers by keyword on the `adk web` command line, for example:

```bash
adk web --port 8080 agui lro scheduler -service_id=my-service -app_name=my-agent
```

Importing [`web`](./web) (and the scheduler sublauncher) pulls in `go.alis.build/mux`, which requires `ALIS_OS_PROJECT` and `IDENTITY_SERVICE_URL` at process start.

## Testing

```bash
go test ./...
```

The scheduler package imports `go.alis.build/mux`; set `ALIS_OS_PROJECT` and `IDENTITY_SERVICE_URL` when running tests (see [.vscode/settings.json](./.vscode/settings.json) for local IDE defaults).

## Requirements

- Go 1.26+
- `google.golang.org/adk` (see [go.mod](./go.mod) for the pinned version)

## License

Apache 2.0 — see [LICENSE](./LICENSE).
