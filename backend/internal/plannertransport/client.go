package plannertransport

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	liveroutev1 "github.com/liveroute/liveroute/backend/gen/liveroute/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

var (
	ErrClosed            = errors.New("planner stream client is closed")
	ErrStreamUnavailable = errors.New("planner stream is unavailable")
)

const ProtocolVersion = "liveroute.v1"

var RequiredCapabilities = []string{
	"canonical_first_plan_sync",
	"durable_plan_proposals",
	"epoch_scoped_observations",
	"finalized_mutation_watermark",
	"result_quality_metadata",
	"snapshot_schema_1",
	"user_authoritative_current_plan",
}

type Stream interface {
	Send(*liveroutev1.PlannerStreamRequest) error
	Recv() (*liveroutev1.PlannerStreamResponse, error)
	CloseSend() error
}

type StreamFactory func(context.Context) (Stream, error)

type Config struct {
	BackendInstanceID    string
	AdmissionCapacity    int
	NotificationCapacity int
	ReconnectDelay       func(uint64) time.Duration
}

type Client struct {
	config        Config
	streamFactory StreamFactory
	context       context.Context
	cancel        context.CancelFunc
	requests      chan exchange
	notifications chan *liveroutev1.PlannerStreamResponse
	ready         atomic.Bool
	wait          sync.WaitGroup
}

type exchange struct {
	context  context.Context
	request  *liveroutev1.PlannerStreamRequest
	response chan exchangeResult
}

type exchangeResult struct {
	response *liveroutev1.PlannerStreamResponse
	err      error
}

type received struct {
	response *liveroutev1.PlannerStreamResponse
	err      error
}

func Dial(target string, config Config) (*Client, *grpc.ClientConn, error) {
	if target == "" {
		return nil, nil, errors.New("planner target is required")
	}
	connection, err := grpc.NewClient(
		target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(4*1024*1024),
			grpc.MaxCallSendMsgSize(4*1024*1024),
		),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("create planner connection: %w", err)
	}
	generated := liveroutev1.NewLiveRoutePlannerClient(connection)
	client, err := New(config, func(ctx context.Context) (Stream, error) {
		return generated.PlanTrips(ctx)
	})
	if err != nil {
		_ = connection.Close()
		return nil, nil, err
	}
	return client, connection, nil
}

func New(config Config, factory StreamFactory) (*Client, error) {
	if config.BackendInstanceID == "" || config.AdmissionCapacity <= 0 ||
		config.NotificationCapacity <= 0 || factory == nil {
		return nil, errors.New("invalid planner stream client configuration")
	}
	if config.ReconnectDelay == nil {
		config.ReconnectDelay = reconnectDelay
	}
	ctx, cancel := context.WithCancel(context.Background())
	client := &Client{
		config:        config,
		streamFactory: factory,
		context:       ctx,
		cancel:        cancel,
		requests:      make(chan exchange, config.AdmissionCapacity),
		notifications: make(chan *liveroutev1.PlannerStreamResponse, config.NotificationCapacity),
	}
	client.wait.Add(1)
	go client.run()
	return client, nil
}

func (client *Client) Exchange(
	ctx context.Context,
	request *liveroutev1.PlannerStreamRequest,
) (*liveroutev1.PlannerStreamResponse, error) {
	if ctx == nil || request == nil || request.GetRequestId() == "" {
		return nil, errors.New("correlated planner request is required")
	}
	call := exchange{context: ctx, request: request, response: make(chan exchangeResult, 1)}
	select {
	case client.requests <- call:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-client.context.Done():
		return nil, ErrClosed
	}
	select {
	case result := <-call.response:
		return result.response, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-client.context.Done():
		return nil, ErrClosed
	}
}

func (client *Client) Notifications() <-chan *liveroutev1.PlannerStreamResponse {
	return client.notifications
}

// StreamReady reports whether the current long-lived stream completed the V1
// capability negotiation. It is false during startup and reconnect windows.
func (client *Client) StreamReady() bool {
	return client.ready.Load()
}

func (client *Client) Close() {
	client.cancel()
	client.wait.Wait()
}

