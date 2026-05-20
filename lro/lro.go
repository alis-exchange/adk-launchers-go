package lro

import (
	"context"
	"flag"
	"fmt"
	"net/http"

	"github.com/gorilla/mux"
	"go.alis.build/adk/launchers/launcherutils"
	lro "go.alis.build/lro/v2"
	"google.golang.org/adk/cmd/launcher"
	"google.golang.org/adk/cmd/launcher/web"
)

// resumeOperationPathPrefix is the path segment registered by lro/v2 RegisterHTTP,
// relative to the configured path_prefix. Used only for startup log messages.
const resumeOperationPathPrefix = "/resume-operation/"

// Option configures an [lroLauncher] before it is returned from [NewLauncher].
// Options run in order; later options override earlier ones for the same field.
type Option func(*lroLauncher)

// WithLROClient injects a pre-built [go.alis.build/lro/v2.Client].
//
// When set, -service_id and [WithServiceID] are not required because [resolveClient]
// returns this client without calling [go.alis.build/lro/v2.NewFromEnv].
func WithLROClient(client *lro.Client) Option {
	return func(l *lroLauncher) {
		l.client = client
	}
}

// WithServiceID sets the LRO module service identifier passed to
// [go.alis.build/lro/v2.NewFromEnv] when no client is injected.
//
// The value must match the service id used when the LRO module was provisioned
// (Spanner tables, Cloud Tasks queues, etc.).
func WithServiceID(serviceID string) Option {
	return func(l *lroLauncher) {
		l.config.serviceID = serviceID
	}
}

// WithPathPrefix sets the HTTP path prefix under which resume-operation routes are
// served. An empty string is treated as "/". A value without a leading slash gets one
// prepended; trailing slashes are removed.
func WithPathPrefix(pathPrefix string) Option {
	return func(l *lroLauncher) {
		l.config.pathPrefix = launcherutils.NormalizePathPrefix(pathPrefix)
	}
}

// lroConfig holds launcher settings shared by programmatic options and CLI flags.
type lroConfig struct {
	// pathPrefix is the URL prefix for LRO HTTP routes (e.g. "/" or "/api").
	pathPrefix string
	// serviceID is the lro/v2 module id for NewFromEnv; ignored when client is injected.
	serviceID string
}

// lroLauncher implements [web.Sublauncher] for LRO HTTP resume endpoints.
type lroLauncher struct {
	flags  *flag.FlagSet
	config *lroConfig
	// client is either injected via [WithLROClient] or resolved in [SetupSubrouters].
	client *lro.Client
}

// Compile-time check that lroLauncher satisfies the ADK web sublauncher contract.
var _ web.Sublauncher = (*lroLauncher)(nil)

// NewLauncher builds an LRO web sublauncher for use with [web.NewLauncher].
//
// Configure defaults via [Option] values; callers can still override path_prefix and
// service_id on the command line when the "lro" keyword is parsed.
//
// Returns [web.Sublauncher]; use a type assertion to [*lroLauncher] if [LROClient]
// is needed after setup.
func NewLauncher(opts ...Option) web.Sublauncher {
	cfg := &lroConfig{
		pathPrefix: "/",
	}

	l := &lroLauncher{
		config: cfg,
	}

	for _, opt := range opts {
		opt(l)
	}

	fs := flag.NewFlagSet("lro", flag.ContinueOnError)
	fs.StringVar(&cfg.pathPrefix, "path_prefix", cfg.pathPrefix,
		"Path prefix for LRO resume-operation HTTP routes.")
	fs.StringVar(&cfg.serviceID, "service_id", cfg.serviceID,
		"LRO module service id for NewFromEnv (required unless an LRO client is injected).")
	l.flags = fs

	return l
}

// LROClient returns the [go.alis.build/lro/v2.Client] resolved during [SetupSubrouters],
// or nil if setup has not run yet.
//
// Useful when the host application needs the same client instance for gRPC
// [go.alis.build/lro/v2.Client.RegisterGRPC] or tool handlers without a second
// NewFromEnv call.
func (l *lroLauncher) LROClient() *lro.Client {
	return l.client
}

