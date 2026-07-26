#include "liveroute/domain/current_plan.hpp"

#include <array>
#include <cstddef>
#include <optional>
#include <vector>

namespace {

using liveroute::domain::ActivityId;
using liveroute::domain::CurrentPlan;
using liveroute::domain::CurrentPlanSegment;
using liveroute::domain::PlanEntryState;
using liveroute::domain::PlanId;
using liveroute::domain::PlanOrigin;
using liveroute::domain::ProposalId;
using liveroute::domain::UnixTimeMilliseconds;

template <typename Id>
Id id_with_first_byte(std::byte first_byte) {
  std::array<std::byte, 16> bytes{};
  bytes.front() = first_byte;
  return Id{bytes};
}

CurrentPlan valid_plan() {
  return {
      .plan_id = id_with_first_byte<PlanId>(std::byte{1}),
      .plan_revision = 1,
      .origin = PlanOrigin::kUserAuthored,
      .segments = {
          {.activity_id = id_with_first_byte<ActivityId>(std::byte{2}),
           .state = PlanEntryState::kScheduled,
           .scheduled_start = UnixTimeMilliseconds{100},
           .scheduled_end = UnixTimeMilliseconds{200}},
          {.activity_id = id_with_first_byte<ActivityId>(std::byte{3}),
           .state = PlanEntryState::kOmitted,
           .scheduled_start = std::nullopt,
           .scheduled_end = std::nullopt},
          {.activity_id = id_with_first_byte<ActivityId>(std::byte{4}),
           .state = PlanEntryState::kScheduled,
           .scheduled_start = UnixTimeMilliseconds{200},
           .scheduled_end = UnixTimeMilliseconds{300}},
      },
      .created_at = UnixTimeMilliseconds{10},
      .source_proposal_id = std::nullopt,
  };
}

}  // namespace

int main() {
  const std::vector<ActivityId> activities{
      id_with_first_byte<ActivityId>(std::byte{2}),
      id_with_first_byte<ActivityId>(std::byte{3}),
      id_with_first_byte<ActivityId>(std::byte{4}),
  };
  const auto plan = valid_plan();
  if (!plan.is_valid_for(activities)) return 1;

  auto duplicate = plan;
  duplicate.segments[1].activity_id = duplicate.segments[0].activity_id;
  if (duplicate.is_valid_for(activities)) return 1;

  auto overlap = plan;
  overlap.segments[2].scheduled_start = UnixTimeMilliseconds{199};
  if (overlap.is_valid_for(activities)) return 1;

  auto omitted_time = plan;
  omitted_time.segments[1].scheduled_start = UnixTimeMilliseconds{300};
  if (omitted_time.is_valid_for(activities)) return 1;

  auto invalid_origin = plan;
  invalid_origin.source_proposal_id = id_with_first_byte<ProposalId>(std::byte{5});
  if (invalid_origin.is_valid_for(activities)) return 1;

  auto unknown_origin = plan;
  unknown_origin.origin = static_cast<PlanOrigin>(99);
  if (unknown_origin.is_valid_for(activities)) return 1;

  auto accepted = plan;
  accepted.origin = PlanOrigin::kAcceptedEngineProposal;
  accepted.source_proposal_id = id_with_first_byte<ProposalId>(std::byte{5});
  if (!accepted.is_valid_for(activities)) return 1;

  std::vector<ActivityId> too_many_ids;
  auto too_many = plan;
  too_many.segments.clear();
  for (std::size_t index = 0; index < 65; ++index) {
    const auto activity_id =
        id_with_first_byte<ActivityId>(static_cast<std::byte>(index + 1));
    too_many_ids.push_back(activity_id);
    too_many.segments.push_back(
        {.activity_id = activity_id,
         .state = PlanEntryState::kOmitted,
         .scheduled_start = std::nullopt,
         .scheduled_end = std::nullopt});
  }
  if (too_many.is_valid_for(too_many_ids)) return 1;

  return 0;
}
