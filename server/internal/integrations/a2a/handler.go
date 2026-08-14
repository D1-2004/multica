package a2aintegration

import (
	"context"
	"fmt"
	"iter"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
)

const a2aStreamKeepAliveInterval = 15 * time.Second

// Port is the narrow application boundary required by the inbound A2A wire adapter.
// Implementations can obtain the authenticated caller through PrincipalFromContext.
type Port interface {
	SendMessage(context.Context, *a2a.SendMessageRequest) (a2a.SendMessageResult, error)
	GetTask(context.Context, *a2a.GetTaskRequest) (*a2a.Task, error)
}

// TaskLister can be implemented by a Port that supports A2A ListTasks.
type TaskLister interface {
	ListTasks(context.Context, *a2a.ListTasksRequest) (*a2a.ListTasksResponse, error)
}

// TaskCanceler can be implemented by a Port that supports A2A CancelTask.
type TaskCanceler interface {
	CancelTask(context.Context, *a2a.CancelTaskRequest) (*a2a.Task, error)
}

// TaskStreamer is implemented by a Port that persists and replays ordered
// task events. The wire adapter deliberately owns no in-memory event queue.
type TaskStreamer interface {
	SubscribeToTask(context.Context, *a2a.SubscribeToTaskRequest) iter.Seq2[a2a.Event, error]
	SendStreamingMessage(context.Context, *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error]
}

// PushConfigManager is implemented by a Port that durably stores per-task
// callback configuration and delivers task events through an outbox worker.
type PushConfigManager interface {
	GetTaskPushConfig(context.Context, *a2a.GetTaskPushConfigRequest) (*a2a.PushConfig, error)
	ListTaskPushConfigs(context.Context, *a2a.ListTaskPushConfigRequest) (*a2a.ListTaskPushConfigResponse, error)
	CreateTaskPushConfig(context.Context, *a2a.PushConfig) (*a2a.PushConfig, error)
	DeleteTaskPushConfig(context.Context, *a2a.DeleteTaskPushConfigRequest) error
}

type requestHandler struct {
	port Port
}

var _ a2asrv.RequestHandler = (*requestHandler)(nil)

// HandlerOption customizes the protocol handler without adding persistence to the wire layer.
type HandlerOption func(*handlerConfig)

type handlerConfig struct {
	logger       *slog.Logger
	interceptors []a2asrv.CallInterceptor
}

// WithLogger sets the request-scoped logger used by the official SDK wrapper.
func WithLogger(logger *slog.Logger) HandlerOption {
	return func(config *handlerConfig) {
		config.logger = logger
	}
}

// WithCallInterceptors appends application interceptors after the mandatory version guard.
func WithCallInterceptors(interceptors ...a2asrv.CallInterceptor) HandlerOption {
	return func(config *handlerConfig) {
		config.interceptors = append(config.interceptors, interceptors...)
	}
}

// NewRequestHandler adapts a Port to the official transport-agnostic A2A server interface.
// It intentionally does not call a2asrv.NewHandler, which would allocate in-memory task state.
func NewRequestHandler(port Port, options ...HandlerOption) a2asrv.RequestHandler {
	if port == nil {
		panic("A2A Port is required")
	}

	config := handlerConfig{}
	for _, option := range options {
		option(&config)
	}

	interceptors := make([]a2asrv.CallInterceptor, 0, 2+len(config.interceptors))
	interceptors = append(interceptors, VersionInterceptor{})
	interceptors = append(interceptors, AuthorizationInterceptor{})
	interceptors = append(interceptors, config.interceptors...)

	return &a2asrv.InterceptedHandler{
		Handler:      &requestHandler{port: port},
		Interceptors: interceptors,
		Logger:       config.logger,
	}
}

// NewJSONRPCHandler constructs an A2A v1 JSON-RPC HTTP handler backed only by the injected Port.
func NewJSONRPCHandler(port Port, options ...HandlerOption) http.Handler {
	return newJSONRPCHandler(port, a2aStreamKeepAliveInterval, options...)
}

func newJSONRPCHandler(port Port, keepAliveInterval time.Duration, options ...HandlerOption) http.Handler {
	return a2asrv.NewJSONRPCHandler(
		NewRequestHandler(port, options...),
		a2asrv.WithTransportKeepAlive(keepAliveInterval),
		a2asrv.WithTransportPanicHandler(a2aTransportPanicError),
	)
}

