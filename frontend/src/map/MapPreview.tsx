import { useEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
import mapboxgl from "mapbox-gl";

import type { Trip } from "../api/types";
import type { GpsLocationSample } from "../live/gpsTelemetry";
import {
  buildDirectionsRequests,
  directionsRequestKey,
  RouteCache,
  type RouteGeometry,
} from "./directions";

interface MapPreviewProps {
  trip: Trip;
  currentLocation?: GpsLocationSample | null;
  onCanonicalRoutes?: (geometries: RouteGeometry[]) => void;
  routeOverride?: RouteGeometry | null;
}

function configuredToken(): string {
  return import.meta.env.VITE_MAPBOX_PUBLIC_ACCESS_TOKEN?.trim() ?? "";
}

export function MapPreview({
  trip,
  currentLocation = null,
  onCanonicalRoutes,
  routeOverride = null,
}: MapPreviewProps): ReactNode {
  const containerRef = useRef<HTMLDivElement>(null);
  const mapRef = useRef<mapboxgl.Map | null>(null);
  const currentMarkerRef = useRef<mapboxgl.Marker | null>(null);
  const routeCacheRef = useRef(new RouteCache());
  const token = configuredToken();
  const [mapReady, setMapReady] = useState(0);
  const [routeState, setRouteState] = useState<
    | { kind: "idle" }
    | { kind: "loading" }
    | { kind: "ready"; geometries: RouteGeometry[] }
    | { kind: "error"; message: string }
  >({ kind: "idle" });

  useEffect(() => {
    if (!token) {
      setRouteState({ kind: "idle" });
      return;
    }

    const controller = new AbortController();
    const requests = buildDirectionsRequests(trip);
    const keys = new Set(requests.map(directionsRequestKey));
    routeCacheRef.current.retain(trip.saved_plan.saved_plan_id, keys);
    if (requests.length === 0) {
      setRouteState({ kind: "ready", geometries: [] });
      onCanonicalRoutes?.([]);
      return () => controller.abort();
    }

    setRouteState({ kind: "loading" });
    void Promise.all(
      requests.map((request) =>
        routeCacheRef.current.get(request, token, controller.signal),
      ),
    )
      .then((geometries) => {
        if (!controller.signal.aborted) {
          setRouteState({ kind: "ready", geometries });
          onCanonicalRoutes?.(geometries);
        }
      })
      .catch((error: unknown) => {
        if (!controller.signal.aborted) {
          setRouteState({
            kind: "error",
            message:
              error instanceof Error
                ? error.message
                : "The route preview could not be loaded.",
          });
        }
      });

    return () => controller.abort();
  }, [onCanonicalRoutes, token, trip]);

  useEffect(() => {
    if (!token || !containerRef.current) return;

    mapboxgl.accessToken = token;
    const first = trip.saved_plan.activities[0]?.place;
    const map = new mapboxgl.Map({
      container: containerRef.current,
      style: "mapbox://styles/mapbox/streets-v12",
      center: first ? [first.longitude, first.latitude] : [-71.412, 41.824],
      zoom: first ? 11 : 9,
      attributionControl: true,
    });
    mapRef.current = map;
    setMapReady((value) => value + 1);

    map.addControl(new mapboxgl.NavigationControl(), "top-right");
    const bounds = new mapboxgl.LngLatBounds();
    for (const activity of trip.saved_plan.activities) {
      const { latitude, longitude } = activity.place;
      new mapboxgl.Marker({ color: "#196a54" })
        .setLngLat([longitude, latitude])
        .setPopup(new mapboxgl.Popup().setText(activity.place.display_name))
        .addTo(map);
      bounds.extend([longitude, latitude]);
    }
    if (trip.saved_plan.activities.length > 1) {
      map.fitBounds(bounds, { padding: 64, maxZoom: 13 });
    }

    const addRouteLayers = (): void => {
      if (routeState.kind !== "ready") return;
      const geometries = routeOverride
        ? [...routeState.geometries, routeOverride]
        : routeState.geometries;
      geometries.forEach((geometry, index) => {
        const sourceId = "canonical-route-" + String(index);
        map.addSource(sourceId, {
          type: "geojson",
          data: {
            type: "Feature",
            properties: {},
            geometry: {
              type: "LineString",
              coordinates: geometry.coordinates,
            },
          },
        });
        map.addLayer({
          id: sourceId,
          type: "line",
          source: sourceId,
          layout: { "line-cap": "round", "line-join": "round" },
          paint: {
            "line-color": "#f1785f",
            "line-width": 4,
            "line-opacity": 0.86,
          },
        });
      });
    };
    if (routeState.kind === "ready") {
      if (map.isStyleLoaded()) {
        addRouteLayers();
      } else {
        map.once("load", addRouteLayers);
      }
    }

    return () => {
      map.off("load", addRouteLayers);
      currentMarkerRef.current?.remove();
      currentMarkerRef.current = null;
      mapRef.current = null;
      map.remove();
    };
  }, [routeOverride, routeState, token, trip]);

  useEffect(() => {
    const map = mapRef.current;
    if (!map || !currentLocation) {
      currentMarkerRef.current?.remove();
      currentMarkerRef.current = null;
      return;
    }
    if (!currentMarkerRef.current) {
      currentMarkerRef.current = new mapboxgl.Marker({ color: "#246bfe" })
        .setPopup(new mapboxgl.Popup().setText("Current location"))
        .addTo(map);
    }
    currentMarkerRef.current.setLngLat([
      currentLocation.longitude,
      currentLocation.latitude,
    ]);
  }, [currentLocation, mapReady]);

  if (!token) {
    return (
      <div className="map-preview map-preview-unconfigured" role="status">
        <p className="eyebrow">Map preview</p>
        <p>
          Set the public Mapbox token for this deployment to view the stops on a
          map.
        </p>
      </div>
    );
  }

  return (
    <>
      <div ref={containerRef} className="map-preview" aria-label="Trip map" />
      {routeState.kind === "loading" ? (
        <p className="map-preview-note" role="status">
          Loading the canonical route…
        </p>
      ) : null}
      {routeState.kind === "error" ? (
        <p className="map-preview-error" role="alert">
          {routeState.message}
        </p>
      ) : null}
    </>
  );
}
