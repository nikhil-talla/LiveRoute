package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	liveroutev1 "github.com/liveroute/liveroute/backend/gen/liveroute/v1"
	"github.com/liveroute/liveroute/backend/internal/auth"
	"github.com/liveroute/liveroute/backend/internal/config"
	"github.com/liveroute/liveroute/backend/internal/dispatch"
	"github.com/liveroute/liveroute/backend/internal/gateway"
	"github.com/liveroute/liveroute/backend/internal/persistence"
	"github.com/liveroute/liveroute/backend/internal/place"
	"github.com/liveroute/liveroute/backend/internal/plannertransport"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("backend failed", "error", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("liveroute-backend", flag.ContinueOnError)
	configPath := flags.String("config", envOr("LIVEROUTE_CONFIG_PATH", "/app/config/local-v1.yaml"), "configuration path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("liveroute-backend does not accept positional arguments")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	databaseURL := os.Getenv("LIVEROUTE_DATABASE_URL")
	plannerTarget := envOr("LIVEROUTE_PLANNER_TARGET", "planner-service:50051")
	bindAddress := envOr("LIVEROUTE_BIND_ADDRESS", ":8080")
	backendID := envOr("LIVEROUTE_BACKEND_INSTANCE_ID", "")
	if databaseURL == "" || backendID == "" || !canonicalUUID(backendID) {
		return errors.New("LIVEROUTE_DATABASE_URL and canonical LIVEROUTE_BACKEND_INSTANCE_ID are required")
	}
	rootContext, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return fmt.Errorf("parse database URL: %w", err)
	}
	poolConfig.MaxConns = int32(cfg.Database.PoolMaxConnections)
	pool, err := pgxpool.NewWithConfig(rootContext, poolConfig)
	if err != nil {
		return fmt.Errorf("open database pool: %w", err)
	}
	defer pool.Close()

	canonicalState, err := persistence.NewCanonicalStateStore(pool)
	if err != nil {
		return err
	}
	commands, err := persistence.NewCommandStore(pool)
	if err != nil {
		return err
	}
	outbox, err := persistence.NewOutboxStore(pool)
	if err != nil {
		return err
	}
	leases, err := persistence.NewLeaseStore(pool)
	if err != nil {
		return err
	}
	proposals, err := persistence.NewProposalStore(pool)
	if err != nil {
		return err
	}
	snapshots, err := persistence.NewSnapshotStore(pool)
	if err != nil {
		return err
	}
	authenticator, err := persistence.NewDevelopmentAuthenticator(pool)
	if err != nil {
		return err
	}
	hmacKeys, err := loadHTTPHMACKeys()
	if err != nil {
		return err
	}
	httpAuthStore, err := persistence.NewHTTPAuthStore(pool, hmacKeys)
	if err != nil {
		return err
	}
	savedTrips, err := persistence.NewSavedTripStore(pool)
	if err != nil {
		return err
	}
	var placeStore *persistence.PlaceStore
	var placeResolver *place.MapboxResolver
	mapboxToken, configured, err := loadOptionalSecret("LIVEROUTE_MAPBOX_TOKEN", "LIVEROUTE_MAPBOX_TOKEN_FILE", "/run/secrets/liveroute_mapbox_token")
	if err != nil {
		return err
	}
	if configured {
		timeZones, loadErr := place.LoadTimeZoneResolver(
			envOr("LIVEROUTE_TIMEZONE_BOUNDARIES_PATH", "/opt/liveroute/share/timezone-boundaries/2026c/combined-with-oceans.json"),
			"17f0821bad87d7a44dcebff12ad70b82bf973c54942e29fe20f5f9f8be4b3db6",
			envOr("LIVEROUTE_TZDATA_ZONE_TABLE_PATH", "/opt/liveroute/share/tzdata/2026c/zoneinfo/zone1970.tab"),
		)
		if loadErr != nil {
			return loadErr
		}
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.DialContext = (&net.Dialer{Timeout: 500 * time.Millisecond, KeepAlive: 30 * time.Second}).DialContext
		placeResolver, err = place.NewMapboxResolver(place.MapboxConfig{
			Endpoint: "https://api.mapbox.com/search/geocode/v6/reverse", Token: mapboxToken,
			Client: &http.Client{Transport: transport, Timeout: 3 * time.Second}, TimeZones: timeZones,
			MaxResponseBytes: 262144, GlobalConcurrency: 16, PerUserConcurrency: 1, AttemptsPerMinute: 5,
		})
		if err != nil {
			return err
		}
		placeStore, err = persistence.NewPlaceStore(pool, hmacKeys, placeResolver)
		if err != nil {
			return err
		}
	}
	var googleVerifier *auth.GoogleVerifier
	if clientID := strings.TrimSpace(os.Getenv("LIVEROUTE_GOOGLE_WEB_CLIENT_ID")); clientID != "" {
		googleVerifier, err = auth.NewGoogleVerifier(rootContext, clientID, &http.Client{Timeout: 3 * time.Second})
		if err != nil {
			return err
		}
	}

	planner, plannerConnection, err := plannertransport.Dial(plannerTarget, plannertransport.Config{
		BackendInstanceID: backendID, AdmissionCapacity: cfg.Stream.InboundQueueCapacity,
		NotificationCapacity: cfg.Stream.OutboundQueueCapacity,
		ReconnectDelay:       reconnectDelay(cfg.Stream.ReconnectInitialBackoffMS, cfg.Stream.ReconnectMaxBackoffMS),
	})
	if err != nil {
		return err
	}
	defer plannerConnection.Close()
	defer planner.Close()

	holderID := backendID
	bootstrapper, err := dispatch.NewBootstrapper(holderID, time.Duration(cfg.Planner.AttemptTimeoutMS)*time.Millisecond, canonicalState, snapshots, leases, planner)
	if err != nil {
		return err
	}
	supervisor, err := dispatch.NewRuntimeSupervisor(holderID,
		time.Duration(cfg.Database.LeaseDurationMS)*time.Millisecond,
		time.Duration(cfg.Database.LeaseRenewalMarginMS)*time.Millisecond,
		cfg.Runtime.MaxActiveTrips, leases, bootstrapper,
		func(tripID string, leaseErr error) {
			slog.Warn("trip runtime lease lost", "trip_id", tripID, "error", leaseErr)
		},
	)
	if err != nil {
		return err
	}
	defer supervisor.Close()
	var activationMu sync.Mutex
	activationRuns := make(map[string]struct{})
	telemetry, err := dispatch.NewTelemetryDispatcher(planner, supervisor, time.Duration(cfg.Planner.AttemptTimeoutMS)*time.Millisecond)
	if err != nil {
		return err
	}

	runtimeFinalizer, err := dispatch.NewRuntimeFinalizer(commands)
	if err != nil {
		return err
	}
	durableDispatcher, err := dispatch.New(dispatch.Config{
		ClaimOwner: backendID, LeaseHolder: holderID, BatchSize: cfg.Database.OutboxClaimBatchSize,
		ClaimDuration:  time.Duration(cfg.Database.LeaseDurationMS) * time.Millisecond,
		AttemptTimeout: time.Duration(cfg.Planner.AttemptTimeoutMS) * time.Millisecond,
	}, outbox, leases, planner, canonicalState, runtimeFinalizer, commands)
	if err != nil {
		return err
	}
	proposalConsumer, err := dispatch.NewProposalConsumer(holderID, proposals)
	if err != nil {
		return err
	}
	startActivation := func(tripID, operationID string) {
		activationMu.Lock()
		if _, exists := activationRuns[tripID]; exists {
			activationMu.Unlock()
			return
		}
		activationRuns[tripID] = struct{}{}
		activationMu.Unlock()
		go func() {
			defer func() {
				activationMu.Lock()
				delete(activationRuns, tripID)
				activationMu.Unlock()
			}()
			if err := supervisor.Activate(rootContext, tripID); err != nil {
				slog.Warn("activation runtime bootstrap failed", "trip_id", tripID, "operation_id", operationID, "error", err)
				return
			}
			state, ok := supervisor.RuntimeState(tripID)
			if !ok {
				slog.Warn("activation runtime state disappeared", "trip_id", tripID, "operation_id", operationID)
				return
			}
			if _, err := savedTrips.CompleteActivation(rootContext, persistence.CompleteActivationRequest{
				TripID: tripID, OperationID: operationID, HolderID: holderID, RuntimeEpoch: state.RuntimeEpoch,
			}); err != nil {
				slog.Warn("activation completion failed", "trip_id", tripID, "operation_id", operationID, "error", err)
			}
		}()
	}
	startDeactivation := func(tripID, operationID string) {
		go func() {
			state, ok := supervisor.RuntimeState(tripID)
			runtimeEpoch := uint64(0)
			if ok {
				runtimeEpoch = state.RuntimeEpoch
			} else if lease, err := leases.Current(rootContext, tripID, holderID); err == nil {
				runtimeEpoch = lease.RuntimeEpoch
			}
			if err := supervisor.Deactivate(rootContext, tripID); err != nil {
				slog.Warn("deactivation runtime teardown failed", "trip_id", tripID, "operation_id", operationID, "error", err)
				return
			}
			if _, err := savedTrips.CompleteDeactivation(rootContext, persistence.CompleteDeactivationRequest{
				TripID: tripID, OperationID: operationID, HolderID: holderID, RuntimeEpoch: runtimeEpoch,
			}); err != nil {
				slog.Warn("deactivation completion failed", "trip_id", tripID, "operation_id", operationID, "error", err)
			}
		}()
	}

	createAdapter, err := gateway.NewCreateTripAdapter(canonicalState)
	if err != nil {
		return err
	}
	replaceAdapter, err := gateway.NewReplaceCurrentPlanAdapter(canonicalState, uint32(cfg.Database.OutboxMaxInFlight))
	if err != nil {
		return err
	}
	editAdapter, err := gateway.NewTripEditedAdapter(canonicalState, uint32(cfg.Database.OutboxMaxInFlight))
	if err != nil {
		return err
	}
	runtimeAdapter, err := gateway.NewRuntimeMutationAdapter(commands)
	if err != nil {
		return err
	}
	proposalAdapter, err := gateway.NewProposalDecisionAdapter(proposals, commands)
	if err != nil {
		return err
	}
	commandDispatcher, err := gateway.NewCommandDispatcher(createAdapter, replaceAdapter, editAdapter, runtimeAdapter, proposalAdapter)
	if err != nil {
		return err
	}

	validator, err := loadValidator()
	if err != nil {
		return err
	}
	websocketHandler, err := gateway.NewHandler(gateway.Config{
		Validator: validator, Authenticator: authenticator, BackendInstanceID: backendID,
		FrameLimit: int64(cfg.Limits.WebSocketFrameBytes), DecodedMessageLimit: int64(cfg.Limits.WebSocketDecodedMessageBytes),
		InboundQueueCapacity: cfg.WebSocket.InboundQueueCapacity, OutboundQueueCapacity: cfg.WebSocket.OutboundQueueCapacity,
		HeartbeatInterval:           time.Duration(cfg.WebSocket.HeartbeatIntervalMS) * time.Millisecond,
		IdleTimeout:                 time.Duration(cfg.WebSocket.IdleTimeoutMS) * time.Millisecond,
		AuthenticationTimeout:       time.Duration(cfg.WebSocket.AuthenticationTimeoutMS) * time.Millisecond,
		AllowOriginlessLocalClients: cfg.WebSocket.AllowOriginlessLocalClients,
		AllowedOrigins:              cfg.WebSocket.AllowedOrigins, MaxOutstandingResyncIDs: cfg.Limits.ResynchronizationOutstandingCommandIDs,
	})
	if err != nil {
		return err
	}
	subscriptions, err := gateway.NewSubscriptionHub(cfg.Limits.ResynchronizationOutstandingCommandIDs)
	if err != nil {
		return err
	}
	websocketHandler.SetOnSessionClosed(subscriptions.RemoveSink)
	durableDispatcher.SetOnRuntimeFinalized(func(
		finalized persistence.FinalizedCommand,
		response *liveroutev1.PlannerStreamResponse,
	) {
		if err := supervisor.CommitMutation(
			finalized.TripID,
			response.GetRuntimeEpoch(),
			response.GetAcceptedMutationSequence(),
			response.GetPlannerStateVersion(),
		); err != nil {
			slog.Warn("commit finalized runtime watermarks failed", "trip_id", finalized.TripID, "error", err)
		}
		raw, err := gateway.BuildRuntimeCommandFinalizedEnvelope(
			finalized,
			gateway.RuntimeVersions{
				RuntimeEpoch:                response.GetRuntimeEpoch(),
				AcceptedMutationSequence:    response.GetAcceptedMutationSequence(),
				AcceptedObservationSequence: response.GetAcceptedObservationSequence(),
			},
		)
		if err != nil {
			slog.Warn("build finalized command acknowledgement failed", "trip_id", finalized.TripID, "error", err)
			return
		}
		if err := subscriptions.PublishTrip(finalized.TripID, raw); err != nil {
			slog.Warn("publish finalized command acknowledgement failed", "trip_id", finalized.TripID, "error", err)
		}
		if err := broadcastSubscriptionState(
			rootContext,
			finalized.TripID,
			canonicalState,
			proposals,
			supervisor,
			subscriptions,
		); err != nil {
			slog.Warn("publish finalized trip state failed", "trip_id", finalized.TripID, "error", err)
		}
	})
	proposalConsumer.SetOnStored(func(ctx context.Context, stored persistence.PersistedProposal, response *liveroutev1.PlannerStreamResponse) error {
		raw, err := gateway.BuildPlanProposalEnvelope(stored.TripID, gateway.TripVersions{
			TripRevision:                strconv.FormatUint(response.GetTripRevision(), 10),
			RuntimeEpoch:                strconv.FormatUint(response.GetRuntimeEpoch(), 10),
			PlannerStateVersion:         strconv.FormatUint(response.GetPlannerStateVersion(), 10),
			AcceptedMutationSequence:    strconv.FormatUint(response.GetAcceptedMutationSequence(), 10),
			AcceptedObservationSequence: strconv.FormatUint(response.GetAcceptedObservationSequence(), 10),
		}, stored.Payload)
		if err != nil {
			return err
		}
		return subscriptions.PublishTrip(stored.TripID, raw)
	})
	admission, err := gateway.NewTripCommandAdmission(rootContext, cfg.WebSocket.InboundQueueCapacity, cfg.Runtime.MaxActiveTrips,
		func(ctx context.Context, message gateway.AuthenticatedMessage) {
			handleMessage(ctx, message, commandDispatcher, supervisor, telemetry, canonicalState, proposals, commands, subscriptions)
		})
	if err != nil {
		return err
	}
	defer admission.Close()
	// The connection reader remains bounded; this callback transfers trip work
	// to the bounded serial per-trip admission layer.
	websocketHandler.SetOnMessage(func(_ context.Context, message gateway.AuthenticatedMessage) {
		if err := admission.Submit(message); err != nil {
			if kind, _ := message.Message["kind"].(string); kind == "telemetry_update" {
				publishTelemetryAdmissionDrop(message, err)
			} else {
				publishError(message, err)
			}
		}
	})
	grpcHealth := healthpb.NewHealthClient(plannerConnection)
	plannerHealth := gateway.GRPCServingCheck(grpcHealth, "liveroute.v1.LiveRoutePlanner")
	healthHandler, err := gateway.NewHealthHandler(gateway.ReadinessChecks{
		MigrationsCurrent: func(ctx context.Context) error { return persistence.MigrationsCurrent(ctx, pool, 4) },
		PostgreSQLPing:    func(ctx context.Context) error { return persistence.PostgreSQLPing(ctx, pool) },
		PlannerStreamReady: func(ctx context.Context) error {
			if !planner.StreamReady() {
				return errors.New("planner stream is not ready")
			}
			return plannerHealth(ctx)
		},
		OSRMCarServing:  gateway.OSRMTableServingCheck(http.DefaultClient, cfg.Routing.CarEndpoint, "driving"),
		OSRMFootServing: gateway.OSRMTableServingCheck(http.DefaultClient, cfg.Routing.FootEndpoint, "walking"),
		Additional: []gateway.ReadinessCheck{func(context.Context) error {
			if placeResolver == nil {
				return errors.New("permanent geocoder is not configured")
			}
			if !placeResolver.Ready() {
				return errors.New("permanent geocoder rejected its credential")
			}
			return nil
		}},
	})
	if err != nil {
		return err
	}
	server, err := gateway.NewHTTPServer(bindAddress, websocketHandler, healthHandler)
	if err != nil {
		return err
	}
	httpAuthHandler, err := gateway.NewHTTPAuthHandler(gateway.HTTPAuthConfig{
		Store: httpAuthStore, Trips: savedTrips, Places: placeStore, StartActivation: startActivation, StartDeactivation: startDeactivation, GoogleVerifier: googleVerifier, AllowedOrigins: cfg.WebSocket.AllowedOrigins,
		SecureCookies: false, FrontendOrigin: "http://localhost:5173",
	})
	if err != nil {
		return err
	}
	if err := server.SetHTTPAuthHandler(httpAuthHandler); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", bindAddress)
	if err != nil {
		return fmt.Errorf("listen backend: %w", err)
	}

	go runWorker(rootContext, "outbox dispatcher", func(ctx context.Context) error {
		return durableDispatcher.Run(ctx, 20*time.Millisecond, func(err error) { slog.Warn("outbox dispatcher pass failed", "error", err) })
	})
	go runWorker(rootContext, "proposal consumer", func(ctx context.Context) error {
		return proposalConsumer.Run(ctx, planner.Notifications())
	})
	go runWorker(rootContext, "runtime recovery", func(ctx context.Context) error {
		return recoverLeasedTrips(ctx, leases, savedTrips, supervisor, startActivation, startDeactivation)
	})
	if err := server.Serve(rootContext, listener); errors.Is(err, context.Canceled) {
		return nil
	} else {
		return err
	}
}