// Keyword implements [web.Sublauncher]. It is the CLI token that activates this sublauncher.
func (l *lroLauncher) Keyword() string {
	return "lro"
}

// Parse implements [web.Sublauncher]. It parses lro-specific flags and returns any
// remaining arguments for the parent web launcher.
func (l *lroLauncher) Parse(args []string) ([]string, error) {
	if err := l.flags.Parse(args); err != nil || !l.flags.Parsed() {
		return nil, fmt.Errorf("failed to parse lro flags: %v", err)
	}

	// Re-normalize after CLI parse in case path_prefix was changed on the command line.
	l.config.pathPrefix = launcherutils.NormalizePathPrefix(l.config.pathPrefix)

	return l.flags.Args(), nil
}

// CommandLineSyntax implements [web.Sublauncher]. It returns flag help for the lro keyword.
func (l *lroLauncher) CommandLineSyntax() string {
	return launcherutils.FormatFlagUsage(l.flags)
}

// SimpleDescription implements [web.Sublauncher]. It is shown in the web launcher help text.
func (l *lroLauncher) SimpleDescription() string {
	return "mounts alis.lro.v2 resume-operation HTTP routes for Cloud Tasks callbacks"
}

// SetupSubrouters implements [web.Sublauncher]. It resolves the LRO client and mounts
// [go.alis.build/lro/v2.Client.RegisterHTTP] routes on the shared ADK web router.
//
// The launcher [Config] parameter is intentionally unused; LRO HTTP does not require
// session or agent services at mount time.
func (l *lroLauncher) SetupSubrouters(router *mux.Router, _ *launcher.Config) error {
	ctx := context.Background()

	client, err := l.resolveClient(ctx)
	if err != nil {
		return err
	}
	// Cache for [LROClient] and so repeated setup (if any) reuses the same instance.
	l.client = client

	return l.mountLROHTTP(router, client)
}

// UserMessage implements [web.Sublauncher]. It prints the base URL for resume routes
// when the web server starts.
func (l *lroLauncher) UserMessage(webURL string, printer func(v ...any)) {
	prefix := l.config.pathPrefix
	if prefix == "" {
		prefix = "/"
	}
	printer(fmt.Sprintf("       lro:  LRO resume routes under %s%s{operation}",
		webURL, prefix+resumeOperationPathPrefix))
}

// resolveClient returns the injected client or constructs one via NewFromEnv.
func (l *lroLauncher) resolveClient(ctx context.Context) (*lro.Client, error) {
	if l.client != nil {
		return l.client, nil
	}
	if l.config.serviceID == "" {
		return nil, fmt.Errorf("lro: service_id is required (pass -service_id or WithServiceID/WithLROClient)")
	}
	client, err := lro.NewFromEnv(ctx, l.config.serviceID)
	if err != nil {
		return nil, fmt.Errorf("lro client: %w", err)
	}
	return client, nil
}

// mountLROHTTP registers lro/v2 HTTP handlers on an inner ServeMux and attaches it to
// the gorilla subrouter.
//
// Gorilla's PathPrefix subrouter strips path_prefix from the request URL before the
// inner mux sees it, so RegisterHTTP routes (which are root-absolute, e.g.
// /resume-operation/...) match correctly under a non-root prefix.
func (l *lroLauncher) mountLROHTTP(router *mux.Router, client *lro.Client) error {
	inner := http.NewServeMux()
	client.RegisterHTTP(inner)

	subrouter := router
	if l.config.pathPrefix != "" && l.config.pathPrefix != "/" {
		subrouter = router.PathPrefix(l.config.pathPrefix).Subrouter()
	}
	// Catch-all under the prefix delegates to the lro/v2 ServeMux.
	subrouter.PathPrefix("/").Handler(inner)
	return nil
}
