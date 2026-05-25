// Package web provides an ADK web launcher built on go.alis.build/mux.
//
// ADK sublaunchers register routes on a shared gorilla/mux subrouter via [Sublauncher].
// The subrouter is mounted at "/" on the process-wide host mux after all sublaunchers
// have run setup.
//
// Sublaunchers that need host-level features (IAM-authenticated routes, gRPC, and
// similar) may optionally implement [HostRouteSetup]. SetupHostRoutes runs before
// SetupSubrouters so specific host patterns take precedence over the gorilla
// catch-all. ADK built-in sublaunchers do not implement HostRouteSetup and are
// unaffected.
//
// # Launcher usage
//
//	import (
//	    "go.alis.build/adk/launchers/agui"
//	    "go.alis.build/adk/launchers/web"
//	)
//
//	launcher := web.NewLauncher(agui.NewLauncher("my-agent"))
//
// At runtime:
//
//	adk web --port 8080 agui
//
// Importing go.alis.build/mux requires IDENTITY_SERVICE_URL and ALIS_OS_PROJECT
// to be set at process start.
//
// # HostRouteSetup examples
//
// Implement [HostRouteSetup] on the same type that implements [Sublauncher].
// Register host routes in SetupHostRoutes; keep SetupSubrouters for gorilla-only
// paths (for example unauthenticated local dev) or leave it empty when all routes
// live on the host mux.
//
// Authenticated GET (IAM cookies or bearer token; identity in request context):
//
//	import (
//	    "net/http"
//
//	    "go.alis.build/iam/v3"
//	    hostmux "go.alis.build/mux"
//	    "google.golang.org/adk/cmd/launcher"
//	)
//
//	func (l *myLauncher) SetupHostRoutes(config *launcher.Config) error {
//	    hostmux.AuthenticatedGet("/my-agent/status", func(w http.ResponseWriter, r *http.Request) error {
//	        identity := iam.MustFromContext(r.Context())
//	        _, err := w.Write([]byte("hello, " + identity.Email))
//	        return err
//	    })
//	    return nil
//	}
//
// Authenticated POST or an existing http.Handler (for example SSE):
//
//	func (l *myLauncher) SetupHostRoutes(config *launcher.Config) error {
//	    hostmux.AuthenticatedHandleHTTP("POST /my-agent/run_sse", l.runSSEHandler())
//	    return nil
//	}
//
// CORS preflight for a browser client must stay unauthenticated; register OPTIONS
// on the host mux without the Authenticated* helpers:
//
//	func (l *myLauncher) SetupHostRoutes(config *launcher.Config) error {
//	    hostmux.AuthenticatedHandleHTTP("POST /my-agent/run_sse", l.handler())
//	    hostmux.Options("/my-agent/run_sse", func(w http.ResponseWriter, r *http.Request) error {
//	        w.WriteHeader(http.StatusNoContent)
//	        return nil
//	    })
//	    return nil
//	}
//
// Native gRPC (HTTP/2) on the same listener. Only one broad gRPC fallback (POST /)
// is allowed per process—register from a single sublauncher or from main, not from
// every sublauncher:
//
//	func (l *myLauncher) SetupHostRoutes(config *launcher.Config) error {
//	    hostmux.HandleGRPC(l.grpcServer) // grpc.Server implements http.Handler
//	    return nil
//	}
//
// gRPC-Web for browser clients with the same auth middleware as other user routes:
//
//	func (l *myLauncher) SetupHostRoutes(config *launcher.Config) error {
//	    hostmux.AuthenticatedHandleGRPCWeb(l.grpcWebServer)
//	    return nil
//	}
//
// After host setup, SetupSubrouters still runs for gorilla routes. Example split:
// production uses host mux (auth); local dev uses gorilla only:
//
//	func (l *myLauncher) SetupHostRoutes(config *launcher.Config) error {
//	    if !l.requireAuth {
//	        return nil
//	    }
//	    hostmux.AuthenticatedHandleHTTP("POST /my-agent/run_sse", l.handler())
//	    return nil
//	}
//
//	func (l *myLauncher) SetupSubrouters(router *mux.Router, config *launcher.Config) error {
//	    if l.requireAuth {
//	        return nil // routes already on host mux
//	    }
//	    router.Handle("/my-agent/run_sse", l.handler()).Methods(http.MethodPost)
//	    return nil
//	}
package web
