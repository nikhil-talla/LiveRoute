import defaults from "./activity-defaults.json";

import type { ActivityInput } from "../api/types";

export function createDefaultActivity(
  placeId: string,
  ordinal: number,
): ActivityInput {
  return {
    place_id: placeId,
    ordinal,
    schedule: { state: defaults.schedule.state as "unscheduled" },
    inbound_travel_mode:
      defaults.inbound_travel_mode as ActivityInput["inbound_travel_mode"],
    activity_class: defaults.activity_class as ActivityInput["activity_class"],
    priority_rank: defaults.priority_rank,
    utility_score: defaults.utility_score,
    timing: {
      ...defaults.timing,
      open_windows: defaults.timing.open_windows.map((window) => ({
        ...window,
      })),
      can_shorten: false,
    },
  };
}
