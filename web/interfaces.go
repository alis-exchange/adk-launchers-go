package web

import (
	"google.golang.org/adk/cmd/launcher"
	adkweb "google.golang.org/adk/cmd/launcher/web"
)

// Sublauncher is the ADK web sublauncher contract. Implementations register HTTP
// routes on the shared gorilla/mux subrouter via SetupSubrouters.
//
// All ADK built-in sublaunchers (api, a2a, webui, and others) satisfy this interface.
type Sublauncher = adkweb.Sublauncher

// HostRouteSetup is an optional extension for sublaunchers that also register routes
// on the process-wide HTTP mux (go.alis.build/mux) before the gorilla subrouter
// catch-all is mounted. Use this for authenticated routes, gRPC, or other host-level
// features that are not available on the gorilla subrouter alone.
//
// Sublaunchers that do not implement HostRouteSetup are unchanged: only
// SetupSubrouters runs. ADK default sublaunchers do not implement this interface.
//
// Do not register the same path in SetupHostRoutes and SetupSubrouters; pick one
// per URL. See package web documentation for AuthenticatedGet and gRPC examples.
type HostRouteSetup interface {
	SetupHostRoutes(config *launcher.Config) error
}
