package scheduler

import (
	"testing"

	schedulerservice "go.alis.build/a2a/extension/scheduler/service"
	"google.golang.org/grpc"
)

func TestWithGRPCRegistrar_registersService(t *testing.T) {
	gs := grpc.NewServer()
	svc := &schedulerservice.SchedulerService{}
	l := NewLauncher("my.agent", svc, WithGRPCRegistrar(gs)).(*schedulerLauncher)

	l.registerGRPC()

	info := gs.GetServiceInfo()
	if _, ok := info[schedulerGRPCServiceName]; !ok {
		t.Fatalf("expected %q in registered services, got %v", schedulerGRPCServiceName, info)
	}
}

func TestWithGRPCRegistrar_nilRegistrarIsNoOp(t *testing.T) {
	svc := &schedulerservice.SchedulerService{}
	l := NewLauncher("my.agent", svc).(*schedulerLauncher)

	l.registerGRPC() // must not panic
}

func TestWithGRPCRegistrar_skipsDoubleRegistration(t *testing.T) {
	gs := grpc.NewServer()
	svc := &schedulerservice.SchedulerService{}
	l := NewLauncher("my.agent", svc, WithGRPCRegistrar(gs)).(*schedulerLauncher)

	l.registerGRPC()
	l.registerGRPC() // must not fatal on duplicate
}

func TestWithGRPCRegistrar_nilPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil ServiceRegistrar")
		}
	}()
	WithGRPCRegistrar(nil)
}
