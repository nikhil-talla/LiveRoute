package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	ProtocolVersion    string    `yaml:"protocol_version"`
	Limits             Limits    `yaml:"limits"`
	Runtime            Runtime   `yaml:"runtime"`
	Executors          Executors `yaml:"executors"`
	Stream             Stream    `yaml:"stream"`
	WebSocket          WebSocket `yaml:"websocket"`
	Database           Database  `yaml:"database"`
	Planner            Planner   `yaml:"planner"`
	Hours              Hours     `yaml:"hours"`
	Routing            Routing   `yaml:"routing"`
	ShutdownDeadlineMS int       `yaml:"shutdown_deadline_ms"`
}

type Limits struct {
	WebSocketFrameBytes                    int `yaml:"websocket_frame_bytes"`
	WebSocketDecodedMessageBytes           int `yaml:"websocket_decoded_message_bytes"`
	GRPCMessageBytes                       int `yaml:"grpc_message_bytes"`
	SnapshotPayloadBytes                   int `yaml:"snapshot_payload_bytes"`
	ResynchronizationOutstandingCommandIDs int `yaml:"resynchronization_outstanding_command_ids"`
}

type Runtime struct {
	ShardCount              int `yaml:"shard_count"`
	MaxActiveTrips          int `yaml:"max_active_trips"`
	MaxActivitiesPerTrip    int `yaml:"max_activities_per_trip"`
	CriticalQueueCapacity   int `yaml:"critical_queue_capacity"`
	HighQueueCapacity       int `yaml:"high_queue_capacity"`
	NormalQueueCapacity     int `yaml:"normal_queue_capacity"`
	AdvisoryQueueCapacity   int `yaml:"advisory_queue_capacity"`
	CompletionQueueCapacity int `yaml:"completion_queue_capacity"`
	PriorityFairnessBurst   int `yaml:"priority_fairness_burst"`
}

type Executors struct {
	ProviderWorkers       int `yaml:"provider_workers"`
	ProviderQueueCapacity int `yaml:"provider_queue_capacity"`
	PlannerWorkers        int `yaml:"planner_workers"`
	PlannerQueueCapacity  int `yaml:"planner_queue_capacity"`
}

type Stream struct {
	PoolSize                  int `yaml:"pool_size"`
	InboundQueueCapacity      int `yaml:"inbound_queue_capacity"`
	OutboundQueueCapacity     int `yaml:"outbound_queue_capacity"`
	ReconnectInitialBackoffMS int `yaml:"reconnect_initial_backoff_ms"`
	ReconnectMaxBackoffMS     int `yaml:"reconnect_max_backoff_ms"`
}

type WebSocket struct {
	InboundQueueCapacity        int      `yaml:"inbound_queue_capacity"`
	OutboundQueueCapacity       int      `yaml:"outbound_queue_capacity"`
	HeartbeatIntervalMS         int      `yaml:"heartbeat_interval_ms"`
	IdleTimeoutMS               int      `yaml:"idle_timeout_ms"`
	AuthenticationTimeoutMS     int      `yaml:"authentication_timeout_ms"`
	AllowOriginlessLocalClients bool     `yaml:"allow_originless_local_clients"`
	AllowedOrigins              []string `yaml:"allowed_origins"`
}

type Database struct {
	PoolMaxConnections   int `yaml:"pool_max_connections"`
	OutboxClaimBatchSize int `yaml:"outbox_claim_batch_size"`
	OutboxMaxInFlight    int `yaml:"outbox_max_in_flight"`
	LeaseDurationMS      int `yaml:"lease_duration_ms"`
	LeaseRenewalMarginMS int `yaml:"lease_renewal_margin_ms"`
	LeaseSafetyMarginMS  int `yaml:"lease_safety_margin_ms"`
}

type Planner struct {
	AttemptTimeoutMS int `yaml:"attempt_timeout_ms"`
	MaxCandidates    int `yaml:"max_candidates"`
	BeamWidth        int `yaml:"beam_width"`
	MaxExpansions    int `yaml:"max_expansions"`
}

type Hours struct {
	SeedFilePath       string `yaml:"seed_file_path"`
	SeedFileSHA256     string `yaml:"seed_file_sha256"`
	TZDataRelease      string `yaml:"tzdata_release"`
	TZDataZoneInfoPath string `yaml:"tzdata_zoneinfo_path"`
}

type Routing struct {
	DatasetVersion         string `yaml:"dataset_version"`
	CarEndpoint            string `yaml:"car_endpoint"`
	FootEndpoint           string `yaml:"foot_endpoint"`
	MaxLocations           int    `yaml:"max_locations"`
	MaxMatrixCells         int    `yaml:"max_matrix_cells"`
	MaxEncodedRequestBytes int    `yaml:"max_encoded_request_bytes"`
	MaxResponseBytes       int    `yaml:"max_response_bytes"`
	PerProfileConcurrency  int    `yaml:"per_profile_concurrency"`
	ConnectTimeoutMS       int    `yaml:"connect_timeout_ms"`
	RequestTimeoutMS       int    `yaml:"request_timeout_ms"`
}

func Load(path string) (Config, error) {
	if path == "" {
		return Config{}, errors.New("configuration path is required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read configuration: %w", err)
	}
	var result Config
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&result); err != nil {
		return Config{}, fmt.Errorf("decode configuration: %w", err)
	}
	if err := result.Validate(); err != nil {
		return Config{}, err
	}
	return result, nil
}

