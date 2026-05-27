package scheduler

import (
	"flag"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/gorilla/mux"
	schedulerext "go.alis.build/a2a/extension/scheduler"
	schedjsonrpc "go.alis.build/a2a/extension/scheduler/jsonrpc"
	schedulerservice "go.alis.build/a2a/extension/scheduler/service"
	"go.alis.build/adk/launchers/internal/adkrun"
	"go.alis.build/adk/launchers/internal/launcherutils"
	launchersweb "go.alis.build/adk/launchers/web"
	"go.alis.build/iam/v3"
	alismux "go.alis.build/mux"
	adklauncher "google.golang.org/adk/cmd/launcher"
	adkweb "google.golang.org/adk/cmd/launcher/web"
	"google.golang.org/grpc"
)

// Launcher is the public surface of [NewLauncher].
//
// Hosts compose it with [go.alis.build/adk/launchers/web.NewLauncher]. For gRPC, use
// [WithGRPCRegistrar] or call [Launcher.SchedulerService] with schedulerext.RegisterGRPC.
type Launcher interface {
	adkweb.Sublauncher
	launchersweb.HostRouteSetup
	// SchedulerService returns the extension service for host gRPC registration.
	SchedulerService() *schedulerservice.SchedulerService
}

// Option configures optional [schedulerLauncher] settings applied in [NewLauncher].
type Option func(*schedulerLauncher)

// WithCronIdentity sets the IAM principal used for SchedulerService GetCron and UpdateCron
// inside the cron handler.
//
// When unset, [defaultSystemIdentity] is used (alis-build@{ALIS_OS_PROJECT}.iam.gserviceaccount.com).
// The host constructs and owns the [*iam.Identity] (service account, test double, etc.).
func WithCronIdentity(identity *iam.Identity) Option {
	return func(l *schedulerLauncher) {
		l.cronCfg.systemIdentity = identity
	}
}

// WithJSONRPCOptions forwards options to the extension JSON-RPC handler (for example CORS).
func WithJSONRPCOptions(opts ...schedjsonrpc.JSONRPCHandlerOption) Option {
	return func(l *schedulerLauncher) {
		l.jsonrpcOpts = append(l.jsonrpcOpts, opts...)
	}
}

// WithSynchronousExecution waits for the ADK run to finish before returning HTTP 200.
// Agent failures are returned as 500 so Cloud Tasks may retry. Cron persist failures
// (UpdateCron) are logged and return 200 to prevent duplicate agent execution.
// Default is async (extension behavior).
func WithSynchronousExecution(sync bool) Option {
	return func(l *schedulerLauncher) {
		l.cronCfg.syncExecution = sync
	}
}

// WithCronObserver registers callbacks for cron execution lifecycle events.
func WithCronObserver(observer CronObserver) Option {
	return func(l *schedulerLauncher) {
		l.cronCfg.observer = observer
	}
}

// WithGRPCRegistrar registers SchedulerService on reg during [SetupHostRoutes].
//
// Pass the host's grpc.Server (it implements [grpc.ServiceRegistrar]). The host must
// still mount that server on go.alis.build/mux (for example hostmux.HandleGRPC) and
// add [schedulerservice.UnaryServerInterceptor] (iam.UnaryInterceptor) so that caller
// identity is available to service methods. Do not also call schedulerext.RegisterGRPC
// for the same service instance.
func WithGRPCRegistrar(reg grpc.ServiceRegistrar) Option {
	if reg == nil {
		panic("scheduler: WithGRPCRegistrar requires a non-nil ServiceRegistrar")
	}
	return func(l *schedulerLauncher) {
		l.grpcRegistrar = reg
	}
}

// schedulerLauncher implements [Launcher] and mounts scheduler routes on the host mux.
type schedulerLauncher struct {
	// flags holds CLI flags for the "scheduler" sublauncher keyword.
	flags *flag.FlagSet
	// appName is the ADK application name passed to [adkrun.NewRuntime].
	appName string
	// service is the extension SchedulerService (Spanner + Cloud Tasks); owned by the host.
	service *schedulerservice.SchedulerService
	// cronCfg is passed to cronHandler for identity, sync mode, and observers.
	cronCfg cronConfig
	// jsonrpcOpts are forwarded to schedulerext.RegisterHTTP for the JSON-RPC surface.
	jsonrpcOpts []schedjsonrpc.JSONRPCHandlerOption
	// grpcRegistrar when set triggers schedulerext.RegisterGRPC in [SetupHostRoutes].
	grpcRegistrar grpc.ServiceRegistrar

	// setupOnce ensures mountHostRoutes runs at most once per launcher instance.
	setupOnce sync.Once
	// setupErr stores the first error from mountHostRoutes.
	setupErr error
}

var (
	_ Launcher                    = (*schedulerLauncher)(nil)
	_ adkweb.Sublauncher          = (*schedulerLauncher)(nil)
	_ launchersweb.HostRouteSetup = (*schedulerLauncher)(nil)
)

// NewLauncher returns a scheduler sublauncher bound to svc and appName.
//
// svc must be constructed by the host (Spanner, Cloud Tasks, TargetUrl, etc.).
// appName is the ADK app to run when a cron fires (-app_name flag overrides at CLI).
//
// Optional gRPC: [WithGRPCRegistrar] during [SetupHostRoutes], or register manually:
//
//	schedulerext.RegisterGRPC(grpcServer, l.SchedulerService())
//
// The host still calls hostmux.HandleGRPC(grpcServer) once per process.
func NewLauncher(appName string, svc *schedulerservice.SchedulerService, opts ...Option) Launcher {
	l := &schedulerLauncher{service: svc, appName: appName}
	for _, opt := range opts {
		opt(l)
	}

	fs := flag.NewFlagSet("scheduler", flag.ContinueOnError)
	fs.StringVar(&l.appName, "app_name", l.appName, "ADK app name to run when a cron fires")
	l.flags = fs

	return l
}

