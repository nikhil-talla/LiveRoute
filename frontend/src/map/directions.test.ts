import {
  buildDirectionsRequests,
  MAX_DIRECTIONS_COORDINATES,
  RouteCache,
} from "./directions";
import type { Trip } from "../api/types";

function tripWithModes(modes: Array<"walking" | "driving">): Trip {
  return {
    saved_plan: {
      saved_plan_id: "plan-id",
      saved_plan_revision: "7",
      activities: modes.map((inbound_travel_mode, ordinal) => ({
        ordinal,
        place: {
          latitude: ordinal,
          longitude: 100 + ordinal,
        },
        inbound_travel_mode,
      })),
    },
  } as Trip;
}

describe("Mapbox Directions planning requests", () => {
  it("preserves order, groups consecutive modes, and overlaps chunks", () => {
    const modes = Array.from(
      { length: MAX_DIRECTIONS_COORDINATES + 2 },
      () => "driving" as const,
    );
    const requests = buildDirectionsRequests(tripWithModes(modes));

    expect(requests).toHaveLength(2);
    expect(requests[0]?.profile).toBe("mapbox/driving");
    expect(requests[0]?.coordinates).toHaveLength(25);
    expect(requests[1]?.coordinates).toHaveLength(3);
    expect(requests[1]?.coordinates[0]).toEqual(
      requests[0]?.coordinates.at(-1),
    );
    expect(requests[1]?.coordinates.at(-1)).toEqual([126, 26]);
  });

  it("uses the destination activity mode for each ordered leg", () => {
    const requests = buildDirectionsRequests(
      tripWithModes(["driving", "walking", "walking", "driving", "driving"]),
    );

    expect(requests.map((request) => request.profile)).toEqual([
      "mapbox/walking",
      "mapbox/driving",
    ]);
    expect(requests[0]?.coordinates).toEqual([
      [100, 0],
      [101, 1],
      [102, 2],
    ]);
    expect(requests[1]?.coordinates).toEqual([
      [102, 2],
      [103, 3],
      [104, 4],
    ]);
  });

  it("reuses a successful route request from the canonical cache", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({
          routes: [
            {
              geometry: {
                type: "LineString",
                coordinates: [
                  [100, 0],
                  [101, 1],
                ],
              },
            },
          ],
        }),
        { status: 200, headers: { "content-type": "application/json" } },
      ),
    );
    const request = buildDirectionsRequests(
      tripWithModes(["driving", "driving"]),
    )[0]!;
    const cache = new RouteCache();

    await cache.get(request, "public-token");
    await cache.get(request, "public-token");

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(fetchMock.mock.calls[0]?.[0]).toContain(
      "mapbox.com/directions/v5/mapbox/driving/100,0;101,1",
    );
  });
});
