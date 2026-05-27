// Package launchers is the root of go.alis.build/adk/launchers, a collection of
// optional ADK web sublaunchers that extend google.golang.org/adk with extra HTTP
// endpoints and protocols. Import the subpackages you need; this package has no
// API of its own.
//
// Each sublauncher implements [google.golang.org/adk/cmd/launcher/web.Sublauncher],
// registers a CLI keyword, and mounts routes on the shared gorilla/mux router
// started by adk web. Compose one or more sublaunchers when building your agent
// server's web launcher.
//
// # Subpackages
//
//   - [agui] — AG-UI protocol SSE streaming for CopilotKit and other AG-UI clients
//     (keyword: agui).
//   - [lro] — HTTP resume callbacks for go.alis.build/lro/v2 long-running operations,
//     invoked by Cloud Tasks (keyword: lro).
//   - [scheduler] — A2A scheduler extension HTTP (cron JSON-RPC and execution callback;
//     in-process ADK cron execution; keyword: scheduler).
//
// Shared implementation helpers live under internal/launcherutils and are not part of
// the public import path.
//
// Configuration, routes, and usage examples for each sublauncher are documented in
// that package's doc.go.
//
// # Usage
//
// Import subpackages and pass them to [google.golang.org/adk/cmd/launcher/web.NewLauncher]:
//
//	import (
//	    "go.alis.build/adk/launchers/agui"
//	    "go.alis.build/adk/launchers/lro"
//	    "go.alis.build/adk/launchers/scheduler"
//	    launchersweb "go.alis.build/adk/launchers/web"
//	    weblauncher "google.golang.org/adk/cmd/launcher/web"
//	)
//
//	web := launchersweb.NewLauncher(
//	    weblauncher.NewLauncher(),
//	    agui.NewLauncher("my-agent"),
//	    lro.NewLauncher(lro.WithServiceID("my-service")),
//	    scheduler.NewLauncher("my-agent", schedSvc),
//	)
//
// At runtime, enable sublaunchers by keyword on the adk web command line:
//
//	adk web --port 8080 agui lro scheduler -service_id=my-service -app_name=my-agent
package launchers
