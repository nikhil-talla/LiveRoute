#include "liveroute/benchmark/planner_allocation.hpp"

#include <algorithm>
#include <array>
#include <chrono>
#include <cstddef>
#include <cstdint>
#include <cstdlib>
#include <filesystem>
#include <fstream>
#include <iomanip>
#include <iostream>
#include <optional>
#include <stdexcept>
#include <sstream>
#include <string>
#include <string_view>
#include <thread>
#include <vector>
#include <ctime>
#include <unistd.h>

#include "liveroute/domain/trip_state.hpp"
#include "liveroute/planner/planning_input_assembler.hpp"
#include "liveroute/planner/replan_attempt.hpp"
#include "liveroute/providers/sha256.hpp"

namespace {

using namespace liveroute::domain;
using namespace liveroute::planner;
using liveroute::benchmark::PlannerAllocationScope;
using liveroute::benchmark::PlannerAllocationSnapshot;

constexpr std::array<std::size_t, 5> kSuffixSizes = {4, 8, 16, 32, 64};
constexpr std::array<std::uint64_t, 19> kHistogramBounds = {
    1,       5,       10,      25,      50,      100,     250,
    500,     1000,    2500,    5000,    10000,   25000,   50000,
    100000,  250000,  500000, 1000000, 0};
constexpr std::size_t kWarmups = 10;
constexpr std::size_t kMeasured = 200;

struct BenchmarkCase {
  TripState state;
  TravelTimeMatrix matrix;
  TripEventPayload trigger;
};

struct AttemptMeasurement {
  std::uint64_t elapsed_microseconds{};
  PlannerAllocationSnapshot allocations;
  std::uint64_t expansions{};
  std::uint64_t candidates{};
  std::string digest;
};

struct Options {
  std::filesystem::path output_dir{"."};
  std::string benchmark{"allocation"};
  std::string variant{"baseline"};
  std::string image_digest;
  std::optional<std::size_t> suffix_size;
  std::uint8_t tail_optimization_mask{};
};

std::uint8_t tail_mask_for_variant(std::string_view variant) {
  if (variant == "tail-baseline") return 0;
  if (variant == "validated-input") return kTailValidatedInput;
  if (variant == "lower-bound-scratch") return kTailLowerBoundScratch;
  if (variant == "partial-beam-selection") return kTailPartialBeamSelection;
  if (variant == "combined-candidate") return kTailAllOptimizations;
  throw std::invalid_argument("unknown planner-tail variant");
}

template <typename Id>
Id id(std::uint8_t marker) {
  std::array<std::byte, 16> bytes{};
  bytes.front() = static_cast<std::byte>(marker);
  return Id{bytes};
}

std::string json_escape(std::string_view value) {
  std::string result;
  result.reserve(value.size() + 2);
  constexpr char kHex[] = "0123456789abcdef";
  for (const char character : value) {
    const auto code = static_cast<unsigned char>(character);
    if (code < 0x20U) {
      result += "\\u00";
      result.push_back(kHex[code >> 4U]);
      result.push_back(kHex[code & 0x0fU]);
      continue;
    }
    switch (character) {
      case '"': result += "\\\""; break;
      case '\\': result += "\\\\"; break;
      case '\n': result += "\\n"; break;
      case '\r': result += "\\r"; break;
      case '\t': result += "\\t"; break;
      default: result.push_back(character); break;
    }
  }
  return result;
}

std::string quoted(std::string_view value) {
  return "\"" + json_escape(value) + "\"";
}

std::string uuid_from(std::string_view seed) {
  std::string uuid = liveroute::providers::sha256_hex(seed).substr(0, 32);
  uuid.insert(8, "-");
  uuid.insert(13, "-");
  uuid.insert(18, "-");
  uuid.insert(23, "-");
  uuid[14] = '4';
  uuid[19] = 'a';
  return uuid;
}

std::string utc_now() {
  const auto now = std::chrono::system_clock::to_time_t(
      std::chrono::system_clock::now());
  std::tm calendar{};
  gmtime_r(&now, &calendar);
  std::ostringstream output;
  output << std::put_time(&calendar, "%Y-%m-%dT%H:%M:%SZ");
  return output.str();
}

std::string read_first_line(const char* path, std::string fallback) {
  std::ifstream input(path);
  std::string line;
  if (input && std::getline(input, line) && !line.empty()) return line;
  return fallback;
}

std::string target_arch() {
#if defined(__x86_64__)
  return "x86_64";
#elif defined(__aarch64__)
  return "aarch64";
#else
  return "unknown";
#endif
}

Options parse_options(int argc, char** argv) {
  Options options;
  const char* environment_digest =
      std::getenv("LIVEROUTE_BENCHMARK_CONTAINER_IMAGE_DIGEST");
  if (environment_digest != nullptr) options.image_digest = environment_digest;
  for (int index = 1; index < argc; ++index) {
    const std::string argument = argv[index];
    const auto separator = argument.find('=');
    if (separator == std::string::npos) {
      throw std::invalid_argument("benchmark arguments require --name=value");
    }
    const auto name = argument.substr(0, separator);
    const auto value = argument.substr(separator + 1);
    if (name == "--output-dir") {
      options.output_dir = value;
    } else if (name == "--benchmark") {
      options.benchmark = value;
    } else if (name == "--variant") {
      options.variant = value;
    } else if (name == "--suffix-size") {
      options.suffix_size = static_cast<std::size_t>(std::stoul(value));
    } else if (name == "--tail-mask") {
      const auto parsed = std::stoul(value);
      if (parsed > kTailAllOptimizations) {
        throw std::invalid_argument("tail mask must be between 0 and 7");
      }
      options.tail_optimization_mask = static_cast<std::uint8_t>(parsed);
    } else if (name == "--container-image-digest") {
      options.image_digest = value;
    } else {
      throw std::invalid_argument("unknown benchmark argument: " + name);
    }
  }
  if (options.benchmark != "allocation" &&
      options.benchmark != "layout-timing" &&
      options.benchmark != "tail") {
    throw std::invalid_argument(
        "benchmark must be allocation, layout-timing, or tail");
  }
  if (options.benchmark == "allocation" && options.variant != "baseline" &&
      options.variant != "candidate") {
    throw std::invalid_argument("allocation variant must be baseline or candidate");
  }
  if (options.benchmark == "layout-timing" &&
      options.variant != "aos-baseline" && options.variant != "soa-candidate") {
    throw std::invalid_argument(
        "layout-timing variant must be aos-baseline or soa-candidate");
  }
  if (options.benchmark == "tail") {
    const auto expected = tail_mask_for_variant(options.variant);
    if (options.variant == "combined-candidate") {
      if (options.tail_optimization_mask == 0) {
        options.tail_optimization_mask = expected;
      }
    } else if (options.tail_optimization_mask != 0 &&
               options.tail_optimization_mask != expected) {
      throw std::invalid_argument("tail mask does not match variant");
    } else {
      options.tail_optimization_mask = expected;
    }
  }
  if (options.suffix_size.has_value() &&
      std::find(kSuffixSizes.begin(), kSuffixSizes.end(),
                *options.suffix_size) == kSuffixSizes.end()) {
    throw std::invalid_argument("suffix size is not in the Phase 19 suite");
  }
  if (options.image_digest.size() != 71 ||
      options.image_digest.rfind("sha256:", 0) != 0) {
    throw std::invalid_argument(
        "--container-image-digest or LIVEROUTE_BENCHMARK_CONTAINER_IMAGE_DIGEST "
        "must contain a sha256 image digest");
  }
  return options;
}

BenchmarkCase make_case(std::size_t suffix_size) {
  std::vector<Activity> activities;
  activities.reserve(suffix_size);
  std::vector<CurrentPlanSegment> segments;
  segments.reserve(suffix_size);
  for (std::size_t index = 0; index < suffix_size; ++index) {
    const auto activity_id = id<ActivityId>(static_cast<std::uint8_t>(index + 1));
    activities.push_back({
        .activity_id = activity_id,
        .place_id = PlaceId{"planner-benchmark-" + std::to_string(index)},
        .display_name = "Planner benchmark activity",
        .location = Location{40.0 + static_cast<double>(index) * 0.001,
                             -74.0 - static_cast<double>(index) * 0.001},
        .time_zone_name = "UTC",
        .inbound_travel_mode = TravelMode::kDriving,
        .activity_class = ActivityClass::kFlexible,
        .activity_state = ActivityState::kPlanned,
        .priority_rank = static_cast<std::int32_t>(index),
        .utility_score = 100 - static_cast<std::int32_t>(index),
        .timing = ActivityTiming{
            .open_windows = {{UnixTimeMilliseconds{0},
                              UnixTimeMilliseconds{1000000000}}},
            .reservation_start = std::nullopt,
            .reservation_grace_seconds = 0,
            .min_duration_seconds = 5,
            .preferred_duration_seconds = 5,
            .max_duration_seconds = 5,
            .mandatory = true,
            .can_shorten = false,
            .can_move = false,
            .can_skip = false,
            .mandatory_deadline = std::nullopt},
        .activity_delay_seconds = 0,
        .found_closed_at = std::nullopt,
    });
    const auto start = UnixTimeMilliseconds{10000 +
                                            static_cast<std::int64_t>(index) *
                                                10000};
    segments.push_back({.activity_id = activity_id,
                        .state = PlanEntryState::kScheduled,
                        .scheduled_start = start,
                        .scheduled_end = UnixTimeMilliseconds{start.value() +
                                                             5000}});
  }

  std::vector<ActivityId> activity_ids;
  activity_ids.reserve(activities.size());
  for (const auto& activity : activities) activity_ids.push_back(activity.activity_id);
  const auto plan_id = id<PlanId>(200);
  TripState state{
      .trip_id = id<TripId>(201),
      .default_time_zone_name = "UTC",
      .activities = std::move(activities),
      .completed_prefix_count = 0,
      .current_activity_id = std::nullopt,
      .current_plan = CurrentPlan{.plan_id = plan_id,
                                  .plan_revision = 1,
                                  .origin = PlanOrigin::kUserAuthored,
                                  .segments = std::move(segments),
                                  .created_at = UnixTimeMilliseconds{0},
                                  .source_proposal_id = std::nullopt},
      .travel_delays = {},
      .current_observation = {},
      .active_proposal = std::nullopt,
  };

  const auto location_count = suffix_size + 1;
  std::vector<RouteEstimate> estimates(
      location_count * location_count,
      RouteEstimate{.duration = std::chrono::seconds{1},
                    .distance_meters = 100,
                    .reachable = true});
  for (std::size_t index = 0; index < location_count; ++index) {
    estimates[index * location_count + index] =
        RouteEstimate{.duration = std::chrono::seconds{0},
                      .distance_meters = 0,
                      .reachable = true};
  }
  return {.state = std::move(state),
          .matrix = TravelTimeMatrix{location_count, std::move(estimates)},
          .trigger = ActivityDelayed{id<ActivityId>(1), 1}};
}

std::string canonical_digest(const ReplanAttemptResult& attempt,
                             const std::optional<StoredPlanProposal>& stored) {
  std::ostringstream canonical;
  canonical << static_cast<unsigned>(attempt.search.outcome) << '|'
            << attempt.search.candidate_count << '|'
            << attempt.search.expansion_count << '|'
            << (attempt.search.deadline_hit ? 1 : 0) << '|'
            << (attempt.proposal.has_value() ? 1 : 0) << '|'
            << (stored.has_value() ? 1 : 0) << '|';
  if (stored.has_value()) {
    canonical << static_cast<unsigned>(stored->notification) << '|'
              << static_cast<unsigned>(stored->quality.plan_quality) << '|'
              << stored->proposal.preserved_prefix.size() << '|'
              << stored->proposal.revised_suffix.size();
    for (const auto& segment : stored->proposal.revised_suffix) {
      canonical << '|'
                << static_cast<unsigned>(segment.disposition) << '|'
                << (segment.scheduled_start.has_value()
                        ? segment.scheduled_start->value()
                        : -1)
                << '|'
                << (segment.scheduled_end.has_value()
                        ? segment.scheduled_end->value()
                        : -1);
    }
  }
  return liveroute::providers::sha256_hex(canonical.str());
}

AttemptMeasurement run_attempt(const BenchmarkCase& benchmark_case,
                               PlannerScratch& scratch) {
  const auto started = std::chrono::steady_clock::now();
  PlannerAllocationScope scope;
  const auto input = assemble_beam_search_input(
      benchmark_case.state, UnixTimeMilliseconds{0}, UnixTimeMilliseconds{0},
      UnixTimeMilliseconds{1000000000}, benchmark_case.matrix);
  if (!input) throw std::runtime_error("benchmark fixture produced invalid input");
  const ProposalSource source{
      .proposal_id = id<ProposalId>(202),
      .runtime_epoch = 1,
      .planner_state_version = PlannerStateVersion{1},
      .base_current_plan_id = benchmark_case.state.current_plan.plan_id,
      .trip_revision = TripRevision{1},
      .accepted_mutation_sequence = MutationSequence{1},
      .created_at = UnixTimeMilliseconds{0},
  };
  const ReplanBudget budget{
      .deadline = std::chrono::steady_clock::now() + std::chrono::seconds{60},
      .max_candidates = 4096,
      .beam_width = 32,
      .max_expansions = 16384,
      .stop_token = {},
  };
  const auto facts = derive_replan_facts(*input);
  if (!facts) throw std::runtime_error("benchmark fixture produced invalid facts");
  const auto attempt = run_replan_attempt(
      *input, benchmark_case.state.activities, source, benchmark_case.trigger,
      *facts, budget, scratch);
  const PlannerStats stats{
      .candidates_evaluated = attempt.search.candidate_count,
      .candidates_pruned = 0,
      .search_depth = static_cast<std::uint32_t>(input->remaining_activities.size()),
      .queue_wait_microseconds = 0,
      .provider_microseconds = 0,
      .planner_microseconds = 0,
      .serialization_microseconds = 0,
      .deadline_hit = attempt.search.deadline_hit,
  };
  const auto stored = assemble_stored_plan_proposal(
      attempt, benchmark_case.state.activities, stats, RoutingQuality::kFresh,
      RecoveryState::kCurrent);
  const auto finished = std::chrono::steady_clock::now();
  const auto elapsed = std::chrono::duration_cast<std::chrono::microseconds>(
                           finished - started)
                           .count();
  const auto snapshot = scope.snapshot();
  if (!snapshot.valid || snapshot.scope_overflows != 0 ||
      attempt.search.deadline_hit || !stored.has_value()) {
    std::ostringstream message;
    message << "planner benchmark attempt did not complete cleanly"
            << " outcome=" << static_cast<unsigned>(attempt.search.outcome)
            << " candidates=" << attempt.search.candidate_count
            << " expansions=" << attempt.search.expansion_count
            << " deadline=" << (attempt.search.deadline_hit ? 1 : 0)
            << " stored=" << (stored.has_value() ? 1 : 0)
            << " scope_valid=" << (snapshot.valid ? 1 : 0)
            << " overflows=" << snapshot.scope_overflows;
    throw std::runtime_error(message.str());
  }
  return {.elapsed_microseconds = static_cast<std::uint64_t>(
              std::max<std::int64_t>(1, elapsed)),
          .allocations = snapshot,
          .expansions = attempt.search.expansion_count,
          .candidates = attempt.search.candidate_count,
          .digest = canonical_digest(attempt, stored)};
}

std::string histogram_json(const std::vector<AttemptMeasurement>& attempts) {
  std::array<std::uint64_t, kHistogramBounds.size()> buckets{};
  std::uint64_t sum = 0;
  std::uint64_t maximum = 0;
  for (const auto& attempt : attempts) {
    const auto value = attempt.elapsed_microseconds;
    sum += value;
    maximum = std::max(maximum, value);
    std::size_t bucket = kHistogramBounds.size() - 1;
    for (std::size_t index = 0; index + 1 < kHistogramBounds.size(); ++index) {
      if (value <= kHistogramBounds[index]) {
        bucket = index;
        break;
      }
    }
    ++buckets[bucket];
  }
  std::ostringstream output;
  output << "{\"unit\":\"microseconds\",\"count\":" << attempts.size()
         << ",\"sum_microseconds\":" << sum
         << ",\"max_microseconds\":" << maximum
         << ",\"upper_bounds_microseconds\":[1,5,10,25,50,100,250,500,1000,2500,5000,10000,25000,50000,100000,250000,500000,1000000,null]"
         << ",\"bucket_counts\":[";
  for (std::size_t index = 0; index < buckets.size(); ++index) {
    if (index != 0) output << ',';
    output << buckets[index];
  }
  output << "]}";
  return output.str();
}

std::string artifact_json(const Options& options, std::string_view run_id,
                          std::string_view started_at, std::size_t suffix_size,
                          const std::vector<AttemptMeasurement>& attempts,
                          std::uint64_t elapsed_microseconds) {
  std::uint64_t allocation_calls = 0;
  std::uint64_t allocated_bytes = 0;
  for (const auto& attempt : attempts) {
    allocation_calls += attempt.allocations.calls;
    allocated_bytes += attempt.allocations.bytes;
  }
  std::ostringstream output;
  output << "{\"schema_version\":\"liveroute.benchmark.v1\","
         << "\"run_id\":" << quoted(run_id) << ','
         << "\"started_at\":" << quoted(started_at) << ','
         << "\"benchmark_name\":\"planner-allocation-v1\","
         << "\"dimensions\":{";
  output << "\"mode\":\"planner\",\"workload_profile\":null,\"seed\":1,"
         << "\"parameters\":{";
  output << "\"attempt_deadline_ms\":60000,\"beam_width\":32,"
         << "\"max_candidates\":4096,\"max_expansions\":16384,"
         << "\"suffix_size\":" << suffix_size << ",\"variant\":"
         << quoted(options.variant) << "},";
  output << "\"build\":{\"git_commit\":null,\"worktree_dirty\":true,"
         << "\"build_type\":\"RelWithDebInfo\",\"compiler_id\":\"GNU\","
         << "\"compiler_version\":" << quoted(__VERSION__)
         << ",\"target_arch\":" << quoted(target_arch())
         << ",\"container_image_digest\":"
         << quoted(options.image_digest) << "},";
  output << "\"environment\":{\"os_name\":\"Linux\",\"kernel_version\":"
         << quoted(read_first_line("/proc/sys/kernel/osrelease", "unknown"))
         << ",\"cpu_model\":\"unknown\""
         << ",\"logical_cpu_count\":"
         << std::max(1U, std::thread::hardware_concurrency())
         << ",\"cpu_quota_millicores\":null,\"memory_limit_bytes\":null},"
         << "\"protocol_version\":\"liveroute.v1\","
         << "\"planner_policy_version\":\"liveroute-v1-lexicographic-1\","
         << "\"osrm_dataset_version\":\"none\","
         << "\"route_cache_policy_version\":null,\"route_cache_enabled\":false},";
  output << "\"measurement\":{\"warmup_operations\":10,"
         << "\"measured_operations\":200,\"elapsed_microseconds\":"
         << std::max<std::uint64_t>(1, elapsed_microseconds)
         << ",\"completed_operations\":200},\"counters\":{";
  output << "\"deadline_misses\":0,\"planner_allocation_calls\":"
         << allocation_calls << ",\"planner_allocated_bytes\":"
         << allocated_bytes << ",\"planner_allocation_scope_overflows\":0},"
         << "\"histograms\":{";
  output << "\"planner\":" << histogram_json(attempts)
         << "},\"gauges\":{}}";
  return output.str();
}

std::string layout_timing_artifact_json(
    const Options& options, std::string_view run_id, std::string_view started_at,
    std::size_t suffix_size, std::string_view result_digest,
    const std::vector<AttemptMeasurement>& attempts,
    std::uint64_t elapsed_microseconds) {
  std::uint64_t allocation_calls = 0;
  std::uint64_t allocated_bytes = 0;
  std::uint64_t expansions = 0;
  std::uint64_t candidates = 0;
  for (const auto& attempt : attempts) {
    allocation_calls += attempt.allocations.calls;
    allocated_bytes += attempt.allocations.bytes;
    expansions += attempt.expansions;
    candidates += attempt.candidates;
  }
  std::ostringstream output;
  output << "{\"schema_version\":\"liveroute.benchmark.v1\",";
  output << "\"run_id\":" << quoted(run_id) << ",\"started_at\":"
         << quoted(started_at)
         << ",\"benchmark_name\":\"planner-layout-timing-v1\",";
  output << "\"dimensions\":{\"mode\":\"planner\","
         << "\"workload_profile\":null,\"seed\":1,\"parameters\":{";
  output << "\"attempt_deadline_ms\":60000,\"beam_width\":32,"
         << "\"max_candidates\":4096,\"max_expansions\":16384,"
         << "\"layout_version\":"
         << quoted(options.variant == "aos-baseline" ? "aos-v1" : "soa-v1")
         << ",\"result_digest\":" << quoted(result_digest)
         << ",\"suffix_size\":" << suffix_size
         << ",\"variant\":" << quoted(options.variant) << "},";
  output << "\"build\":{\"git_commit\":null,\"worktree_dirty\":true,"
         << "\"build_type\":\"RelWithDebInfo\",\"compiler_id\":\"GNU\","
         << "\"compiler_version\":" << quoted(__VERSION__)
         << ",\"target_arch\":" << quoted(target_arch())
         << ",\"container_image_digest\":" << quoted(options.image_digest)
         << "},\"environment\":{\"os_name\":\"Linux\","
         << "\"kernel_version\":"
         << quoted(read_first_line("/proc/sys/kernel/osrelease", "unknown"))
         << ",\"cpu_model\":\"unknown\",\"logical_cpu_count\":"
         << std::max(1U, std::thread::hardware_concurrency())
         << ",\"cpu_quota_millicores\":null,\"memory_limit_bytes\":null},"
         << "\"protocol_version\":\"liveroute.v1\","
         << "\"planner_policy_version\":\"liveroute-v1-lexicographic-1\","
         << "\"osrm_dataset_version\":\"none\","
         << "\"route_cache_policy_version\":null,\"route_cache_enabled\":false},";
  output << "\"measurement\":{\"warmup_operations\":10,"
         << "\"measured_operations\":200,\"elapsed_microseconds\":"
         << std::max<std::uint64_t>(1, elapsed_microseconds)
         << ",\"completed_operations\":200},\"counters\":{";
  output << "\"deadline_misses\":0,\"planner_allocation_calls\":"
         << allocation_calls << ",\"planner_allocated_bytes\":"
         << allocated_bytes << ",\"planner_allocation_scope_overflows\":0,"
         << "\"planner_candidates\":" << candidates
         << ",\"planner_expansions\":" << expansions << "},"
         << "\"histograms\":{\"planner\":" << histogram_json(attempts)
         << "},\"gauges\":{}}";
  return output.str();
}

std::string tail_artifact_json(
    const Options& options, std::string_view run_id, std::string_view started_at,
    std::size_t suffix_size, std::string_view result_digest,
    const std::vector<AttemptMeasurement>& attempts,
    std::uint64_t elapsed_microseconds) {
  std::uint64_t allocation_calls = 0;
  std::uint64_t allocated_bytes = 0;
  std::uint64_t expansions = 0;
  std::uint64_t candidates = 0;
  for (const auto& attempt : attempts) {
    allocation_calls += attempt.allocations.calls;
    allocated_bytes += attempt.allocations.bytes;
    expansions += attempt.expansions;
    candidates += attempt.candidates;
  }
  std::ostringstream output;
  output << "{\"schema_version\":\"liveroute.benchmark.v1\",";
  output << "\"run_id\":" << quoted(run_id) << ",\"started_at\":"
         << quoted(started_at) << ",\"benchmark_name\":\"planner-tail-v1\",";
  output << "\"dimensions\":{\"mode\":\"planner\",";
  output << "\"workload_profile\":null,\"seed\":1,\"parameters\":{";
  output << "\"attempt_deadline_ms\":60000,\"beam_width\":32,"
         << "\"layout_version\":\"aos-v1\","
         << "\"max_candidates\":4096,\"max_expansions\":16384,"
         << "\"result_digest\":" << quoted(result_digest)
         << ",\"suffix_size\":" << suffix_size
         << ",\"tail_optimization_mask\":"
         << static_cast<unsigned int>(options.tail_optimization_mask)
         << ",\"variant\":" << quoted(options.variant) << "},";
  output << "\"build\":{\"git_commit\":null,\"worktree_dirty\":true,"
         << "\"build_type\":\"RelWithDebInfo\",\"compiler_id\":\"GNU\","
         << "\"compiler_version\":" << quoted(__VERSION__)
         << ",\"target_arch\":" << quoted(target_arch())
         << ",\"container_image_digest\":" << quoted(options.image_digest)
         << "},\"environment\":{\"os_name\":\"Linux\",\"kernel_version\":"
         << quoted(read_first_line("/proc/sys/kernel/osrelease", "unknown"))
         << ",\"cpu_model\":\"unknown\",\"logical_cpu_count\":"
         << std::max(1U, std::thread::hardware_concurrency())
         << ",\"cpu_quota_millicores\":null,\"memory_limit_bytes\":null},"
         << "\"protocol_version\":\"liveroute.v1\","
         << "\"planner_policy_version\":\"liveroute-v1-lexicographic-1\","
         << "\"osrm_dataset_version\":\"none\","
         << "\"route_cache_policy_version\":null,\"route_cache_enabled\":false},";
  output << "\"measurement\":{\"warmup_operations\":10,"
         << "\"measured_operations\":200,\"elapsed_microseconds\":"
         << std::max<std::uint64_t>(1, elapsed_microseconds)
         << ",\"completed_operations\":200},\"counters\":{";
  output << "\"deadline_misses\":0,\"planner_allocation_calls\":"
         << allocation_calls << ",\"planner_allocated_bytes\":"
         << allocated_bytes << ",\"planner_allocation_scope_overflows\":0,"
         << "\"planner_candidates\":" << candidates
         << ",\"planner_expansions\":" << expansions << "},"
         << "\"histograms\":{\"planner\":" << histogram_json(attempts)
         << "},\"gauges\":{}}";
  return output.str();
}

}  // namespace