func (config Config) Validate() error {
	if config.ProtocolVersion != "liveroute.v1" {
		return errors.New("configuration protocol_version must be liveroute.v1")
	}
	positive := map[string]int{
		"limits.websocket_frame_bytes":                     config.Limits.WebSocketFrameBytes,
		"limits.websocket_decoded_message_bytes":           config.Limits.WebSocketDecodedMessageBytes,
		"limits.grpc_message_bytes":                        config.Limits.GRPCMessageBytes,
		"limits.snapshot_payload_bytes":                    config.Limits.SnapshotPayloadBytes,
		"limits.resynchronization_outstanding_command_ids": config.Limits.ResynchronizationOutstandingCommandIDs,
		"runtime.shard_count":                              config.Runtime.ShardCount,
		"runtime.max_active_trips":                         config.Runtime.MaxActiveTrips,
		"runtime.max_activities_per_trip":                  config.Runtime.MaxActivitiesPerTrip,
		"runtime.critical_queue_capacity":                  config.Runtime.CriticalQueueCapacity,
		"runtime.high_queue_capacity":                      config.Runtime.HighQueueCapacity,
		"runtime.normal_queue_capacity":                    config.Runtime.NormalQueueCapacity,
		"runtime.advisory_queue_capacity":                  config.Runtime.AdvisoryQueueCapacity,
		"runtime.completion_queue_capacity":                config.Runtime.CompletionQueueCapacity,
		"runtime.priority_fairness_burst":                  config.Runtime.PriorityFairnessBurst,
		"executors.provider_workers":                       config.Executors.ProviderWorkers,
		"executors.provider_queue_capacity":                config.Executors.ProviderQueueCapacity,
		"executors.planner_workers":                        config.Executors.PlannerWorkers,
		"executors.planner_queue_capacity":                 config.Executors.PlannerQueueCapacity,
		"stream.pool_size":                                 config.Stream.PoolSize,
		"stream.inbound_queue_capacity":                    config.Stream.InboundQueueCapacity,
		"stream.outbound_queue_capacity":                   config.Stream.OutboundQueueCapacity,
		"stream.reconnect_initial_backoff_ms":              config.Stream.ReconnectInitialBackoffMS,
		"stream.reconnect_max_backoff_ms":                  config.Stream.ReconnectMaxBackoffMS,
		"websocket.inbound_queue_capacity":                 config.WebSocket.InboundQueueCapacity,
		"websocket.outbound_queue_capacity":                config.WebSocket.OutboundQueueCapacity,
		"websocket.heartbeat_interval_ms":                  config.WebSocket.HeartbeatIntervalMS,
		"websocket.idle_timeout_ms":                        config.WebSocket.IdleTimeoutMS,
		"websocket.authentication_timeout_ms":              config.WebSocket.AuthenticationTimeoutMS,
		"database.pool_max_connections":                    config.Database.PoolMaxConnections,
		"database.outbox_claim_batch_size":                 config.Database.OutboxClaimBatchSize,
		"database.outbox_max_in_flight":                    config.Database.OutboxMaxInFlight,
		"database.lease_duration_ms":                       config.Database.LeaseDurationMS,
		"database.lease_renewal_margin_ms":                 config.Database.LeaseRenewalMarginMS,
		"database.lease_safety_margin_ms":                  config.Database.LeaseSafetyMarginMS,
		"planner.attempt_timeout_ms":                       config.Planner.AttemptTimeoutMS,
		"planner.max_candidates":                           config.Planner.MaxCandidates,
		"planner.beam_width":                               config.Planner.BeamWidth,
		"planner.max_expansions":                           config.Planner.MaxExpansions,
		"routing.max_locations":                            config.Routing.MaxLocations,
		"routing.max_matrix_cells":                         config.Routing.MaxMatrixCells,
		"routing.max_encoded_request_bytes":                config.Routing.MaxEncodedRequestBytes,
		"routing.max_response_bytes":                       config.Routing.MaxResponseBytes,
		"routing.per_profile_concurrency":                  config.Routing.PerProfileConcurrency,
		"routing.connect_timeout_ms":                       config.Routing.ConnectTimeoutMS,
		"routing.request_timeout_ms":                       config.Routing.RequestTimeoutMS,
		"shutdown_deadline_ms":                             config.ShutdownDeadlineMS,
	}
	for name, value := range positive {
		if value <= 0 {
			return fmt.Errorf("%s must be positive", name)
		}
	}
	if config.Limits.WebSocketFrameBytes > 262144 || config.Limits.WebSocketDecodedMessageBytes > 262144 ||
		config.Limits.GRPCMessageBytes > 4*1024*1024 || config.Limits.SnapshotPayloadBytes > 2*1024*1024 ||
		config.Limits.ResynchronizationOutstandingCommandIDs > 128 {
		return errors.New("configuration exceeds fixed V1 protocol limits")
	}
	if config.Stream.ReconnectInitialBackoffMS > config.Stream.ReconnectMaxBackoffMS ||
		config.Database.LeaseRenewalMarginMS >= config.Database.LeaseDurationMS ||
		config.Database.LeaseSafetyMarginMS >= config.Database.LeaseDurationMS ||
		config.Planner.AttemptTimeoutMS >= config.Database.LeaseDurationMS {
		return errors.New("configuration timing bounds are inconsistent")
	}
	if len(config.WebSocket.AllowedOrigins) == 0 || config.Hours.SeedFilePath == "" ||
		len(config.Hours.SeedFileSHA256) != 64 || config.Hours.TZDataRelease == "" ||
		config.Hours.TZDataZoneInfoPath == "" || config.Routing.DatasetVersion == "" ||
		config.Routing.CarEndpoint == "" || config.Routing.FootEndpoint == "" {
		return errors.New("configuration origin and hours settings are incomplete")
	}
	return nil
}
