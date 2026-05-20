// Package lro implements an ADK web sublauncher that exposes HTTP endpoints for
// go.alis.build/lro/v2 long-running operations. Cloud Tasks invokes these routes
// when resuming work after an operation is created or reaches a resume point.
//
// # Role in the ADK web launcher
//
// The ADK web launcher composes one or more sublaunchers, each activated by a CLI
// keyword. This package registers the keyword "lro" and mounts LRO resume routes on
// the shared gorilla/mux router started by google.golang.org/adk/cmd/launcher/web.
//
// # Client resolution
//
// The sublauncher needs a go.alis.build/lro/v2 Client to register HTTP handlers.
// Resolution happens in SetupSubrouters, in this order:
//
//  1. Injected client — if WithLROClient was passed to NewLauncher, that client is used.
//  2. Environment client — otherwise lro.NewFromEnv is called with the service ID from
//     WithServiceID or the -service_id CLI flag.
//
// At least one of injection or service ID must be configured; otherwise SetupSubrouters
// returns an error.
//
// Injecting a client is preferred when the host application already constructs a single
// shared LRO client (for example in a tools package) so Spanner, queues, and handlers are
// not initialized twice.
//
// # HTTP routes
//
// Routes are registered via Client.RegisterHTTP on an inner http.ServeMux, then
// mounted under an optional path prefix (default "/"). The
// primary callback pattern is:
//
//	{path_prefix}/resume-operation/{operation_name}
//
// Exact paths and methods are owned by the lro/v2 module; this launcher does not
// define custom endpoints beyond what RegisterHTTP provides.
//
// # Configuration
//
// Options apply when calling NewLauncher and become defaults for CLI flags:
//
//   - WithLROClient — use an existing LRO client (service_id not required).
//   - WithServiceID — LRO module service id for NewFromEnv.
//   - WithPathPrefix — URL prefix for resume routes (default "/").
//
// CLI flags (after the "lro" keyword on the web command line):
//
//   - -path_prefix — same as WithPathPrefix.
//   - -service_id — same as WithServiceID; required unless a client was injected.
//
// # Usage
//
// Programmatic defaults:
//
//	web.NewLauncher(
//	    lro.NewLauncher(
//	        lro.WithServiceID("my-service"),
//	        lro.WithPathPrefix("/api"),
//	    ),
//	)
//
// Shared client (no service_id needed):
//
//	var client *lro.Client // constructed elsewhere
//	web.NewLauncher(lro.NewLauncher(lro.WithLROClient(client)))
//
// CLI example (service id via flag):
//
//	adk web --port 8080 lro -service_id=my-service
//
// CLI example (service id pre-set via option; flag can still override):
//
//	adk web --port 8080 lro
//
// # Accessing the client after setup
//
// LROClient on the concrete launcher value returned from NewLauncher returns the
// resolved client after SetupSubrouters has run. Callers that need the same instance
// for gRPC registration or tool handlers can type-assert to *lroLauncher or retain
// a reference from WithLROClient at construction time.
//
// # Environment
//
// When using NewFromEnv, the host process must provide the environment variables and
// infrastructure expected by go.alis.build/lro/v2 (project, region, Spanner, Cloud
// Tasks queues, etc.). See the lro/v2 module documentation for details.
//
// This package does not register LRO tools, ADK plugins, or agent resume logic; it only
// mounts HTTP callbacks required for asynchronous operation execution.
package lro