func (client *Client) run() {
	defer client.wait.Done()
	defer close(client.notifications)
	var failures uint64
	for client.context.Err() == nil {
		streamContext, cancelStream := context.WithCancel(client.context)
		stream, err := client.openStream(streamContext)
		if err == nil {
			client.ready.Store(true)
			failures = 0
			err = client.serve(stream)
			client.ready.Store(false)
			_ = stream.CloseSend()
		}
		if err != nil {
			client.ready.Store(false)
		}
		cancelStream()
		if client.context.Err() != nil {
			return
		}
		failures++
		delay := client.config.ReconnectDelay(failures)
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-client.context.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		}
	}
}

func (client *Client) openStream(ctx context.Context) (Stream, error) {
	stream, err := client.streamFactory(ctx)
	if err != nil {
		return nil, err
	}
	requestID, err := NewRequestID()
	if err != nil {
		_ = stream.CloseSend()
		return nil, err
	}
	capabilities := append([]string(nil), RequiredCapabilities...)
	sort.Strings(capabilities)
	if err := stream.Send(&liveroutev1.PlannerStreamRequest{
		RequestId: requestID,
		Payload: &liveroutev1.PlannerStreamRequest_OpenStream{
			OpenStream: &liveroutev1.OpenStream{
				BackendInstanceId: client.config.BackendInstanceID,
				ProtocolVersion:   ProtocolVersion,
				Capabilities:      capabilities,
			},
		},
	}); err != nil {
		_ = stream.CloseSend()
		return nil, err
	}
	response, err := stream.Recv()
	if err != nil {
		_ = stream.CloseSend()
		return nil, err
	}
	ready := response.GetStreamReady()
	if response.GetRequestId() != requestID || ready == nil ||
		ready.GetStatus() != liveroutev1.StatusCode_STATUS_CODE_OK ||
		ready.GetProtocolVersion() != ProtocolVersion ||
		!containsCapabilities(ready.GetCapabilities(), capabilities) {
		_ = stream.CloseSend()
		return nil, errors.New("planner rejected stream negotiation")
	}
	return stream, nil
}

func (client *Client) serve(stream Stream) error {
	receive := make(chan received, 1)
	go func() {
		for {
			response, err := stream.Recv()
			select {
			case receive <- received{response: response, err: err}:
			case <-client.context.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	pending := map[string]exchange{}
	failPending := func(err error) {
		for id, call := range pending {
			call.response <- exchangeResult{err: err}
			delete(pending, id)
		}
	}
	defer failPending(ErrStreamUnavailable)
	for {
		select {
		case call := <-client.requests:
			if err := call.context.Err(); err != nil {
				call.response <- exchangeResult{err: err}
				continue
			}
			id := call.request.GetRequestId()
			if _, exists := pending[id]; exists {
				call.response <- exchangeResult{err: errors.New("duplicate in-flight request id")}
				continue
			}
			pending[id] = call
			if err := stream.Send(call.request); err != nil {
				delete(pending, id)
				call.response <- exchangeResult{err: ErrStreamUnavailable}
				return err
			}
		case incoming := <-receive:
			if incoming.err != nil {
				return incoming.err
			}
			id := incoming.response.GetRequestId()
			if call, ok := pending[id]; ok {
				delete(pending, id)
				call.response <- exchangeResult{response: incoming.response}
				continue
			}
			select {
			case client.notifications <- incoming.response:
			default:
				return errors.New("planner notification capacity exhausted")
			}
		case <-client.context.Done():
			return ErrClosed
		}
	}
}

func containsCapabilities(actual, required []string) bool {
	values := make(map[string]bool, len(actual))
	for _, value := range actual {
		values[value] = true
	}
	for _, value := range required {
		if !values[value] {
			return false
		}
	}
	return true
}

func reconnectDelay(failures uint64) time.Duration {
	shift := uint64(0)
	if failures > 0 {
		shift = failures - 1
	}
	if shift > 7 {
		shift = 7
	}
	ceiling := 100 * time.Millisecond * time.Duration(1<<shift)
	if ceiling > 10*time.Second {
		ceiling = 10 * time.Second
	}
	value, err := rand.Int(rand.Reader, big.NewInt(int64(ceiling)+1))
	if err != nil {
		return ceiling
	}
	return time.Duration(value.Int64())
}

func NewRequestID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate request id: %w", err)
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}