int main(int argc, char** argv) {
  try {
    const auto options = parse_options(argc, argv);
    std::filesystem::create_directories(options.output_dir);
    const auto process_seed =
        std::to_string(std::chrono::system_clock::now().time_since_epoch().count()) +
        ":" + std::to_string(static_cast<unsigned long>(::getpid()));
    const auto started_at = utc_now();
    for (const auto suffix_size : kSuffixSizes) {
      if (options.suffix_size.has_value() &&
          *options.suffix_size != suffix_size) {
        continue;
      }
      auto benchmark_case = make_case(suffix_size);
      PlannerScratch scratch;
      scratch.use_soa = options.benchmark != "allocation"
                            ? options.benchmark == "layout-timing" &&
                                  options.variant == "soa-candidate"
                            : options.variant != "baseline";
      scratch.tail_optimization_mask =
          options.benchmark == "tail" ? options.tail_optimization_mask : 0;
      for (std::size_t index = 0; index < kWarmups; ++index) {
        static_cast<void>(run_attempt(benchmark_case, scratch));
      }

      std::vector<AttemptMeasurement> attempts;
      attempts.reserve(kMeasured);
      const auto measured_started = std::chrono::steady_clock::now();
      std::optional<std::string> expected_digest;
      for (std::size_t index = 0; index < kMeasured; ++index) {
        auto attempt = run_attempt(benchmark_case, scratch);
        if (!expected_digest) expected_digest = attempt.digest;
        if (*expected_digest != attempt.digest) {
          throw std::runtime_error("planner result digest changed within a run");
        }
        attempts.push_back(std::move(attempt));
      }
      const auto measured_finished = std::chrono::steady_clock::now();
      const auto elapsed = std::chrono::duration_cast<std::chrono::microseconds>(
                               measured_finished - measured_started)
                               .count();
      const auto run_id = uuid_from(process_seed + ":" +
                                    std::to_string(suffix_size));
      const auto artifact = options.benchmark == "layout-timing"
                                ? layout_timing_artifact_json(
                                      options, run_id, started_at, suffix_size,
                                      *expected_digest, attempts,
                                      static_cast<std::uint64_t>(
                                          std::max<std::int64_t>(1, elapsed)))
                                : options.benchmark == "tail"
                                      ? tail_artifact_json(
                                            options, run_id, started_at,
                                            suffix_size, *expected_digest,
                                            attempts,
                                            static_cast<std::uint64_t>(
                                                std::max<std::int64_t>(1,
                                                                       elapsed)))
                                      : artifact_json(
                                      options, run_id, started_at, suffix_size,
                                      attempts,
                                      static_cast<std::uint64_t>(
                                          std::max<std::int64_t>(1, elapsed)));
      const auto path = options.output_dir /
                        ((options.benchmark == "layout-timing"
                              ? "planner-layout-timing-v1-"
                              : options.benchmark == "tail"
                                    ? "planner-tail-v1-"
                              : "planner-allocation-v1-") +
                         options.variant + "-" + run_id + "-" +
                         std::to_string(suffix_size) + ".json");
      std::ofstream output(path);
      if (!output) throw std::runtime_error("cannot create benchmark artifact");
      output << artifact << '\n';
      std::cout << "suffix_size=" << suffix_size
                << " run_id=" << run_id
                << " result_digest=" << *expected_digest << '\n';
    }
    return 0;
  } catch (const std::exception& error) {
    std::cerr << "planner allocation benchmark failed: " << error.what() << '\n';
    return 1;
  }
}
