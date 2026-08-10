import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

import type { LiveRouteApi } from "../api/client";
import type { Place, Trip } from "../api/types";
import { NewTripPage } from "./App";

vi.mock("../map/PlaceSearch", () => ({
  PlaceSearch: ({
    onPlaceConfirmed,
  }: {
    onPlaceConfirmed: (place: Place) => void;
  }) => (
    <button
      type="button"
      onClick={() =>
        onPlaceConfirmed({
          place_id: "11111111-1111-4111-8111-111111111111",
          latitude: 41.824,
          longitude: -71.412,
          display_name: "Providence Station",
          time_zone_name: "America/New_York",
          created_at_unix_ms: 1,
        })
      }
    >
      Confirm test place
    </button>
  ),
}));

describe("NewTripPage", () => {
  it("submits the selected timezone and complete edited activity", async () => {
    const createTrip = vi.fn(
      async (): Promise<Trip> =>
        ({ trip_id: "22222222-2222-4222-8222-222222222222" }) as Trip,
    );
    const api = {
      createTrip,
      getSession: vi.fn(),
      listTrips: vi.fn(),
      getTrip: vi.fn(),
      resolvePlace: vi.fn(),
      createPlace: vi.fn(),
    } as unknown as LiveRouteApi;

    render(
      <MemoryRouter>
        <NewTripPage
          api={api}
          csrfToken="csrf-token"
          defaultTimeZoneName="America/New_York"
        />
      </MemoryRouter>,
    );

    fireEvent.change(screen.getByLabelText("Trip name"), {
      target: { value: "Saturday in Providence" },
    });
    fireEvent.change(screen.getByLabelText("Trip timezone"), {
      target: { value: "America/Chicago" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Confirm test place" }));
    fireEvent.change(screen.getByLabelText("Travel mode"), {
      target: { value: "walking" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save trip" }));

    await waitFor(() => expect(createTrip).toHaveBeenCalledTimes(1));
    expect(createTrip).toHaveBeenCalledWith(
      expect.objectContaining({
        trip_name: "Saturday in Providence",
        default_time_zone_name: "America/Chicago",
        activities: [
          expect.objectContaining({
            place_id: "11111111-1111-4111-8111-111111111111",
            inbound_travel_mode: "walking",
            timing: expect.objectContaining({
              min_duration_seconds: 3_600,
              can_shorten: false,
            }),
          }),
        ],
      }),
      "csrf-token",
    );
  });
});
