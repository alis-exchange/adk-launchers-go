package agui

import (
	"context"
	"iter"
	"net/http/httptest"
	"slices"
	"time"

	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/encoding/sse"
	"google.golang.org/adk/session"
)

var (
	_ session.Session = (*mockSession)(nil)
	_ session.Events  = (*mockEvents)(nil)
	_ session.State   = (*mockState)(nil)
)

// mockState implements session.State with a real backing map.
type mockState struct {
	data map[string]any
}

func (s *mockState) Get(key string) (any, error) {
	v, ok := s.data[key]
	if !ok {
		return nil, session.ErrStateKeyNotExist
	}
	return v, nil
}

func (s *mockState) Set(key string, val any) error {
	s.data[key] = val
	return nil
}

func (s *mockState) All() iter.Seq2[string, any] {
	return func(yield func(string, any) bool) {
		for k, v := range s.data {
			if !yield(k, v) {
				return
			}
		}
	}
}

// mockEvents implements session.Events with an optional event slice.
type mockEvents struct {
	events []*session.Event
}

func (e *mockEvents) All() iter.Seq[*session.Event] {
	return slices.Values(e.events)
}

func (e *mockEvents) Len() int              { return len(e.events) }
func (e *mockEvents) At(i int) *session.Event { return e.events[i] }

// mockSession implements session.Session with configurable fields.
type mockSession struct {
	id     string
	state  map[string]any
	events []*session.Event
}

func (s *mockSession) ID() string      { return s.id }
func (s *mockSession) AppName() string { return "test-app" }
func (s *mockSession) UserID() string  { return "test-user" }
func (s *mockSession) State() session.State {
	if s.state == nil {
		return &mockState{data: map[string]any{}}
	}
	return &mockState{data: s.state}
}
func (s *mockSession) Events() session.Events { return &mockEvents{events: s.events} }
func (s *mockSession) LastUpdateTime() time.Time {
	return time.Now()
}

// newTestEmitter creates an emitter backed by an httptest.Recorder so we can
// inspect the SSE output after processing.
func newTestEmitter() (*emitter, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	return newEmitter(context.Background(), rec, sse.NewSSEWriter(), nil, nil), rec
}

// newTestLauncher creates a minimal aguiLauncher for testing processEvent.
func newTestLauncher(appName string, svc ...session.Service) *aguiLauncher {
	l := &aguiLauncher{config: &AGUIConfig{appName: appName}}
	if len(svc) > 0 {
		l.sessionService = svc[0]
	}
	return l
}

// failAppendService wraps a session.Service and makes AppendEvent fail with a
// configurable error. Used to test persist-failure paths without affecting reads.
type failAppendService struct {
	session.Service
	appendErr error
}

func (s *failAppendService) AppendEvent(_ context.Context, _ session.Session, _ *session.Event) error {
	return s.appendErr
}
