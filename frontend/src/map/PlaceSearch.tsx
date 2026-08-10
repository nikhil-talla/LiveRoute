import { useState } from "react";
import type { ReactNode } from "react";
import { SearchBox } from "@mapbox/search-js-react";
import type { SearchBoxRetrieveResponse } from "@mapbox/search-js-core";

import type { LiveRouteApi } from "../api/client";
import type { Coordinate, Place } from "../api/types";

interface PlaceSearchProps {
  api: LiveRouteApi;
  csrfToken: string;
  onPlaceConfirmed?: (place: Place) => void;
}

interface TemporarySelection {
  name: string;
  coordinate: Coordinate;
}

function configuredToken(): string {
  return import.meta.env.VITE_MAPBOX_PUBLIC_ACCESS_TOKEN?.trim() ?? "";
}

function firstCoordinate(
  response: SearchBoxRetrieveResponse,
): Coordinate | null {
  const feature = response.features[0];
  const coordinates = feature?.properties.coordinates;
  if (!coordinates) return null;
  return {
    latitude: coordinates.latitude,
    longitude: coordinates.longitude,
  };
}

export function PlaceSearch({
  api,
  csrfToken,
  onPlaceConfirmed,
}: PlaceSearchProps): ReactNode {
  const token = configuredToken();
  const [selection, setSelection] = useState<TemporarySelection | null>(null);
  const [resolution, setResolution] = useState<
    | { kind: "idle" }
    | { kind: "loading" }
    | {
        kind: "ready";
        token: string;
        displayName: string;
        coordinate: Coordinate;
      }
    | { kind: "confirmed"; place: Place }
    | { kind: "error"; message: string }
  >({ kind: "idle" });

  if (!token) {
    return (
      <section className="place-search" aria-labelledby="place-search-title">
        <p className="eyebrow">Add a place</p>
        <h2 id="place-search-title">Search is not configured</h2>
        <p>
          Set the public Mapbox token to search for a temporary place result.
        </p>
      </section>
    );
  }

  const handleRetrieve = (response: SearchBoxRetrieveResponse): void => {
    const feature = response.features[0];
    const coordinate = firstCoordinate(response);
    if (!feature || !coordinate) {
      setResolution({
        kind: "error",
        message: "That result has no usable coordinate.",
      });
      return;
    }
    setSelection({
      name: feature.properties.name,
      coordinate,
    });
    setResolution({ kind: "idle" });
  };

  const resolveSelection = async (): Promise<void> => {
    if (!selection) return;
    setResolution({ kind: "loading" });
    try {
      const result = await api.resolvePlace(selection.coordinate, csrfToken);
      setResolution({
        kind: "ready",
        token: result.resolution_token,
        displayName: result.candidate.display_name,
        coordinate: {
          latitude: result.candidate.latitude,
          longitude: result.candidate.longitude,
        },
      });
    } catch (error: unknown) {
      setResolution({
        kind: "error",
        message:
          error instanceof Error
            ? error.message
            : "The selected place could not be resolved.",
      });
    }
  };

  const confirmResolution = async (): Promise<void> => {
    if (resolution.kind !== "ready") return;
    setResolution({ kind: "loading" });
    try {
      const place = await api.createPlace(resolution.token, csrfToken);
      setResolution({ kind: "confirmed", place });
      onPlaceConfirmed?.(place);
    } catch (error: unknown) {
      setResolution({
        kind: "error",
        message:
          error instanceof Error
            ? error.message
            : "The permanent place could not be saved.",
      });
    }
  };

  return (
    <section className="place-search" aria-labelledby="place-search-title">
      <p className="eyebrow">Add a place</p>
      <h2 id="place-search-title">Where are you going?</h2>
      <p className="place-search-note">
        Search results are temporary until you confirm the permanent location.
      </p>
      <SearchBox
        accessToken={token}
        placeholder="Search restaurants, museums, addresses…"
        options={{ country: "US", language: "en" }}
        onRetrieve={handleRetrieve}
      />
      {selection ? (
        <div className="place-selection" aria-label="Temporary place selection">
          <strong>{selection.name}</strong>
          <small>
            {selection.coordinate.latitude.toFixed(6)},{" "}
            {selection.coordinate.longitude.toFixed(6)}
          </small>
          <button
            className="primary-button"
            type="button"
            onClick={() => void resolveSelection()}
            disabled={resolution.kind === "loading"}
          >
            Use this location
          </button>
        </div>
      ) : null}
      {resolution.kind === "ready" ? (
        <div className="place-confirmation" role="alert">
          <p className="eyebrow">Confirm permanent location</p>
          <strong>{resolution.displayName}</strong>
          <small>
            {resolution.coordinate.latitude.toFixed(6)},{" "}
            {resolution.coordinate.longitude.toFixed(6)}
          </small>
          <button
            className="primary-button"
            type="button"
            onClick={() => void confirmResolution()}
          >
            Confirm this place
          </button>
        </div>
      ) : null}
      {resolution.kind === "confirmed" ? (
        <div className="place-confirmation" role="status">
          <p className="eyebrow">Place saved</p>
          <strong>{resolution.place.display_name}</strong>
          <small>{resolution.place.time_zone_name}</small>
        </div>
      ) : null}
      {resolution.kind === "error" ? (
        <p className="place-error" role="alert">
          {resolution.message}
        </p>
      ) : null}
    </section>
  );
}
