import { fireEvent, render, screen } from "@testing-library/react";

import { createDefaultActivity } from "./activityDefaults";
import { ActivityEditor } from "./ActivityEditor";

describe("ActivityEditor", () => {
  it("edits contract fields while preserving a complete activity object", () => {
    const onChange = vi.fn();
    render(
      <ActivityEditor
        value={createDefaultActivity("11111111-1111-4111-8111-111111111111", 0)}
        onChange={onChange}
      />,
    );

    fireEvent.change(screen.getByLabelText("Travel mode"), {
      target: { value: "walking" },
    });
    fireEvent.change(screen.getByLabelText("Preferred seconds"), {
      target: { value: "1800" },
    });
    fireEvent.click(screen.getByLabelText("Has a reservation"));

    expect(onChange.mock.calls[0]?.[0]).toMatchObject({
      place_id: "11111111-1111-4111-8111-111111111111",
      inbound_travel_mode: "walking",
    });
    expect(onChange.mock.calls[1]?.[0]).toMatchObject({
      place_id: "11111111-1111-4111-8111-111111111111",
      timing: {
        preferred_duration_seconds: 1800,
      },
    });
    const edited = onChange.mock.calls[2]?.[0];
    expect(edited).toHaveProperty("timing.reservation_start_offset_ms", 0);
    expect(edited).toHaveProperty(
      "timing.open_windows[0].closes_offset_ms",
      86_400_000,
    );
    expect(edited).toHaveProperty("timing.can_shorten", false);
  });
});
