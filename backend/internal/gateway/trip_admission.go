package gateway

import (
	"context"
	"errors"
	"sync"
)

var (
	ErrTripAdmissionInvalid = errors.New("trip-scoped message is invalid")
	ErrTripAdmissionClosed  = errors.New("trip admission is closed")
)

type TripCommandHandler func(context.Context, AuthenticatedMessage)

// TripCommandAdmission provides one bounded FIFO queue and one serial worker
// per active trip. It intentionally does not perform authorization or domain
// mutation; those remain in the command adapter and persistence layer.
type TripCommandAdmission struct {
	context   context.Context
	cancel    context.CancelFunc
	capacity  int
	maxTrips  int
	handler   TripCommandHandler
	queuesMu  sync.Mutex
	queues    map[string]*tripQueue
	workers   sync.WaitGroup
	closeOnce sync.Once
}

type tripQueue struct {
	tripID string
	items  chan AuthenticatedMessage
}

func NewTripCommandAdmission(
	parent context.Context,
	queueCapacity int,
	maxTrips int,
	handler TripCommandHandler,
) (*TripCommandAdmission, error) {
	if parent == nil || queueCapacity <= 0 || maxTrips <= 0 || handler == nil {
		return nil, errors.New("invalid trip admission configuration")
	}
	ctx, cancel := context.WithCancel(parent)
	return &TripCommandAdmission{
		context: ctx, cancel: cancel, capacity: queueCapacity, maxTrips: maxTrips,
		handler: handler, queues: make(map[string]*tripQueue),
	}, nil
}

func (admission *TripCommandAdmission) Submit(message AuthenticatedMessage) error {
	tripID, ok := message.Message["trip_id"].(string)
	if !ok || !canonicalUUID(tripID) {
		return ErrTripAdmissionInvalid
	}
	admission.queuesMu.Lock()
	if admission.context.Err() != nil {
		admission.queuesMu.Unlock()
		return ErrTripAdmissionClosed
	}
	queue := admission.queues[tripID]
	if queue == nil {
		if len(admission.queues) >= admission.maxTrips {
			admission.queuesMu.Unlock()
			return ErrConnectionCapacity
		}
		queue = &tripQueue{tripID: tripID, items: make(chan AuthenticatedMessage, admission.capacity)}
		admission.queues[tripID] = queue
		admission.workers.Add(1)
		go admission.run(queue)
	}
	admission.queuesMu.Unlock()

	select {
	case queue.items <- message:
		return nil
	default:
		return ErrConnectionCapacity
	}
}

func (admission *TripCommandAdmission) run(queue *tripQueue) {
	defer admission.workers.Done()
	for {
		select {
		case <-admission.context.Done():
			return
		case message := <-queue.items:
			admission.handler(admission.context, message)
		}
	}
}

func (admission *TripCommandAdmission) Close() {
	admission.closeOnce.Do(func() {
		admission.cancel()
		admission.workers.Wait()
	})
}
