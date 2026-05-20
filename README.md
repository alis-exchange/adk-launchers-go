# adk-launchers-go

Go modules that extend [Google ADK](https://google.golang.org/adk) with optional **web sublaunchers**. Each sublauncher plugs into `google.golang.org/adk/cmd/launcher/web` and adds HTTP routes or protocols on top of the standard ADK web server.

Use this repository when you need extra capabilities beyond the core ADK launchers—for example streaming to AG-UI frontends or resuming long-running operations from Cloud Tasks.

## Packages

| Package                            | CLI keyword | Purpose                                                                                                        |
| ---------------------------------- | ----------- | -------------------------------------------------------------------------------------------------------------- |
| [`agui`](./agui) | `agui` | [AG-UI](https://docs.ag-ui.com) SSE endpoint for CopilotKit and other AG-UI clients |
| [`lro`](./lro)   | `lro`  | HTTP resume routes for [go.alis.build/lro/v2](https://pkg.go.dev/go.alis.build/lro/v2) long-running operations |

## Quick start

Import the sublaunchers you need and pass them to `web.NewLauncher`:

```go
import (
    "go.alis.build/adk/launchers/agui"
    "go.alis.build/adk/launchers/lro"
    weblauncher "google.golang.org/adk/cmd/launcher/web"
)

web := weblauncher.NewLauncher(
    agui.NewLauncher("my-agent", agui.WithCORS(agui.CORSConfig{
        AllowedOrigins: []string{"http://localhost:3000"},
    })),
    lro.NewLauncher(lro.WithServiceID("my-service")),
)
```

At runtime, activate sublaunchers by keyword on the `adk web` command line, for example:

```bash
adk web --port 8080 agui lro -service_id=my-service
```

## Requirements

- Go 1.26+
- `google.golang.org/adk` (see [go.mod](./go.mod) for the pinned version)

## License

Apache 2.0 — see [LICENSE](./LICENSE).