func recoverLeasedTrips(ctx context.Context, leases *persistence.LeaseStore, savedTrips *persistence.SavedTripStore, supervisor *dispatch.RuntimeSupervisor, startActivation, startDeactivation func(string, string)) error {
	if leases == nil || savedTrips == nil || supervisor == nil || startActivation == nil || startDeactivation == nil {
		return errors.New("runtime recovery dependencies are required")
	}
	for {
		activations, activationErr := savedTrips.PendingActivations(ctx)
		if activationErr != nil && !errors.Is(activationErr, context.Canceled) {
			slog.Warn("list pending activations for recovery failed", "error", activationErr)
		} else if activationErr == nil {
			for _, activation := range activations {
				startActivation(activation.TripID, activation.OperationID)
			}
		}
		deactivations, deactivationErr := savedTrips.PendingDeactivations(ctx)
		if deactivationErr != nil && !errors.Is(deactivationErr, context.Canceled) {
			slog.Warn("list pending deactivations for recovery failed", "error", deactivationErr)
		} else if deactivationErr == nil {
			for _, deactivation := range deactivations {
				startDeactivation(deactivation.TripID, deactivation.OperationID)
			}
		}
		tripIDs, err := leases.LeasedTrips(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("list leased trips for recovery failed", "error", err)
		} else if err == nil {
			for _, tripID := range tripIDs {
				if err := supervisor.Activate(ctx, tripID); err != nil &&
					!errors.Is(err, persistence.ErrLeaseHeld) &&
					!errors.Is(err, dispatch.ErrRuntimeCapacity) &&
					!errors.Is(err, persistence.ErrTripNotFound) {
					slog.Warn("recover leased trip failed", "trip_id", tripID, "error", err)
				}
			}
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func handleMessage(ctx context.Context, message gateway.AuthenticatedMessage, dispatcher *gateway.CommandDispatcher, supervisor *dispatch.RuntimeSupervisor, telemetry *dispatch.TelemetryDispatcher, stateStore *persistence.CanonicalStateStore, proposals *persistence.ProposalStore, commands *persistence.CommandStore, subscriptions *gateway.SubscriptionHub) {
	tripID, _ := message.Message["trip_id"].(string)
	kind, _ := message.Message["kind"].(string)
	switch kind {
	case "subscribe_trip":
		if err := handleSubscribe(ctx, message, supervisor, stateStore, proposals, subscriptions, true); err != nil {
			publishErrorWithState(ctx, message, err, stateStore, supervisor)
		}
		return
	case "unsubscribe_trip":
		if err := handleSubscribe(ctx, message, supervisor, stateStore, proposals, subscriptions, false); err != nil {
			publishErrorWithState(ctx, message, err, stateStore, supervisor)
		}
		return
	case "resynchronize_trip":
		if err := handleResynchronize(ctx, message, supervisor, stateStore, proposals, commands, subscriptions); err != nil {
			publishErrorWithState(ctx, message, err, stateStore, supervisor)
		}
		return
	case "telemetry_update":
		if err := handleTelemetry(ctx, message, supervisor, telemetry, stateStore); err != nil {
			publishErrorWithState(ctx, message, err, stateStore, supervisor)
		}
		return
	}
	if kind != "create_trip" {
		if err := supervisor.Activate(ctx, tripID); err != nil {
			publishErrorWithState(ctx, message, err, stateStore, supervisor)
			return
		}
	}
	acknowledgement, err := dispatcher.Dispatch(ctx, message)
	if err != nil {
		publishErrorWithState(ctx, message, err, stateStore, supervisor)
		return
	}
	if err := message.Sink.PublishServerEnvelope(acknowledgement); err != nil {
		slog.Warn("publish command acknowledgement failed", "error", err)
	}
	if kind == "create_trip" {
		if err := supervisor.Activate(ctx, tripID); err != nil {
			slog.Warn("activate newly created trip failed", "trip_id", tripID, "error", err)
		}
	}
}

func handleSubscribe(ctx context.Context, message gateway.AuthenticatedMessage, supervisor *dispatch.RuntimeSupervisor, stateStore *persistence.CanonicalStateStore, proposals *persistence.ProposalStore, subscriptions *gateway.SubscriptionHub, subscribe bool) error {
	tripID, _ := message.Message["trip_id"].(string)
	_, err := authorizedState(ctx, stateStore, tripID, message.UserID)
	if err != nil {
		return err
	}
	if subscribe {
		if err := supervisor.Activate(ctx, tripID); err != nil {
			return err
		}
		if err := subscriptions.Subscribe(tripID, message.Sink); err != nil {
			return err
		}
	} else {
		subscriptions.Unsubscribe(tripID, message.Sink)
	}
	return publishSubscriptionState(ctx, message, stateStore, proposals, supervisor, subscribe)
}

func handleResynchronize(ctx context.Context, message gateway.AuthenticatedMessage, supervisor *dispatch.RuntimeSupervisor, stateStore *persistence.CanonicalStateStore, proposals *persistence.ProposalStore, commands *persistence.CommandStore, subscriptions *gateway.SubscriptionHub) error {
	tripID, _ := message.Message["trip_id"].(string)
	if _, err := authorizedState(ctx, stateStore, tripID, message.UserID); err != nil {
		return err
	}
	if err := supervisor.Activate(ctx, tripID); err != nil {
		return err
	}
	if err := subscriptions.Subscribe(tripID, message.Sink); err != nil {
		return err
	}
	state, err := authorizedState(ctx, stateStore, tripID, message.UserID)
	if err != nil {
		return err
	}
	pending, err := pendingProposal(ctx, proposals, tripID)
	if err != nil {
		return err
	}
	runtimeState := runtimeVersions(supervisor, tripID)
	syncState, err := runtimeSyncState(ctx, stateStore, state, runtimeState, tripID)
	if err != nil {
		return err
	}
	payload, err := gateway.SubscriptionStatePayload(state, pending, true, syncState)
	if err != nil {
		return err
	}
	delete(payload, "subscribed")
	requested := requestedMessageIDs(message)
	outcomes, err := commandsOutcomes(ctx, commands, tripID, requested)
	if err != nil {
		return err
	}
	payload["outcomes"] = outcomes
	versions := gateway.StateVersions(state, gateway.RuntimeVersions{
		RuntimeEpoch:                runtimeState.RuntimeEpoch,
		PlannerStateVersion:         runtimeState.PlannerStateVersion,
		AcceptedMutationSequence:    runtimeState.AcceptedMutationSequence,
		AcceptedObservationSequence: runtimeState.AcceptedObservationSequence,
	})
	raw, err := gateway.BuildTripStateEnvelope("resynchronization_state", "OK", false, tripID, messageID(message), versions, payload)
	if err != nil {
		return err
	}
	return message.Sink.PublishServerEnvelope(raw)
}

func handleTelemetry(ctx context.Context, message gateway.AuthenticatedMessage, supervisor *dispatch.RuntimeSupervisor, telemetry *dispatch.TelemetryDispatcher, stateStore *persistence.CanonicalStateStore) error {
	tripID, _ := message.Message["trip_id"].(string)
	if _, err := authorizedState(ctx, stateStore, tripID, message.UserID); err != nil {
		return err
	}
	if err := supervisor.Activate(ctx, tripID); err != nil {
		return publishTelemetryFailure(ctx, message, err, stateStore, supervisor)
	}
	clientMessageID, event, err := gateway.ParseTelemetryEvent(message.Raw)
	if err != nil {
		return err
	}
	if clientMessageID != messageID(message) {
		return errors.New("telemetry message identity differs")
	}
	response, observationSequence, err := telemetry.Dispatch(ctx, tripID, event)
	if err != nil {
		return publishTelemetryFailure(ctx, message, err, stateStore, supervisor)
	}
	disposition, status, retryable := dispatch.TelemetryDisposition(response)
	runtime := runtimeVersions(supervisor, tripID)
	state, err := authorizedState(ctx, stateStore, tripID, message.UserID)
	if err != nil {
		return err
	}
	raw, err := gateway.BuildTelemetryStatusEnvelope(tripID, messageID(message), gateway.StateVersions(state, gateway.RuntimeVersions{
		RuntimeEpoch:                runtime.RuntimeEpoch,
		PlannerStateVersion:         runtime.PlannerStateVersion,
		AcceptedMutationSequence:    runtime.AcceptedMutationSequence,
		AcceptedObservationSequence: runtime.AcceptedObservationSequence,
	}), status, retryable, disposition, observationSequence)
	if err != nil {
		return err
	}
	return message.Sink.PublishServerEnvelope(raw)
}

func publishTelemetryFailure(ctx context.Context, message gateway.AuthenticatedMessage, err error, stateStore *persistence.CanonicalStateStore, supervisor *dispatch.RuntimeSupervisor) error {
	tripID, _ := message.Message["trip_id"].(string)
	messageID := messageID(message)
	if !canonicalUUID(tripID) || !canonicalUUID(messageID) || message.Sink == nil {
		return err
	}
	status, retryable, _ := gateway.ErrorStatus(err)
	if status == "INTERNAL" {
		status, retryable = "UNAVAILABLE", true
	}
	versions := gateway.ZeroTripVersions()
	if state, loadErr := stateStore.Load(ctx, tripID); loadErr == nil && state.OwnerUserID == message.UserID {
		runtime := runtimeVersions(supervisor, tripID)
		versions = gateway.StateVersions(state, gateway.RuntimeVersions{
			RuntimeEpoch:                runtime.RuntimeEpoch,
			PlannerStateVersion:         runtime.PlannerStateVersion,
			AcceptedMutationSequence:    runtime.AcceptedMutationSequence,
			AcceptedObservationSequence: runtime.AcceptedObservationSequence,
		})
	}
	raw, buildErr := gateway.BuildTelemetryStatusEnvelope(tripID, messageID, versions, status, retryable, "rejected", 0)
	if buildErr != nil {
		return err
	}
	if publishErr := message.Sink.PublishServerEnvelope(raw); publishErr != nil {
		return publishErr
	}
	return nil
}

func publishTelemetryAdmissionDrop(message gateway.AuthenticatedMessage, err error) {
	tripID, _ := message.Message["trip_id"].(string)
	messageID := messageID(message)
	if !canonicalUUID(tripID) || !canonicalUUID(messageID) || message.Sink == nil {
		return
	}
	raw, buildErr := gateway.BuildTelemetryStatusEnvelope(tripID, messageID, gateway.ZeroTripVersions(), "RESOURCE_EXHAUSTED", true, "dropped", 0)
	if buildErr == nil {
		_ = message.Sink.PublishServerEnvelope(raw)
	}
	_ = err
}

func publishSubscriptionState(ctx context.Context, message gateway.AuthenticatedMessage, stateStore *persistence.CanonicalStateStore, proposals *persistence.ProposalStore, supervisor *dispatch.RuntimeSupervisor, subscribed bool) error {
	tripID, _ := message.Message["trip_id"].(string)
	state, err := authorizedState(ctx, stateStore, tripID, message.UserID)
	if err != nil {
		return err
	}
	pending, err := pendingProposal(ctx, proposals, tripID)
	if err != nil {
		return err
	}
	runtimeState := runtimeVersions(supervisor, tripID)
	syncState, err := runtimeSyncState(ctx, stateStore, state, runtimeState, tripID)
	if err != nil {
		return err
	}
	payload, err := gateway.SubscriptionStatePayload(state, pending, subscribed, syncState)
	if err != nil {
		return err
	}
	raw, err := gateway.BuildTripStateEnvelope("subscription_state", "OK", false, tripID, messageID(message), gateway.StateVersions(state, gateway.RuntimeVersions{
		RuntimeEpoch:                runtimeState.RuntimeEpoch,
		PlannerStateVersion:         runtimeState.PlannerStateVersion,
		AcceptedMutationSequence:    runtimeState.AcceptedMutationSequence,
		AcceptedObservationSequence: runtimeState.AcceptedObservationSequence,
	}), payload)
	if err != nil {
		return err
	}
	return message.Sink.PublishServerEnvelope(raw)
}

func broadcastSubscriptionState(
	ctx context.Context,
	tripID string,
	stateStore *persistence.CanonicalStateStore,
	proposals *persistence.ProposalStore,
	supervisor *dispatch.RuntimeSupervisor,
	subscriptions *gateway.SubscriptionHub,
) error {
	state, err := stateStore.Load(ctx, tripID)
	if err != nil {
		return err
	}
	pending, err := pendingProposal(ctx, proposals, tripID)
	if err != nil {
		return err
	}
	runtimeState := runtimeVersions(supervisor, tripID)
	syncState, err := runtimeSyncState(ctx, stateStore, state, runtimeState, tripID)
	if err != nil {
		return err
	}
	payload, err := gateway.SubscriptionStatePayload(
		state,
		pending,
		true,
		syncState,
	)
	if err != nil {
		return err
	}
	raw, err := gateway.BuildTripStateEnvelope(
		"subscription_state",
		"OK",
		false,
		tripID,
		"",
		gateway.StateVersions(state, gateway.RuntimeVersions{
			RuntimeEpoch:                runtimeState.RuntimeEpoch,
			PlannerStateVersion:         runtimeState.PlannerStateVersion,
			AcceptedMutationSequence:    runtimeState.AcceptedMutationSequence,
			AcceptedObservationSequence: runtimeState.AcceptedObservationSequence,
		}),
		payload,
	)
	if err != nil {
		return err
	}
	return subscriptions.PublishTrip(tripID, raw)
}

func authorizedState(ctx context.Context, stateStore *persistence.CanonicalStateStore, tripID, userID string) (persistence.CanonicalTripState, error) {
	state, err := stateStore.Load(ctx, tripID)
	if err != nil {
		return persistence.CanonicalTripState{}, err
	}
	if state.OwnerUserID != userID {
		return persistence.CanonicalTripState{}, gateway.ErrTripAccessDenied
	}
	return state, nil
}

func pendingProposal(ctx context.Context, proposals *persistence.ProposalStore, tripID string) ([]byte, error) {
	payload, err := proposals.LatestPendingPayload(ctx, tripID)
	if errors.Is(err, persistence.ErrPendingProposalNotFound) {
		return nil, nil
	}
	return payload, err
}

func runtimeVersions(supervisor *dispatch.RuntimeSupervisor, tripID string) dispatch.RuntimeVersions {
	if value, active := supervisor.RuntimeState(tripID); active {
		return value
	}
	return dispatch.RuntimeVersions{}
}

func runtimeSyncState(ctx context.Context, stateStore *persistence.CanonicalStateStore, state persistence.CanonicalTripState, runtime dispatch.RuntimeVersions, tripID string) (string, error) {
	if runtime.RuntimeEpoch != 0 {
		if runtime.AcceptedMutationSequence < state.FinalizedMutationSequence {
			return "pending", nil
		}
		return "synced", nil
	}
	return stateStore.RuntimeSyncState(ctx, tripID)
}

func messageID(message gateway.AuthenticatedMessage) string {
	value, _ := message.Message["message_id"].(string)
	return value
}

func requestedMessageIDs(message gateway.AuthenticatedMessage) []string {
	payload, _ := message.Message["payload"].(map[string]any)
	values, _ := payload["outstanding_message_ids"].([]any)
	result := make([]string, 0, len(values))
	for _, value := range values {
		if item, ok := value.(string); ok {
			result = append(result, item)
		}
	}
	return result
}

func commandsOutcomes(ctx context.Context, commands *persistence.CommandStore, tripID string, requested []string) ([]any, error) {
	if len(requested) == 0 {
		return []any{}, nil
	}
	stored, err := commands.LoadOutcomes(ctx, tripID, requested)
	if err != nil {
		return nil, err
	}
	byMessageID := make(map[string]persistence.CommandOutcome, len(stored))
	for _, outcome := range stored {
		byMessageID[outcome.MessageID] = outcome
	}
	result := make([]any, 0, len(requested))
	for _, messageID := range requested {
		outcome, exists := byMessageID[messageID]
		if !exists {
			value := map[string]any{
				"message_id": messageID, "phase": "rejected", "status": "NOT_FOUND",
				"retryable": false, "recovery_state": "current",
			}
			result = append(result, value)
			continue
		}
		value, err := gateway.BuildCommandOutcome(messageID, outcome)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, nil
}

func publishError(message gateway.AuthenticatedMessage, err error) {
	tripID, _ := message.Message["trip_id"].(string)
	messageID, _ := message.Message["message_id"].(string)
	if !canonicalUUID(tripID) || !canonicalUUID(messageID) || message.Sink == nil {
		return
	}
	status, retryable, staleReason := gateway.ErrorStatus(err)
	raw, buildErr := gateway.BuildTripErrorEnvelope(tripID, messageID, gateway.ZeroTripVersions(), status, retryable, "request could not be completed", staleReason)
	if buildErr == nil {
		_ = message.Sink.PublishServerEnvelope(raw)
	}
}

func publishErrorWithState(ctx context.Context, message gateway.AuthenticatedMessage, err error, stateStore *persistence.CanonicalStateStore, supervisor *dispatch.RuntimeSupervisor) {
	tripID, _ := message.Message["trip_id"].(string)
	messageID, _ := message.Message["message_id"].(string)
	if !canonicalUUID(tripID) || !canonicalUUID(messageID) || message.Sink == nil {
		return
	}
	versions := gateway.ZeroTripVersions()
	if state, loadErr := stateStore.Load(ctx, tripID); loadErr == nil && state.OwnerUserID == message.UserID {
		runtime := runtimeVersions(supervisor, tripID)
		versions = gateway.StateVersions(state, gateway.RuntimeVersions{
			RuntimeEpoch:                runtime.RuntimeEpoch,
			PlannerStateVersion:         runtime.PlannerStateVersion,
			AcceptedMutationSequence:    runtime.AcceptedMutationSequence,
			AcceptedObservationSequence: runtime.AcceptedObservationSequence,
		})
	}
	status, retryable, staleReason := gateway.ErrorStatus(err)
	raw, buildErr := gateway.BuildTripErrorEnvelope(tripID, messageID, versions, status, retryable, "request could not be completed", staleReason)
	if buildErr == nil {
		_ = message.Sink.PublishServerEnvelope(raw)
	}
}

func loadValidator() (*gateway.EnvelopeValidator, error) {
	client, err := os.ReadFile(envOr("LIVEROUTE_CLIENT_SCHEMA_PATH", "/app/schema/websocket/liveroute-v1-client-envelope.schema.json"))
	if err != nil {
		return nil, fmt.Errorf("read client schema: %w", err)
	}
	server, err := os.ReadFile(envOr("LIVEROUTE_SERVER_SCHEMA_PATH", "/app/schema/websocket/liveroute-v1-server-envelope.schema.json"))
	if err != nil {
		return nil, fmt.Errorf("read server schema: %w", err)
	}
	return gateway.NewEnvelopeValidator(client, server)
}

func reconnectDelay(initialMS, maximumMS int) func(uint64) time.Duration {
	return func(failures uint64) time.Duration {
		value := time.Duration(initialMS) * time.Millisecond
		for index := uint64(1); index < failures && value < time.Duration(maximumMS)*time.Millisecond; index++ {
			value *= 2
		}
		if value > time.Duration(maximumMS)*time.Millisecond {
			value = time.Duration(maximumMS) * time.Millisecond
		}
		return value
	}
}

func runWorker(ctx context.Context, name string, worker func(context.Context) error) {
	if err := worker(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Warn(name+" stopped", "error", err)
	}
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

func loadHTTPHMACKeys() (persistence.HMACKeyRing, error) {
	keyID := strings.TrimSpace(os.Getenv("LIVEROUTE_CSRF_HMAC_KEY_ID"))
	keyText := strings.TrimSpace(os.Getenv("LIVEROUTE_CSRF_HMAC_KEY"))
	if keyText == "" {
		path := envOr("LIVEROUTE_CSRF_HMAC_KEY_FILE", "/run/secrets/liveroute_csrf_hmac_key")
		raw, err := os.ReadFile(path)
		if err != nil {
			return persistence.HMACKeyRing{}, errors.New("LIVEROUTE_CSRF_HMAC_KEY or LIVEROUTE_CSRF_HMAC_KEY_FILE is required")
		}
		keyText = strings.TrimSpace(string(raw))
	}
	if keyID == "" {
		return persistence.HMACKeyRing{}, errors.New("LIVEROUTE_CSRF_HMAC_KEY_ID is required")
	}
	value, err := base64.RawURLEncoding.DecodeString(keyText)
	if err != nil || len(value) < sha256.Size {
		value = []byte(keyText)
	}
	return persistence.NewHMACKeyRing(persistence.HMACKey{ID: keyID, Value: value}, nil)
}

func loadOptionalSecret(valueEnvironment, fileEnvironment, defaultPath string) (string, bool, error) {
	if value := strings.TrimSpace(os.Getenv(valueEnvironment)); value != "" {
		return value, true, nil
	}
	path := envOr(fileEnvironment, defaultPath)
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read %s: %w", fileEnvironment, err)
	}
	value := strings.TrimSpace(string(raw))
	if value == "" {
		return "", false, fmt.Errorf("%s is empty", fileEnvironment)
	}
	return value, true, nil
}

func canonicalUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' || strings.ToLower(value) != value {
		return false
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}
