package gateway

import (
	"context"
	"errors"
	"testing"
)

type dispatcherHandler struct {
	called bool
	result []byte
}

type dispatcherSink struct {
	published []byte
}

func (sink *dispatcherSink) PublishServerEnvelope(payload []byte) error {
	sink.published = append([]byte(nil), payload...)
	return nil
}

func (handler *dispatcherHandler) Handle(context.Context, AuthenticatedMessage) ([]byte, error) {
	handler.called = true
	return handler.result, nil
}

func TestCommandDispatcherRoutesSupportedCommands(t *testing.T) {
	create := &dispatcherHandler{result: []byte("create")}
	replace := &dispatcherHandler{result: []byte("replace")}
	edited := &dispatcherHandler{result: []byte("edited")}
	runtime := &dispatcherHandler{result: []byte("runtime")}
	proposal := &dispatcherHandler{result: []byte("proposal")}
	dispatcher, err := NewCommandDispatcher(create, replace, edited, runtime, proposal)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		message AuthenticatedMessage
		want    string
		handler *dispatcherHandler
	}{
		{name: "create", message: dispatcherMessage("create_trip", ""), want: "create", handler: create},
		{name: "replace", message: dispatcherMessage("trip_command", "replace_current_plan"), want: "replace", handler: replace},
		{name: "edit", message: dispatcherMessage("trip_command", "trip_edited"), want: "edited", handler: edited},
		{name: "runtime", message: dispatcherMessage("trip_command", "travel_delay"), want: "runtime", handler: runtime},
		{name: "proposal", message: dispatcherMessage("trip_command", "accept_proposal"), want: "proposal", handler: proposal},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := dispatcher.Dispatch(context.Background(), test.message)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != test.want || !test.handler.called {
				t.Fatalf("result=%q called=%v", got, test.handler.called)
			}
		})
	}
}

func TestCommandDispatcherRejectsUnimplementedClientFlows(t *testing.T) {
	dispatcher, err := NewCommandDispatcher(
		&dispatcherHandler{}, &dispatcherHandler{}, &dispatcherHandler{},
		&dispatcherHandler{}, &dispatcherHandler{},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range []AuthenticatedMessage{
		dispatcherMessage("subscribe_trip", ""),
		dispatcherMessage("telemetry_update", ""),
		dispatcherMessage("resynchronize_trip", ""),
	} {
		if _, err := dispatcher.Dispatch(context.Background(), message); !errors.Is(err, ErrClientMessageUnsupported) {
			t.Fatalf("message kind error=%v", err)
		}
	}
	if _, err := dispatcher.Dispatch(context.Background(), dispatcherMessage("trip_command", "unknown")); !errors.Is(err, ErrCommandKindUnsupported) {
		t.Fatalf("command kind error=%v", err)
	}
}

func TestNewCommandDispatcherRequiresAllAdapters(t *testing.T) {
	if _, err := NewCommandDispatcher(nil, &dispatcherHandler{}, &dispatcherHandler{}, &dispatcherHandler{}, &dispatcherHandler{}); err == nil {
		t.Fatal("expected missing adapter error")
	}
}

func TestCommandDispatcherPublishesAdapterAcknowledgement(t *testing.T) {
	dispatcher, err := NewCommandDispatcher(
		&dispatcherHandler{result: []byte("ack")}, &dispatcherHandler{},
		&dispatcherHandler{}, &dispatcherHandler{}, &dispatcherHandler{},
	)
	if err != nil {
		t.Fatal(err)
	}
	sink := &dispatcherSink{}
	if err := dispatcher.Publish(context.Background(), AuthenticatedMessage{
		Message: dispatcherMessage("create_trip", "").Message,
		Sink:    sink,
	}); err != nil {
		t.Fatal(err)
	}
	if string(sink.published) != "ack" {
		t.Fatalf("published=%q", sink.published)
	}
}

func TestCommandDispatcherDoesNotPublishFailedDispatch(t *testing.T) {
	dispatcher, err := NewCommandDispatcher(
		&dispatcherHandler{}, &dispatcherHandler{}, &dispatcherHandler{},
		&dispatcherHandler{}, &dispatcherHandler{},
	)
	if err != nil {
		t.Fatal(err)
	}
	sink := &dispatcherSink{}
	if err := dispatcher.Publish(context.Background(), AuthenticatedMessage{
		Message: dispatcherMessage("subscribe_trip", "").Message,
		Sink:    sink,
	}); !errors.Is(err, ErrClientMessageUnsupported) {
		t.Fatalf("dispatch error=%v", err)
	}
	if sink.published != nil {
		t.Fatalf("unexpected publication=%q", sink.published)
	}
}

func dispatcherMessage(kind, commandKind string) AuthenticatedMessage {
	message := AuthenticatedMessage{Message: map[string]any{"kind": kind}}
	if commandKind != "" {
		message.Message["payload"] = map[string]any{"command_kind": commandKind}
	}
	return message
}
