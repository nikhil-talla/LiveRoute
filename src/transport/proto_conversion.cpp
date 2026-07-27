#include "liveroute/transport/proto_conversion.hpp"

#include <algorithm>
#include <array>
#include <charconv>
#include <cmath>
#include <cstddef>
#include <limits>
#include <string_view>
#include <utility>
#include <vector>

#include "liveroute/providers/sha256.hpp"

namespace liveroute::transport {
namespace {

template <typename Value>
[[nodiscard]] ConversionResult<Value> failed(std::string message) {
  return {.value = std::nullopt,
          .error = ConversionError{.safe_message = std::move(message)}};
}

[[nodiscard]] int hex_value(char value) noexcept {
  if (value >= '0' && value <= '9') return value - '0';
  if (value >= 'a' && value <= 'f') return value - 'a' + 10;
  return -1;
}

[[nodiscard]] std::optional<std::array<std::byte, 16>> parse_uuid(
    std::string_view value) noexcept {
  if (value.size() != 36 || value[8] != '-' || value[13] != '-' ||
      value[18] != '-' || value[23] != '-') {
    return std::nullopt;
  }
  std::array<std::byte, 16> bytes{};
  std::size_t output = 0;
  for (std::size_t index = 0; index < value.size();) {
    if (value[index] == '-') {
      ++index;
      continue;
    }
    if (index + 1 >= value.size() || output >= bytes.size()) {
      return std::nullopt;
    }
    const auto high = hex_value(value[index]);
    const auto low = hex_value(value[index + 1]);
    if (high < 0 || low < 0) return std::nullopt;
    bytes[output++] =
        static_cast<std::byte>(static_cast<unsigned>((high << 4) | low));
    index += 2;
  }
  if (output != bytes.size()) return std::nullopt;
  return bytes;
}

[[nodiscard]] std::string format_uuid(
    const std::array<std::byte, 16>& value) {
  constexpr char kHex[] = "0123456789abcdef";
  std::string output;
  output.reserve(36);
  for (std::size_t index = 0; index < value.size(); ++index) {
    if (index == 4 || index == 6 || index == 8 || index == 10) {
      output.push_back('-');
    }
    const auto byte = std::to_integer<unsigned>(value[index]);
    output.push_back(kHex[byte >> 4U]);
    output.push_back(kHex[byte & 0x0fU]);
  }
  return output;
}

template <typename Id>
[[nodiscard]] std::optional<Id> parse_id(const std::string& value) {
  const auto bytes = parse_uuid(value);
  if (!bytes.has_value()) return std::nullopt;
  return Id{*bytes};
}

[[nodiscard]] std::optional<domain::TravelMode> travel_mode_from_proto(
    ::liveroute::v1::TravelMode value) noexcept {
  switch (value) {
    case ::liveroute::v1::TRAVEL_MODE_WALKING:
      return domain::TravelMode::kWalking;
    case ::liveroute::v1::TRAVEL_MODE_DRIVING:
      return domain::TravelMode::kDriving;
    default:
      return std::nullopt;
  }
}

[[nodiscard]] std::optional<domain::ActivityClass>
activity_class_from_proto(::liveroute::v1::ActivityClass value) noexcept {
  switch (value) {
    case ::liveroute::v1::ACTIVITY_CLASS_FIXED:
      return domain::ActivityClass::kFixed;
    case ::liveroute::v1::ACTIVITY_CLASS_FLEXIBLE:
      return domain::ActivityClass::kFlexible;
    default:
      return std::nullopt;
  }
}

[[nodiscard]] std::optional<domain::ActivityState>
activity_state_from_proto(::liveroute::v1::ActivityState value) noexcept {
  switch (value) {
    case ::liveroute::v1::ACTIVITY_STATE_PLANNED:
      return domain::ActivityState::kPlanned;
    case ::liveroute::v1::ACTIVITY_STATE_STARTED:
      return domain::ActivityState::kStarted;
    case ::liveroute::v1::ACTIVITY_STATE_COMPLETED:
      return domain::ActivityState::kCompleted;
    case ::liveroute::v1::ACTIVITY_STATE_SKIPPED:
      return domain::ActivityState::kSkipped;
    default:
      return std::nullopt;
  }
}

[[nodiscard]] std::optional<domain::PlanOrigin> plan_origin_from_proto(
    ::liveroute::v1::PlanOrigin value) noexcept {
  switch (value) {
    case ::liveroute::v1::PLAN_ORIGIN_USER_AUTHORED:
      return domain::PlanOrigin::kUserAuthored;
    case ::liveroute::v1::PLAN_ORIGIN_ACCEPTED_ENGINE_PROPOSAL:
      return domain::PlanOrigin::kAcceptedEngineProposal;
    default:
      return std::nullopt;
  }
}

[[nodiscard]] std::optional<domain::PlanEntryState>
plan_entry_state_from_proto(::liveroute::v1::PlanEntryState value) noexcept {
  switch (value) {
    case ::liveroute::v1::PLAN_ENTRY_STATE_SCHEDULED:
      return domain::PlanEntryState::kScheduled;
    case ::liveroute::v1::PLAN_ENTRY_STATE_OMITTED:
      return domain::PlanEntryState::kOmitted;
    default:
      return std::nullopt;
  }
}

[[nodiscard]] std::optional<domain::Location> location_from_proto(
    const ::liveroute::v1::Location& value) {
  domain::Location result{value.latitude(), value.longitude()};
  if (!result.is_valid()) return std::nullopt;
  return result;
}

[[nodiscard]] std::optional<domain::TimeWindow> window_from_proto(
    const ::liveroute::v1::TimeWindow& value) {
  domain::TimeWindow result{
      domain::UnixTimeMilliseconds{value.opens_at_unix_ms()},
      domain::UnixTimeMilliseconds{value.closes_at_unix_ms()}};
  if (!result.is_valid()) return std::nullopt;
  return result;
}

[[nodiscard]] std::optional<domain::ActivityTiming> timing_from_proto(
    const ::liveroute::v1::ActivityTiming& value) {
  domain::ActivityTiming result;
  result.open_windows.reserve(
      static_cast<std::size_t>(value.open_windows_size()));
  for (const auto& window : value.open_windows()) {
    auto converted = window_from_proto(window);
    if (!converted.has_value()) return std::nullopt;
    result.open_windows.push_back(*converted);
  }
  if (value.has_reservation_start_unix_ms()) {
    result.reservation_start =
        domain::UnixTimeMilliseconds{value.reservation_start_unix_ms()};
  }
  result.reservation_grace_seconds = value.reservation_grace_seconds();
  result.min_duration_seconds = value.min_duration_seconds();
  result.preferred_duration_seconds = value.preferred_duration_seconds();
  result.max_duration_seconds = value.max_duration_seconds();
  result.mandatory = value.mandatory();
  result.can_shorten = value.can_shorten();
  result.can_move = value.can_move();
  result.can_skip = value.can_skip();
  if (value.has_mandatory_deadline_unix_ms()) {
    result.mandatory_deadline =
        domain::UnixTimeMilliseconds{value.mandatory_deadline_unix_ms()};
  }
  if (!result.is_valid()) return std::nullopt;
  return result;
}

[[nodiscard]] std::optional<domain::Activity> activity_from_proto(
    const ::liveroute::v1::Activity& value) {
  const auto activity_id = parse_id<domain::ActivityId>(value.activity_id());
  const auto location = value.has_location()
                            ? location_from_proto(value.location())
                            : std::nullopt;
  const auto mode = travel_mode_from_proto(value.inbound_travel_mode());
  const auto activity_class =
      activity_class_from_proto(value.activity_class());
  const auto activity_state =
      activity_state_from_proto(value.activity_state());
  const auto timing =
      value.has_timing() ? timing_from_proto(value.timing()) : std::nullopt;
  if (!activity_id || !location || !mode || !activity_class ||
      !activity_state || !timing) {
    return std::nullopt;
  }
  domain::Activity result{
      .activity_id = *activity_id,
      .place_id = domain::PlaceId{value.place_id()},
      .display_name = value.display_name(),
      .location = *location,
      .time_zone_name = value.time_zone_name(),
      .inbound_travel_mode = *mode,
      .activity_class = *activity_class,
      .activity_state = *activity_state,
      .priority_rank = value.priority_rank(),
      .utility_score = value.utility_score(),
      .timing = *timing,
      .activity_delay_seconds = value.activity_delay_seconds(),
      .found_closed_at = std::nullopt,
  };
  if (value.has_found_closed_at_unix_ms()) {
    result.found_closed_at =
        domain::UnixTimeMilliseconds{value.found_closed_at_unix_ms()};
  }
  if (!result.is_valid()) return std::nullopt;
  return result;
}

[[nodiscard]] std::optional<domain::CurrentPlan> current_plan_from_proto(
    const ::liveroute::v1::CurrentPlan& value) {
  const auto plan_id = parse_id<domain::PlanId>(value.plan_id());
  const auto origin = plan_origin_from_proto(value.origin());
  if (!plan_id || !origin) return std::nullopt;
  domain::CurrentPlan result{
      .plan_id = *plan_id,
      .plan_revision = value.plan_revision(),
      .origin = *origin,
      .segments = {},
      .created_at =
          domain::UnixTimeMilliseconds{value.created_at_unix_ms()},
      .source_proposal_id = std::nullopt,
  };
  if (value.has_source_proposal_id()) {
    result.source_proposal_id =
        parse_id<domain::ProposalId>(value.source_proposal_id());
    if (!result.source_proposal_id.has_value()) return std::nullopt;
  }
  result.segments.reserve(static_cast<std::size_t>(value.segments_size()));
  for (const auto& segment : value.segments()) {
    const auto activity_id =
        parse_id<domain::ActivityId>(segment.activity_id());
    const auto state = plan_entry_state_from_proto(segment.state());
    if (!activity_id || !state) return std::nullopt;
    domain::CurrentPlanSegment converted{
        .activity_id = *activity_id,
        .state = *state,
        .scheduled_start = std::nullopt,
        .scheduled_end = std::nullopt};
    if (segment.has_scheduled_start_unix_ms()) {
      converted.scheduled_start = domain::UnixTimeMilliseconds{
          segment.scheduled_start_unix_ms()};
    }
    if (segment.has_scheduled_end_unix_ms()) {
      converted.scheduled_end =
          domain::UnixTimeMilliseconds{segment.scheduled_end_unix_ms()};
    }
    if (!converted.is_valid()) return std::nullopt;
    result.segments.push_back(converted);
  }
  return result;
}

[[nodiscard]] std::optional<domain::TripState> trip_from_proto(
    const ::liveroute::v1::TripDefinition& trip,
    const ::liveroute::v1::CurrentPlan& plan) {
  const auto trip_id = parse_id<domain::TripId>(trip.trip_id());
  const auto owner_id = parse_uuid(trip.owner_user_id());
  const auto current_plan = current_plan_from_proto(plan);
  const auto declared_plan =
      parse_id<domain::PlanId>(trip.current_plan_id());
  if (!trip_id || !owner_id || !current_plan || !declared_plan ||
      *declared_plan != current_plan->plan_id) {
    return std::nullopt;
  }
  domain::TripState result{
      .trip_id = *trip_id,
      .default_time_zone_name = trip.default_time_zone_name(),
      .activities = {},
      .completed_prefix_count = trip.completed_prefix_count(),
      .current_activity_id = std::nullopt,
      .current_plan = *current_plan,
      .travel_delays = {},
      .current_observation = {},
      .active_proposal = std::nullopt,
  };
  if (!trip.current_activity_id().empty()) {
    result.current_activity_id =
        parse_id<domain::ActivityId>(trip.current_activity_id());
    if (!result.current_activity_id.has_value()) return std::nullopt;
  }
  result.activities.reserve(static_cast<std::size_t>(trip.activities_size()));
  for (const auto& activity : trip.activities()) {
    auto converted = activity_from_proto(activity);
    if (!converted.has_value()) return std::nullopt;
    result.activities.push_back(std::move(*converted));
  }
  result.travel_delays.reserve(
      static_cast<std::size_t>(trip.travel_delays_size()));
  for (const auto& delay : trip.travel_delays()) {
    const auto from =
        parse_id<domain::ActivityId>(delay.from_activity_id());
    const auto to = parse_id<domain::ActivityId>(delay.to_activity_id());
    if (!from || !to) return std::nullopt;
    result.travel_delays.push_back(
        {.from_activity_id = *from,
         .to_activity_id = *to,
         .additional_seconds = delay.additional_seconds(),
         .observed_at =
             domain::UnixTimeMilliseconds{delay.observed_at_unix_ms()}});
  }
  if (!result.is_valid()) return std::nullopt;
  return result;
}

[[nodiscard]] bool apply_observation(
    const ::liveroute::v1::CurrentObservation& value,
    domain::TripState& state) {
  if (!value.has_location()) return false;
  const auto location = location_from_proto(value.location());
  if (!location) return false;
  state.current_observation.location = *location;
  state.current_observation.observed_at =
      domain::UnixTimeMilliseconds{value.observed_at_unix_ms()};
  if (value.has_velocity_meters_per_second()) {
    state.current_observation.velocity_meters_per_second =
        value.velocity_meters_per_second();
  }
  if (value.has_heading_degrees()) {
    state.current_observation.heading_degrees = value.heading_degrees();
  }
  return state.current_observation.is_valid();
}

[[nodiscard]] std::optional<domain::TripState> state_from_snapshot(
    const ::liveroute::v1::SnapshotBlob& blob,
    runtime::TripRuntimeVersionSnapshot& metadata) {
  if (blob.snapshot_schema_version() != 1 ||
      blob.payload_size_bytes() != blob.payload().size() ||
      blob.checksum_sha256().size() != 32) {
    return std::nullopt;
  }
  const auto checksum = providers::sha256_hex(blob.payload());
  std::string checksum_bytes;
  checksum_bytes.reserve(32);
  for (std::size_t index = 0; index < checksum.size(); index += 2) {
    const auto high = hex_value(checksum[index]);
    const auto low = hex_value(checksum[index + 1]);
    checksum_bytes.push_back(static_cast<char>((high << 4) | low));
  }
  if (checksum_bytes != blob.checksum_sha256()) return std::nullopt;

  ::liveroute::v1::TripStateSnapshot payload;
  if (!payload.ParseFromString(blob.payload()) ||
      payload.snapshot_schema_version() != 1 ||
      !payload.has_trip() || !payload.has_current_plan() ||
      payload.trip_revision() != blob.trip_revision() ||
      payload.finalized_mutation_sequence() !=
          blob.covered_finalized_mutation_sequence() ||
      payload.accepted_mutation_sequence() !=
          payload.finalized_mutation_sequence()) {
    return std::nullopt;
  }
  auto state = trip_from_proto(payload.trip(), payload.current_plan());
  if (!state.has_value()) return std::nullopt;
  metadata = {.runtime_epoch = blob.source_runtime_epoch(),
              .trip_revision = payload.trip_revision(),
              .planner_state_version = blob.source_planner_state_version(),
              .planning_generation = 0,
              .accepted_mutation_sequence =
                  payload.accepted_mutation_sequence(),
              .finalized_mutation_sequence =
                  payload.finalized_mutation_sequence(),
              .accepted_observation_sequence = 0};
  return state;
}

[[nodiscard]] std::optional<domain::TripEventPayload> event_payload_from_proto(
    const ::liveroute::v1::ApplyTripEvent& event) {
  using Input = ::liveroute::v1::ApplyTripEvent;
  switch (event.event_case()) {
    case Input::kLocationUpdated: {
      if (!event.location_updated().has_location()) return std::nullopt;
      const auto location =
          location_from_proto(event.location_updated().location());
      if (!location) return std::nullopt;
      return domain::LocationUpdated{*location};
    }
    case Input::kVelocityUpdated:
      return domain::VelocityUpdated{
          event.velocity_updated().meters_per_second()};
    case Input::kHeadingUpdated:
      return domain::HeadingUpdated{event.heading_updated().degrees()};
    case Input::kActivityStatusChanged: {
      const auto id = parse_id<domain::ActivityId>(
          event.activity_status_changed().activity_id());
      const auto state =
          activity_state_from_proto(event.activity_status_changed().state());
      if (!id || !state) return std::nullopt;
      return domain::ActivityStatusChanged{*id, *state};
    }
    case Input::kActivityDelayed: {
      const auto id = parse_id<domain::ActivityId>(
          event.activity_delayed().activity_id());
      if (!id) return std::nullopt;
      return domain::ActivityDelayed{
          *id, event.activity_delayed().delay_seconds()};
    }
    case Input::kTripEdited: {
      const auto& edit = event.trip_edited();
      if (!edit.has_resulting_current_plan()) return std::nullopt;
      auto plan = current_plan_from_proto(edit.resulting_current_plan());
      if (!plan) return std::nullopt;
      std::optional<domain::TripEditOperation> operation;
      switch (edit.operation_case()) {
        case ::liveroute::v1::TripEdited::kAdd: {
          if (!edit.add().has_activity()) return std::nullopt;
          auto activity = activity_from_proto(edit.add().activity());
          if (!activity) return std::nullopt;
          operation =
              domain::AddActivity{std::move(*activity), edit.add().ordinal()};
          break;
        }
        case ::liveroute::v1::TripEdited::kReplace: {
          if (!edit.replace().has_activity()) return std::nullopt;
          auto activity = activity_from_proto(edit.replace().activity());
          if (!activity) return std::nullopt;
          operation = domain::ReplaceActivity{std::move(*activity)};
          break;
        }
        case ::liveroute::v1::TripEdited::kRemove: {
          const auto id =
              parse_id<domain::ActivityId>(edit.remove().activity_id());
          if (!id) return std::nullopt;
          operation = domain::RemoveActivity{*id};
          break;
        }
        case ::liveroute::v1::TripEdited::kReorder: {
          std::vector<domain::ActivityId> ids;
          ids.reserve(
              static_cast<std::size_t>(edit.reorder().activity_ids_size()));
          for (const auto& text : edit.reorder().activity_ids()) {
            const auto id = parse_id<domain::ActivityId>(text);
            if (!id) return std::nullopt;
            ids.push_back(*id);
          }
          operation = domain::ReorderActivities{std::move(ids)};
          break;
        }
        default:
          return std::nullopt;
      }
      return domain::TripEdited{std::move(*operation), std::move(*plan)};
    }
    case Input::kReservationChanged: {
      const auto& input = event.reservation_changed();
      const auto id = parse_id<domain::ActivityId>(input.activity_id());
      if (!id) return std::nullopt;
      std::optional<domain::UnixTimeMilliseconds> start;
      if (input.has_reservation_start_unix_ms()) {
        start = domain::UnixTimeMilliseconds{
            input.reservation_start_unix_ms()};
      }
      return domain::ReservationChanged{
          *id, start, input.reservation_grace_seconds()};
    }
    case Input::kMandatoryDeadlineChanged: {
      const auto& input = event.mandatory_deadline_changed();
      const auto id = parse_id<domain::ActivityId>(input.activity_id());
      if (!id) return std::nullopt;
      return domain::MandatoryDeadlineChanged{
          *id,
          domain::UnixTimeMilliseconds{input.latest_finish_unix_ms()}};
    }
    case Input::kRouteDeviationDetected: {
      const auto& input = event.route_deviation_detected();
      if (!input.has_location()) return std::nullopt;
      const auto location = location_from_proto(input.location());
      if (!location) return std::nullopt;
      return domain::RouteDeviationDetected{
          *location, input.distance_from_route_meters()};
    }
    case Input::kOperatingHoursChanged: {
      const auto& input = event.operating_hours_changed();
      const auto id = parse_id<domain::ActivityId>(input.activity_id());
      if (!id) return std::nullopt;
      std::vector<domain::TimeWindow> windows;
      windows.reserve(static_cast<std::size_t>(input.open_windows_size()));
      for (const auto& window : input.open_windows()) {
        const auto converted = window_from_proto(window);
        if (!converted) return std::nullopt;
        windows.push_back(*converted);
      }
      return domain::OperatingHoursChanged{*id, std::move(windows)};
    }
    case Input::kPlaceFoundClosed: {
      const auto& input = event.place_found_closed();
      const auto id = parse_id<domain::ActivityId>(input.activity_id());
      if (!id) return std::nullopt;
      return domain::PlaceFoundClosed{
          *id, domain::UnixTimeMilliseconds{input.observed_at_unix_ms()}};
    }
    case Input::kTravelDelay: {
      const auto& input = event.travel_delay();
      const auto from =
          parse_id<domain::ActivityId>(input.from_activity_id());
      const auto to = parse_id<domain::ActivityId>(input.to_activity_id());
      if (!from || !to) return std::nullopt;
      return domain::TravelDelay{*from, *to, input.additional_seconds()};
    }
    case Input::kPlanDecision: {
      const auto& input = event.plan_decision();
      const auto proposal =
          parse_id<domain::ProposalId>(input.proposal_id());
      const auto base = parse_id<domain::PlanId>(input.base_current_plan_id());
      if (!proposal || !base) return std::nullopt;
      domain::PlanDecision decision;
      if (input.decision() == ::liveroute::v1::PLAN_DECISION_ACCEPT) {
        decision = domain::PlanDecision::kAccept;
      } else if (input.decision() ==
                 ::liveroute::v1::PLAN_DECISION_REJECT) {
        decision = domain::PlanDecision::kReject;
      } else {
        return std::nullopt;
      }
      std::optional<domain::CurrentPlan> plan;
      if (input.has_resulting_current_plan()) {
        plan = current_plan_from_proto(input.resulting_current_plan());
        if (!plan) return std::nullopt;
      }
      return domain::PlanDecisionEvent{
          decision,
          *proposal,
          input.source_runtime_epoch(),
          domain::PlannerStateVersion{
              input.source_planner_state_version()},
          *base,
          std::move(plan)};
    }
    case Input::kAdvisoryUpdate: {
      const auto& input = event.advisory_update();
      domain::AdvisoryKind kind;
      switch (input.kind()) {
        case ::liveroute::v1::ADVISORY_KIND_RECOMMENDATION_REFRESH:
          kind = domain::AdvisoryKind::kRecommendationRefresh;
          break;
        case ::liveroute::v1::ADVISORY_KIND_WEATHER_CHANGED:
          kind = domain::AdvisoryKind::kWeatherChanged;
          break;
        case ::liveroute::v1::ADVISORY_KIND_CROWD_CHANGED:
          kind = domain::AdvisoryKind::kCrowdChanged;
          break;
        case ::liveroute::v1::ADVISORY_KIND_SOCIAL_UPDATE:
          kind = domain::AdvisoryKind::kSocialUpdate;
          break;
        default:
          return std::nullopt;
      }
      std::vector<std::byte> payload(input.opaque_payload().size());
      std::transform(input.opaque_payload().begin(),
                     input.opaque_payload().end(), payload.begin(),
                     [](char value) {
                       return static_cast<std::byte>(
                           static_cast<unsigned char>(value));
                     });
      return domain::AdvisoryUpdate{
          kind, input.source(), std::move(payload)};
    }
    case Input::kCurrentPlanReplaced: {
      const auto& input = event.current_plan_replaced();
      if (!input.has_current_plan()) return std::nullopt;
      auto plan = current_plan_from_proto(input.current_plan());
      if (!plan) return std::nullopt;
      return domain::CurrentPlanReplaced{std::move(*plan)};
    }
    default:
      return std::nullopt;
  }
}

void location_to_proto(const domain::Location& source,
                       ::liveroute::v1::Location& destination) {
  destination.set_latitude(source.latitude);
  destination.set_longitude(source.longitude);
}

void current_plan_to_proto(const domain::CurrentPlan& source,
                           ::liveroute::v1::CurrentPlan& destination) {
  destination.set_plan_id(format_uuid(source.plan_id.value()));
  destination.set_plan_revision(source.plan_revision);
  destination.set_origin(
      source.origin == domain::PlanOrigin::kUserAuthored
          ? ::liveroute::v1::PLAN_ORIGIN_USER_AUTHORED
          : ::liveroute::v1::PLAN_ORIGIN_ACCEPTED_ENGINE_PROPOSAL);
  for (const auto& segment : source.segments) {
    auto* output = destination.add_segments();
    output->set_activity_id(format_uuid(segment.activity_id.value()));
    output->set_state(
        segment.state == domain::PlanEntryState::kScheduled
            ? ::liveroute::v1::PLAN_ENTRY_STATE_SCHEDULED
            : ::liveroute::v1::PLAN_ENTRY_STATE_OMITTED);
    if (segment.scheduled_start) {
      output->set_scheduled_start_unix_ms(
          segment.scheduled_start->value());
    }
    if (segment.scheduled_end) {
      output->set_scheduled_end_unix_ms(segment.scheduled_end->value());
    }
  }
  destination.set_created_at_unix_ms(source.created_at.value());
  if (source.source_proposal_id) {
    destination.set_source_proposal_id(
        format_uuid(source.source_proposal_id->value()));
  }
}

void activity_to_proto(const domain::Activity& source,
                       ::liveroute::v1::Activity& destination) {
  destination.set_activity_id(format_uuid(source.activity_id.value()));
  destination.set_place_id(source.place_id.value);
  destination.set_display_name(source.display_name);
  location_to_proto(source.location, *destination.mutable_location());
  destination.set_time_zone_name(source.time_zone_name);
  destination.set_inbound_travel_mode(
      source.inbound_travel_mode == domain::TravelMode::kWalking
          ? ::liveroute::v1::TRAVEL_MODE_WALKING
          : ::liveroute::v1::TRAVEL_MODE_DRIVING);
  destination.set_activity_class(
      source.activity_class == domain::ActivityClass::kFixed
          ? ::liveroute::v1::ACTIVITY_CLASS_FIXED
          : ::liveroute::v1::ACTIVITY_CLASS_FLEXIBLE);
  constexpr std::array<::liveroute::v1::ActivityState, 4> kStates{
      ::liveroute::v1::ACTIVITY_STATE_PLANNED,
      ::liveroute::v1::ACTIVITY_STATE_STARTED,
      ::liveroute::v1::ACTIVITY_STATE_COMPLETED,
      ::liveroute::v1::ACTIVITY_STATE_SKIPPED};
  destination.set_activity_state(
      kStates[static_cast<std::size_t>(source.activity_state)]);
  destination.set_priority_rank(source.priority_rank);
  destination.set_utility_score(source.utility_score);
  auto* timing = destination.mutable_timing();
  for (const auto& window : source.timing.open_windows) {
    auto* output = timing->add_open_windows();
    output->set_opens_at_unix_ms(window.opens_at.value());
    output->set_closes_at_unix_ms(window.closes_at.value());
  }
  if (source.timing.reservation_start) {
    timing->set_reservation_start_unix_ms(
        source.timing.reservation_start->value());
  }
  timing->set_reservation_grace_seconds(
      source.timing.reservation_grace_seconds);
  timing->set_min_duration_seconds(source.timing.min_duration_seconds);
  timing->set_preferred_duration_seconds(
      source.timing.preferred_duration_seconds);
  timing->set_max_duration_seconds(source.timing.max_duration_seconds);
  timing->set_mandatory(source.timing.mandatory);
  timing->set_can_shorten(source.timing.can_shorten);
  timing->set_can_move(source.timing.can_move);
  timing->set_can_skip(source.timing.can_skip);
  if (source.timing.mandatory_deadline) {
    timing->set_mandatory_deadline_unix_ms(
        source.timing.mandatory_deadline->value());
  }
  destination.set_activity_delay_seconds(source.activity_delay_seconds);
  if (source.found_closed_at) {
    destination.set_found_closed_at_unix_ms(
        source.found_closed_at->value());
  }
}

void trip_to_proto(const domain::TripState& source,
                   const std::string& owner_user_id,
                   ::liveroute::v1::TripDefinition& destination) {
  destination.set_trip_id(format_uuid(source.trip_id.value()));
  destination.set_owner_user_id(owner_user_id);
  destination.set_default_time_zone_name(source.default_time_zone_name);
  for (const auto& activity : source.activities) {
    activity_to_proto(activity, *destination.add_activities());
  }
  destination.set_completed_prefix_count(
      static_cast<std::uint32_t>(source.completed_prefix_count));
  if (source.current_activity_id) {
    destination.set_current_activity_id(
        format_uuid(source.current_activity_id->value()));
  }
  destination.set_current_plan_id(
      format_uuid(source.current_plan.plan_id.value()));
  for (const auto& delay : source.travel_delays) {
    auto* output = destination.add_travel_delays();
    output->set_from_activity_id(
        format_uuid(delay.from_activity_id.value()));
    output->set_to_activity_id(format_uuid(delay.to_activity_id.value()));
    output->set_additional_seconds(delay.additional_seconds);
    output->set_observed_at_unix_ms(delay.observed_at.value());
  }
}

[[nodiscard]] ::liveroute::v1::SegmentDisposition disposition_to_proto(
    domain::SegmentDisposition value) noexcept {
  return static_cast<::liveroute::v1::SegmentDisposition>(
      static_cast<int>(value));
}

[[nodiscard]] ::liveroute::v1::PlanReasonCode reason_to_proto(
    domain::PlanReasonCode value) noexcept {
  return static_cast<::liveroute::v1::PlanReasonCode>(
      static_cast<int>(value));
}

void proposal_segment_to_proto(
    const domain::ProposalSegment& source,
    ::liveroute::v1::ProposalSegment& destination) {
  destination.set_activity_id(format_uuid(source.activity_id.value()));
  location_to_proto(source.location, *destination.mutable_location());
  destination.set_time_zone_name(source.time_zone_name);
  if (source.scheduled_start) {
    destination.set_scheduled_start_unix_ms(
        source.scheduled_start->value());
  }
  if (source.scheduled_end) {
    destination.set_scheduled_end_unix_ms(source.scheduled_end->value());
  }
  if (source.inbound_route) {
    auto* route = destination.mutable_inbound_route();
    route->set_duration_seconds(static_cast<std::uint32_t>(
        source.inbound_route->duration.count()));
    route->set_distance_meters(source.inbound_route->distance_meters);
    route->set_reachable(source.inbound_route->reachable);
  }
  destination.set_disposition(disposition_to_proto(source.disposition));
  for (const auto reason : source.reasons) {
    destination.add_reasons(reason_to_proto(reason));
  }
}

}  // namespace

std::optional<domain::TripId> parse_trip_id(const std::string& value) {
  return parse_id<domain::TripId>(value);
}

bool is_canonical_uuid(const std::string& value) noexcept {
  return parse_uuid(value).has_value();
}

std::string format_trip_id(const domain::TripId& value) {
  return format_uuid(value.value());
}

std::string format_plan_id(const domain::PlanId& value) {
  return format_uuid(value.value());
}

ConversionResult<runtime::RuntimeBootstrapRequest> bootstrap_from_proto(
    const ::liveroute::v1::PlannerStreamRequest& envelope,
    std::uint64_t stream_binding) {
  if (envelope.payload_case() !=
          ::liveroute::v1::PlannerStreamRequest::kBootstrapTrip ||
      envelope.runtime_epoch() == 0 || envelope.trip_id().empty()) {
    return failed<runtime::RuntimeBootstrapRequest>(
        "invalid bootstrap envelope");
  }
  const auto envelope_trip = parse_trip_id(envelope.trip_id());
  if (!envelope_trip) {
    return failed<runtime::RuntimeBootstrapRequest>("invalid trip id");
  }
  const auto& bootstrap = envelope.bootstrap_trip();
  std::optional<domain::TripState> state;
  std::string owner_user_id;
  std::uint64_t finalized = bootstrap.finalized_mutation_sequence();
  std::uint64_t revision = bootstrap.trip_revision();
  switch (bootstrap.base_case()) {
    case ::liveroute::v1::BootstrapTrip::kFullTrip: {
      if (!bootstrap.has_current_plan()) {
        return failed<runtime::RuntimeBootstrapRequest>(
            "full bootstrap requires current plan");
      }
      auto converted =
          trip_from_proto(bootstrap.full_trip(), bootstrap.current_plan());
      if (!converted || converted->trip_id != *envelope_trip) {
        return failed<runtime::RuntimeBootstrapRequest>(
            "invalid full bootstrap state");
      }
      state = std::move(*converted);
      owner_user_id = bootstrap.full_trip().owner_user_id();
      break;
    }
    case ::liveroute::v1::BootstrapTrip::kSnapshot: {
      if (bootstrap.has_current_plan()) {
        return failed<runtime::RuntimeBootstrapRequest>(
            "snapshot bootstrap forbids current plan");
      }
      runtime::TripRuntimeVersionSnapshot metadata;
      auto converted = state_from_snapshot(bootstrap.snapshot(), metadata);
      if (!converted || converted->trip_id != *envelope_trip ||
          metadata.trip_revision != revision ||
          metadata.finalized_mutation_sequence != finalized) {
        return failed<runtime::RuntimeBootstrapRequest>(
            "incompatible snapshot");
      }
      state = std::move(*converted);
      ::liveroute::v1::TripStateSnapshot payload;
      if (!payload.ParseFromString(bootstrap.snapshot().payload())) {
        return failed<runtime::RuntimeBootstrapRequest>(
            "invalid snapshot payload");
      }
      owner_user_id = payload.trip().owner_user_id();
      break;
    }
    default:
      return failed<runtime::RuntimeBootstrapRequest>(
          "bootstrap base is required");
  }
  if (bootstrap.has_current_observation() &&
      !apply_observation(bootstrap.current_observation(), *state)) {
    return failed<runtime::RuntimeBootstrapRequest>(
        "invalid current observation");
  }
  if (!bootstrap.has_current_observation() &&
      bootstrap.current_observation_sequence() != 0) {
    return failed<runtime::RuntimeBootstrapRequest>(
        "observation sequence requires observation");
  }
  return {.value =
              runtime::RuntimeBootstrapRequest{
                  .state = std::move(*state),
                  .owner_user_id = std::move(owner_user_id),
                  .runtime_epoch = envelope.runtime_epoch(),
                  .trip_revision = revision,
                  .finalized_mutation_sequence = finalized,
                  .current_observation_sequence =
                      bootstrap.current_observation_sequence(),
                  .stream_binding = stream_binding},
          .error = std::nullopt};
}

ConversionResult<runtime::RuntimeEventRequest> event_from_proto(
    const ::liveroute::v1::PlannerStreamRequest& envelope,
    std::chrono::system_clock::time_point system_now,
    std::chrono::steady_clock::time_point steady_now,
    std::chrono::milliseconds default_attempt_timeout,
    std::size_t max_candidates, std::size_t beam_width,
    std::size_t max_expansions) {
  if (envelope.payload_case() !=
          ::liveroute::v1::PlannerStreamRequest::kApplyEvent ||
      envelope.runtime_epoch() == 0 || envelope.trip_id().empty()) {
    return failed<runtime::RuntimeEventRequest>("invalid event envelope");
  }
  const auto trip_id = parse_trip_id(envelope.trip_id());
  const auto event_id =
      parse_id<domain::EventId>(envelope.apply_event().event_id());
  auto payload = event_payload_from_proto(envelope.apply_event());
  if (!trip_id || !event_id || !payload) {
    return failed<runtime::RuntimeEventRequest>("invalid event payload");
  }
  domain::TripEvent event{
      .event_id = *event_id,
      .occurred_at = domain::UnixTimeMilliseconds{
          envelope.apply_event().occurred_at_unix_ms()},
      .command_expires_at = std::nullopt,
      .payload = std::move(*payload)};
  if (envelope.apply_event().has_command_expires_at_unix_ms()) {
    event.command_expires_at = domain::UnixTimeMilliseconds{
        envelope.apply_event().command_expires_at_unix_ms()};
  }

  const auto expires = std::chrono::system_clock::time_point{
      std::chrono::milliseconds{envelope.expires_at_unix_ms()}};
  if (expires <= system_now) {
    return failed<runtime::RuntimeEventRequest>("request expired");
  }
  const auto remaining =
      std::chrono::duration_cast<std::chrono::steady_clock::duration>(
          expires - system_now);
  const auto attempt_deadline =
      std::min(steady_now + remaining,
               steady_now + default_attempt_timeout);
  const auto event_class = event.event_class();
  if (!event_class) {
    return failed<runtime::RuntimeEventRequest>("event type is required");
  }
  const bool durable =
      *event_class == domain::TripEventClass::kDurable ||
      *event_class ==
          domain::TripEventClass::kCanonicalFirstDurableMirror ||
      *event_class == domain::TripEventClass::kDurableCompareAndSwap;
  const bool plan_decision =
      std::holds_alternative<domain::PlanDecisionEvent>(event.payload);
  if ((durable &&
       (envelope.mutation_sequence() == 0 ||
        envelope.observation_sequence() != 0 ||
        !envelope.has_expected_trip_revision() ||
        (envelope.has_expected_planner_state_version() &&
         !plan_decision))) ||
      (!durable &&
       (envelope.observation_sequence() == 0 ||
        envelope.mutation_sequence() != 0 ||
        envelope.has_expected_trip_revision() ||
        envelope.has_expected_planner_state_version()))) {
    return failed<runtime::RuntimeEventRequest>(
        "event envelope fields do not match event class");
  }

  runtime::RuntimePlanningContext planning{
      .current_time = domain::UnixTimeMilliseconds{
          std::chrono::duration_cast<std::chrono::milliseconds>(
              system_now.time_since_epoch())
              .count()},
      .planning_horizon_start = domain::UnixTimeMilliseconds{
          std::chrono::duration_cast<std::chrono::milliseconds>(
              system_now.time_since_epoch())
              .count()},
      .planning_horizon_end = domain::UnixTimeMilliseconds{
          std::chrono::duration_cast<std::chrono::milliseconds>(
              expires.time_since_epoch())
              .count()},
      .proposal_id = domain::ProposalId{event_id->value()},
      .proposal_created_at = domain::UnixTimeMilliseconds{
          std::chrono::duration_cast<std::chrono::milliseconds>(
              system_now.time_since_epoch())
              .count()},
      .deadline = attempt_deadline,
      .max_candidates = max_candidates,
      .beam_width = beam_width,
      .max_expansions = max_expansions,
      .recovery_state = domain::RecoveryState::kCurrent};
  if (!planning.is_valid()) {
    return failed<runtime::RuntimeEventRequest>("invalid planning deadline");
  }
  return {
      .value =
          runtime::RuntimeEventRequest{
              .trip_id = *trip_id,
              .admission =
                  {.runtime_epoch = envelope.runtime_epoch(),
                   .mutation_sequence = envelope.mutation_sequence(),
                   .observation_sequence =
                       envelope.observation_sequence(),
                   .expected_trip_revision =
                       envelope.has_expected_trip_revision()
                           ? envelope.expected_trip_revision()
                           : 0,
                   .expected_planner_state_version =
                       envelope.has_expected_planner_state_version()
                           ? std::optional<std::uint64_t>{
                                 envelope.expected_planner_state_version()}
                           : std::nullopt,
                   .event = std::move(event)},
              .planning = std::move(planning)},
      .error = std::nullopt};
}

void proposal_to_proto(const domain::StoredPlanProposal& source,
                       ::liveroute::v1::ReplanResult& destination) {
  destination.set_status(::liveroute::v1::STATUS_CODE_OK);
  destination.set_retryable(false);
  auto* proposal = destination.mutable_proposal();
  proposal->set_proposal_id(
      format_uuid(source.proposal.proposal_id.value()));
  proposal->set_source_runtime_epoch(
      source.proposal.source_runtime_epoch);
  proposal->set_source_planner_state_version(
      source.proposal.source_planner_state_version.value());
  proposal->set_base_current_plan_id(
      format_uuid(source.proposal.base_current_plan_id.value()));
  proposal->set_source_trip_revision(
      source.proposal.source_trip_revision.value());
  proposal->set_source_accepted_mutation_sequence(
      source.proposal.source_accepted_mutation_sequence.value());
  for (const auto& segment : source.proposal.preserved_prefix) {
    proposal_segment_to_proto(segment, *proposal->add_preserved_prefix());
  }
  for (const auto& segment : source.proposal.revised_suffix) {
    proposal_segment_to_proto(segment, *proposal->add_revised_suffix());
  }
  proposal->set_created_at_unix_ms(source.proposal.created_at.value());
  destination.set_notification(
      static_cast<::liveroute::v1::NotificationType>(
          static_cast<int>(source.notification)));
  for (const auto reason : source.reasons) {
    destination.add_reasons(reason_to_proto(reason));
  }
  auto* stats = destination.mutable_stats();
  stats->set_candidates_evaluated(source.stats.candidates_evaluated);
  stats->set_candidates_pruned(source.stats.candidates_pruned);
  stats->set_search_depth(source.stats.search_depth);
  stats->set_queue_wait_microseconds(
      source.stats.queue_wait_microseconds);
  stats->set_provider_microseconds(source.stats.provider_microseconds);
  stats->set_planner_microseconds(source.stats.planner_microseconds);
  stats->set_serialization_microseconds(
      source.stats.serialization_microseconds);
  stats->set_deadline_hit(source.stats.deadline_hit);
  auto* quality = destination.mutable_quality();
  quality->set_plan_quality(
      static_cast<::liveroute::v1::PlanQuality>(
          static_cast<int>(source.quality.plan_quality)));
  quality->set_routing_quality(
      static_cast<::liveroute::v1::RoutingQuality>(
          static_cast<int>(source.quality.routing_quality)));
  quality->set_recovery_state(
      static_cast<::liveroute::v1::RecoveryState>(
          static_cast<int>(source.quality.recovery_state)));
}

ConversionResult<::liveroute::v1::SnapshotBlob> snapshot_to_proto(
    const domain::TripState& state,
    const runtime::TripRuntimeVersionSnapshot& versions,
    const std::string& owner_user_id) {
  if (versions.accepted_mutation_sequence !=
          versions.finalized_mutation_sequence ||
      !state.is_valid() || !parse_uuid(owner_user_id).has_value()) {
    return failed<::liveroute::v1::SnapshotBlob>("snapshot is not ready");
  }
  ::liveroute::v1::TripStateSnapshot payload;
  trip_to_proto(state, owner_user_id, *payload.mutable_trip());
  payload.set_trip_revision(versions.trip_revision);
  payload.set_accepted_mutation_sequence(
      versions.accepted_mutation_sequence);
  payload.set_finalized_mutation_sequence(
      versions.finalized_mutation_sequence);
  current_plan_to_proto(state.current_plan,
                        *payload.mutable_current_plan());
  payload.set_snapshot_schema_version(1);
  std::string bytes;
  if (!payload.SerializeToString(&bytes) ||
      bytes.size() > std::numeric_limits<std::uint32_t>::max()) {
    return failed<::liveroute::v1::SnapshotBlob>(
        "snapshot serialization failed");
  }
  const auto checksum = providers::sha256_hex(bytes);
  std::string checksum_bytes;
  checksum_bytes.reserve(32);
  for (std::size_t index = 0; index < checksum.size(); index += 2) {
    checksum_bytes.push_back(static_cast<char>(
        (hex_value(checksum[index]) << 4) |
        hex_value(checksum[index + 1])));
  }
  ::liveroute::v1::SnapshotBlob blob;
  blob.set_snapshot_schema_version(1);
  blob.set_source_runtime_epoch(versions.runtime_epoch);
  blob.set_source_planner_state_version(
      versions.planner_state_version);
  blob.set_trip_revision(versions.trip_revision);
  blob.set_covered_finalized_mutation_sequence(
      versions.finalized_mutation_sequence);
  blob.set_payload_size_bytes(static_cast<std::uint32_t>(bytes.size()));
  blob.set_checksum_sha256(std::move(checksum_bytes));
  blob.set_payload(std::move(bytes));
  return {.value = std::move(blob), .error = std::nullopt};
}

::liveroute::v1::StatusCode status_to_proto(
    runtime::RuntimePlanningStatus status) noexcept {
  switch (status) {
    case runtime::RuntimePlanningStatus::kOk:
    case runtime::RuntimePlanningStatus::kNoNewProposal:
      return ::liveroute::v1::STATUS_CODE_OK;
    case runtime::RuntimePlanningStatus::kStale:
      return ::liveroute::v1::STATUS_CODE_STALE;
    case runtime::RuntimePlanningStatus::kInvalidArgument:
      return ::liveroute::v1::STATUS_CODE_INVALID_ARGUMENT;
    case runtime::RuntimePlanningStatus::kResourceExhausted:
      return ::liveroute::v1::STATUS_CODE_RESOURCE_EXHAUSTED;
    case runtime::RuntimePlanningStatus::kCancelled:
      return ::liveroute::v1::STATUS_CODE_CANCELLED;
    case runtime::RuntimePlanningStatus::kDeadlineExceeded:
      return ::liveroute::v1::STATUS_CODE_DEADLINE_EXCEEDED;
    case runtime::RuntimePlanningStatus::kProviderUnavailable:
      return ::liveroute::v1::STATUS_CODE_PROVIDER_UNAVAILABLE;
    case runtime::RuntimePlanningStatus::kMatrixTooLarge:
      return ::liveroute::v1::STATUS_CODE_MATRIX_TOO_LARGE;
    case runtime::RuntimePlanningStatus::kInfeasible:
      return ::liveroute::v1::STATUS_CODE_INFEASIBLE;
    case runtime::RuntimePlanningStatus::kInternal:
      return ::liveroute::v1::STATUS_CODE_INTERNAL;
  }
  return ::liveroute::v1::STATUS_CODE_INTERNAL;
}

::liveroute::v1::StatusCode status_to_proto(
    runtime::RuntimeControlStatus status) noexcept {
  switch (status) {
    case runtime::RuntimeControlStatus::kOk:
      return ::liveroute::v1::STATUS_CODE_OK;
    case runtime::RuntimeControlStatus::kDuplicate:
      return ::liveroute::v1::STATUS_CODE_DUPLICATE;
    case runtime::RuntimeControlStatus::kStale:
      return ::liveroute::v1::STATUS_CODE_STALE;
    case runtime::RuntimeControlStatus::kInvalidArgument:
      return ::liveroute::v1::STATUS_CODE_INVALID_ARGUMENT;
    case runtime::RuntimeControlStatus::kInactive:
      return ::liveroute::v1::STATUS_CODE_INACTIVE_TRIP;
    case runtime::RuntimeControlStatus::kSnapshotNotReady:
      return ::liveroute::v1::STATUS_CODE_SNAPSHOT_NOT_READY;
  }
  return ::liveroute::v1::STATUS_CODE_INTERNAL;
}

::liveroute::v1::StaleReason stale_reason_to_proto(
    runtime::VersionStaleReason reason) noexcept {
  return static_cast<::liveroute::v1::StaleReason>(
      static_cast<int>(reason));
}

::liveroute::v1::StaleReason stale_reason_to_proto(
    runtime::EventCoordinatorStaleReason reason) noexcept {
  return static_cast<::liveroute::v1::StaleReason>(
      static_cast<int>(reason));
}

void set_response_versions(
    const runtime::TripRuntimeVersionSnapshot& source,
    ::liveroute::v1::PlannerStreamResponse& destination) {
  destination.set_runtime_epoch(source.runtime_epoch);
  destination.set_accepted_mutation_sequence(
      source.accepted_mutation_sequence);
  destination.set_accepted_observation_sequence(
      source.accepted_observation_sequence);
  destination.set_planner_state_version(source.planner_state_version);
  destination.set_trip_revision(source.trip_revision);
}

}  // namespace liveroute::transport