func a2aTransportPanicError(recovered any) error {
	panicText := strings.TrimSpace(fmt.Sprint(recovered))
	if newline := strings.IndexByte(panicText, '\n'); newline >= 0 {
		panicText = panicText[:newline]
	}
	if len(panicText) > 256 {
		panicText = panicText[:256]
	}
	slog.Error("A2A JSON-RPC transport panic",
		"panic_type", fmt.Sprintf("%T", recovered),
		"panic", panicText,
		"stack", string(debug.Stack()),
	)
	return a2a.NewError(a2a.ErrInternalError, "A2A transport internal error")
}

func (handler *requestHandler) GetTask(ctx context.Context, request *a2a.GetTaskRequest) (*a2a.Task, error) {
	return handler.port.GetTask(ctx, request)
}

func (handler *requestHandler) ListTasks(ctx context.Context, request *a2a.ListTasksRequest) (*a2a.ListTasksResponse, error) {
	lister, ok := handler.port.(TaskLister)
	if !ok {
		return nil, a2a.ErrUnsupportedOperation
	}
	return lister.ListTasks(ctx, request)
}

func (handler *requestHandler) CancelTask(ctx context.Context, request *a2a.CancelTaskRequest) (*a2a.Task, error) {
	canceler, ok := handler.port.(TaskCanceler)
	if !ok {
		return nil, a2a.ErrUnsupportedOperation
	}
	return canceler.CancelTask(ctx, request)
}

func (handler *requestHandler) SendMessage(ctx context.Context, request *a2a.SendMessageRequest) (a2a.SendMessageResult, error) {
	return handler.port.SendMessage(ctx, request)
}

func (handler *requestHandler) SubscribeToTask(ctx context.Context, request *a2a.SubscribeToTaskRequest) iter.Seq2[a2a.Event, error] {
	streamer, ok := handler.port.(TaskStreamer)
	if !ok {
		return unsupportedEvents(a2a.ErrUnsupportedOperation)
	}
	return streamer.SubscribeToTask(ctx, request)
}

func (handler *requestHandler) SendStreamingMessage(ctx context.Context, request *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error] {
	streamer, ok := handler.port.(TaskStreamer)
	if !ok {
		return unsupportedEvents(a2a.ErrUnsupportedOperation)
	}
	return streamer.SendStreamingMessage(ctx, request)
}

func (handler *requestHandler) GetTaskPushConfig(ctx context.Context, request *a2a.GetTaskPushConfigRequest) (*a2a.PushConfig, error) {
	manager, ok := handler.port.(PushConfigManager)
	if !ok {
		return nil, a2a.ErrPushNotificationNotSupported
	}
	return manager.GetTaskPushConfig(ctx, request)
}

func (handler *requestHandler) ListTaskPushConfigs(ctx context.Context, request *a2a.ListTaskPushConfigRequest) (*a2a.ListTaskPushConfigResponse, error) {
	manager, ok := handler.port.(PushConfigManager)
	if !ok {
		return nil, a2a.ErrPushNotificationNotSupported
	}
	return manager.ListTaskPushConfigs(ctx, request)
}

func (handler *requestHandler) CreateTaskPushConfig(ctx context.Context, request *a2a.PushConfig) (*a2a.PushConfig, error) {
	manager, ok := handler.port.(PushConfigManager)
	if !ok {
		return nil, a2a.ErrPushNotificationNotSupported
	}
	return manager.CreateTaskPushConfig(ctx, request)
}

func (handler *requestHandler) DeleteTaskPushConfig(ctx context.Context, request *a2a.DeleteTaskPushConfigRequest) error {
	manager, ok := handler.port.(PushConfigManager)
	if !ok {
		return a2a.ErrPushNotificationNotSupported
	}
	return manager.DeleteTaskPushConfig(ctx, request)
}

func (handler *requestHandler) GetExtendedAgentCard(context.Context, *a2a.GetExtendedAgentCardRequest) (*a2a.AgentCard, error) {
	return nil, a2a.ErrUnsupportedOperation
}

func unsupportedEvents(err error) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		yield(nil, err)
	}
}