// SchedulerService returns the extension service for host gRPC registration.
func (l *schedulerLauncher) SchedulerService() *schedulerservice.SchedulerService {
	return l.service
}

// Keyword returns the CLI sublauncher keyword ("scheduler").
func (l *schedulerLauncher) Keyword() string { return "scheduler" }

// Parse parses scheduler-specific CLI flags and returns remaining args.
func (l *schedulerLauncher) Parse(args []string) ([]string, error) {
	if err := l.flags.Parse(args); err != nil || !l.flags.Parsed() {
		return nil, fmt.Errorf("scheduler: parse flags: %w", err)
	}
	return l.flags.Args(), nil
}

// CommandLineSyntax returns formatted flag usage for help output.
func (l *schedulerLauncher) CommandLineSyntax() string {
	return launcherutils.FormatFlagUsage(l.flags)
}

// SimpleDescription returns a one-line summary for the web launcher help text.
func (l *schedulerLauncher) SimpleDescription() string {
	return "scheduler JSON-RPC and ADK cron callback"
}

// SetupSubrouters is a no-op; all routes are registered on the host mux via [SetupHostRoutes].
func (l *schedulerLauncher) SetupSubrouters(_ *mux.Router, _ *adklauncher.Config) error {
	return nil
}

// UserMessage prints scheduler endpoint URLs when the web server starts.
func (l *schedulerLauncher) UserMessage(webURL string, printer func(v ...any)) {
	printer(fmt.Sprintf("       scheduler:  JSON-RPC %s%s", webURL, schedulerext.JSONRPCPath))
	printer(fmt.Sprintf("       scheduler:  cron handler %s%s", webURL, schedulerext.HandlerPath))
}

// SetupHostRoutes registers JSON-RPC and the cron execution handler on go.alis.build/mux.
// Safe to call multiple times; mounting runs once per launcher instance.
func (l *schedulerLauncher) SetupHostRoutes(config *adklauncher.Config) error {
	l.setupOnce.Do(func() {
		l.setupErr = l.mountHostRoutes(config)
	})
	return l.setupErr
}

// mountHostRoutes builds the in-process ADK runtime and registers HTTP routes.
func (l *schedulerLauncher) mountHostRoutes(config *adklauncher.Config) error {
	if l.service == nil {
		return fmt.Errorf("scheduler: service is nil")
	}
	if l.appName == "" {
		return fmt.Errorf("scheduler: app name is required")
	}

	systemIdentity := l.cronCfg.resolveSystemIdentity()
	if systemIdentity == nil {
		return fmt.Errorf("scheduler: system identity required (use WithCronIdentity or set ALIS_OS_PROJECT)")
	}

	rt, err := adkrun.NewRuntime(config, l.appName)
	if err != nil {
		return fmt.Errorf("scheduler: %w", err)
	}

	l.registerGRPC()

	// WithoutHandler skips the stock A2A loopback handler; we mount our ADK handler below.
	httpOpts := []schedulerext.HTTPOption{schedulerext.WithoutHandler()}
	if len(l.jsonrpcOpts) > 0 {
		httpOpts = append(httpOpts, schedulerext.WithJSONRPCOptions(l.jsonrpcOpts...))
	}
	schedulerext.RegisterHTTP(muxRegistrar{}, l.service, httpOpts...)

	alismux.SystemPost(schedulerext.HandlerPath, cronHandler(l.service, rt, systemIdentity, &l.cronCfg))
	return nil
}

const schedulerGRPCServiceName = "alis.a2a.extension.scheduler.v1.SchedulerService"

// serviceInfoProvider is satisfied by *grpc.Server but not by grpc.ServiceRegistrar,
// allowing a pre-registration check without importing a concrete type.
type serviceInfoProvider interface {
	GetServiceInfo() map[string]grpc.ServiceInfo
}

// registerGRPC wires SchedulerService into grpcRegistrar when [WithGRPCRegistrar] was used.
func (l *schedulerLauncher) registerGRPC() {
	if l.grpcRegistrar == nil {
		return
	}
	if si, ok := l.grpcRegistrar.(serviceInfoProvider); ok {
		if _, exists := si.GetServiceInfo()[schedulerGRPCServiceName]; exists {
			return
		}
	}
	schedulerext.RegisterGRPC(l.grpcRegistrar, l.service)
}

// muxRegistrar adapts schedulerext.RegisterHTTP to go.alis.build/mux route registration.
type muxRegistrar struct{}

// Handle registers extension HTTP patterns on the host mux.
// JSON-RPC POST is authenticated; OPTIONS is unauthenticated (CORS preflight).
func (muxRegistrar) Handle(pattern string, handler http.Handler) {
	switch {
	case strings.HasPrefix(pattern, "POST "+schedulerext.JSONRPCPath):
		alismux.AuthenticatedHandleHTTP(pattern, handler)
	case strings.HasPrefix(pattern, "OPTIONS "+schedulerext.JSONRPCPath):
		alismux.HandleHTTP(pattern, handler)
	default:
		panic("scheduler: unexpected route " + pattern)
	}
}
