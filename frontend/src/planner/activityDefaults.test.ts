import { createDefaultActivity } from "./activityDefaults";

describe("new activity defaults", () => {
  it("constructs the complete deterministic activity input", () => {
    expect(
      createDefaultActivity("11111111-1111-4111-8111-111111111111", 2),
    ).toEqual({
      place_id: "11111111-1111-4111-8111-111111111111",
      ordinal: 2,
      schedule: { state: "unscheduled" },
      inbound_travel_mode: "driving",
      activity_class: "flexible",
      priority_rank: 0,
      utility_score: 0,
      timing: {
        open_windows: [{ opens_offset_ms: 0, closes_offset_ms: 86_400_000 }],
        reservation_grace_seconds: 0,
        min_duration_seconds: 3_600,
        preferred_duration_seconds: 3_600,
        max_duration_seconds: 3_600,
        mandatory: false,
        can_shorten: false,
        can_move: true,
        can_skip: true,
      },
    });
  });

  it("does not share mutable availability windows", () => {
    const first = createDefaultActivity("first", 0);
    const second = createDefaultActivity("second", 1);

    first.timing.open_windows[0]!.opens_offset_ms = 1;

    expect(second.timing.open_windows[0]!.opens_offset_ms).toBe(0);
  });
});
