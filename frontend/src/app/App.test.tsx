import { fireEvent, render, screen, waitFor } from "@testing-library/react";

import { ApiError } from "../api/client";
import type { LiveRouteApi } from "../api/client";
import type {
  ExecutionTransition,
  Session,
  Trip,
  TripList,
} from "../api/types";
import { TestApp } from "./App";

const session: Session = {
  user: {
    user_id: "11111111-1111-4111-8111-111111111111",
    display_name: "Nikhil",
    default_time_zone_name: "America/New_York",
  },
  csrf_token: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
  idle_expires_at_unix_ms: 1_786_291_200_000,
  absolute_expires_at_unix_ms: 1_788_883_200_000,
};

const trips: TripList = {
  inactive_trips: [
    {
      trip_id: "22222222-2222-4222-8222-222222222222",
      trip_name: "Saturday in Providence",
      trip_revision: "4",
      execution_state: "inactive",
    },
  ],
  current_execution_trip: {
    trip_id: "33333333-3333-4333-8333-333333333333",
    trip_name: "Newport afternoon",
    trip_revision: "2",
    execution_state: "active",
  },
};

const trip: Trip = {
  trip_id: "22222222-2222-4222-8222-222222222222",
  trip_name: "Saturday in Providence",
  default_time_zone_name: "America/New_York",
  trip_revision: "4",
  execution_state: "inactive",
  saved_plan: {
    saved_plan_id: "44444444-4444-4444-8444-444444444444",
    saved_plan_revision: "2",
    created_at_unix_ms: 1_786_291_200_000,
    activities: [
      {
        activity_id: "55555555-5555-4555-8555-555555555555",
        ordinal: 0,
        place: {
          place_id: "66666666-6666-4666-8666-666666666666",
          latitude: 41.824,
          longitude: -71.412,
          display_name: "Providence Station",
          time_zone_name: "America/New_York",
          created_at_unix_ms: 1_786_291_200_000,
        },
        schedule: {
          state: "scheduled",
          start_offset_ms: 3_600_000,
          end_offset_ms: 5_400_000,
        },
        inbound_travel_mode: "walking",
        activity_class: "flexible",
        priority_rank: 1,
        utility_score: 1,
        timing: {
          open_windows: [],
          reservation_grace_seconds: 0,
          min_duration_seconds: 1_800,
          preferred_duration_seconds: 1_800,
          max_duration_seconds: 1_800,
          mandatory: true,
          can_shorten: false,
          can_move: true,
          can_skip: false,
        },
      },
    ],
  },
  created_at_unix_ms: 1_786_291_200_000,
  updated_at_unix_ms: 1_786_291_200_000,
};

function api(overrides: Partial<LiveRouteApi> = {}): LiveRouteApi {
  return {
    createGoogleLoginNonce: async () => {
      throw new Error("Google login is not used in this test");
    },
    authenticateWithGoogle: async () => {
      throw new Error("Google login is not used in this test");
    },
    getSession: async () => session,
    listTrips: async () => trips,
    getTrip: async () => trip,
    createTrip: async () => trip,
    activateTrip: async () => {
      throw new Error("activation is not used in this test");
    },
    createWebSocketTicket: async () => {
      throw new Error("WebSocket tickets are not used in this test");
    },
    resolvePlace: async () => {
      throw new Error("place resolution is not used in this test");
    },
    createPlace: async () => {
      throw new Error("place creation is not used in this test");
    },
    ...overrides,
  };
}

describe("App session restoration", () => {
  it("restores the session and renders active and inactive trip summaries", async () => {
    render(<TestApp api={api()} />);

    expect(screen.getByLabelText("Restoring session")).toBeInTheDocument();
    expect(
      await screen.findByRole("heading", { name: "Trips" }),
    ).toBeInTheDocument();
    expect(screen.getByText("Saturday in Providence")).toBeInTheDocument();
    expect(screen.getByText("Newport afternoon")).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: "Open live view" }),
    ).toBeInTheDocument();
    expect(screen.getByText("Nikhil")).toBeInTheDocument();
  });

  it("shows the signed-out experience for an unauthenticated session", async () => {
    render(
      <TestApp
        api={api({
          getSession: async () => {
            throw new ApiError(401);
          },
        })}
      />,
    );

    expect(
      await screen.findByRole("heading", { name: /Build the plan/i }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Sign in with Google" }),
    ).toBeDisabled();
  });

  it("loads the full saved plan when an inactive trip is opened", async () => {
    render(
      <TestApp api={api()} initialEntries={[`/planner/${trip.trip_id}`]} />,
    );

    expect(
      await screen.findByRole("heading", { name: "Saturday in Providence" }),
    ).toBeInTheDocument();
    expect(screen.getByText("Providence Station")).toBeInTheDocument();
    expect(screen.getByText("01:00 – 01:30")).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent(
      "Set the public Mapbox token",
    );
  });

  it("starts a fully scheduled trip from the browser location", async () => {
    const getCurrentPosition = vi.fn((success: PositionCallback) => {
      success({
        coords: {
          latitude: 41.824,
          longitude: -71.412,
        },
      } as GeolocationPosition);
    });
    const originalGeolocation = navigator.geolocation;
    Object.defineProperty(navigator, "geolocation", {
      configurable: true,
      value: { getCurrentPosition },
    });
    const activateTrip = vi.fn(
      async (): Promise<ExecutionTransition> =>
        ({
          trip: { ...trip, execution_state: "active" },
          operation: {},
        }) as ExecutionTransition,
    );

    try {
      render(
        <TestApp
          api={api({ activateTrip })}
          initialEntries={["/planner/" + trip.trip_id]}
        />,
      );

      fireEvent.click(await screen.findByRole("button", { name: "Go" }));
      await waitFor(() =>
        expect(activateTrip).toHaveBeenCalledWith(
          trip.trip_id,
          {
            starting_location: { latitude: 41.824, longitude: -71.412 },
          },
          "4",
          session.csrf_token,
        ),
      );
      expect(await screen.findByText("active")).toBeInTheDocument();
    } finally {
      Object.defineProperty(navigator, "geolocation", {
        configurable: true,
        value: originalGeolocation,
      });
    }
  });

  it("keeps a new trip local until it has a valid activity", async () => {
    render(<TestApp api={api()} initialEntries={["/planner/new"]} />);

    expect(
      await screen.findByRole("heading", { name: "Plan a new trip" }),
    ).toBeInTheDocument();
    expect(
      screen.getByText("Trip timezone: America/New_York"),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/at least one complete activity/i),
    ).toBeInTheDocument();
  });

  it("retries a transient restoration failure", async () => {
    let calls = 0;
    const retryingApi = api({
      getSession: async () => {
        calls += 1;
        if (calls === 1) throw new ApiError(503);
        return session;
      },
    });
    render(<TestApp api={retryingApi} />);

    fireEvent.click(await screen.findByRole("button", { name: "Try again" }));
    await waitFor(() =>
      expect(
        screen.getByRole("heading", { name: "Trips" }),
      ).toBeInTheDocument(),
    );
    expect(calls).toBe(2);
  });
});
