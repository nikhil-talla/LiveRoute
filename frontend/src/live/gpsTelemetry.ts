import gpsPolicy from "./gps-policy.json";

export interface GpsLocationSample {
  latitude: number;
  longitude: number;
  accuracy: number;
  observedAtUnixMs: number;
}

export interface LocationTelemetrySender {
  sendLocationTelemetry(sample: {
    latitude: number;
    longitude: number;
    observedAtUnixMs: number;
  }): string | null;
}

export interface GpsPlatform {
  geolocation: Pick<Geolocation, "watchPosition" | "clearWatch">;
  isVisible: () => boolean;
  isOnline: () => boolean;
  onVisibilityChange: (listener: () => void) => () => void;
  onOnline: (listener: () => void) => () => void;
  onOffline: (listener: () => void) => () => void;
}

export interface GpsTelemetryControllerOptions {
  platform: GpsPlatform;
  sender: LocationTelemetrySender;
  now?: () => number;
  onLocation: (sample: GpsLocationSample) => void;
  onStale: (stale: boolean) => void;
  onPermissionDenied?: () => void;
  onAcknowledgementTimeout?: () => void;
}

export function browserGpsPlatform(): GpsPlatform {
  return {
    geolocation: navigator.geolocation,
    isVisible: () => document.visibilityState === "visible",
    isOnline: () => navigator.onLine,
    onVisibilityChange: (listener) => {
      document.addEventListener("visibilitychange", listener);
      return () => document.removeEventListener("visibilitychange", listener);
    },
    onOnline: (listener) => {
      window.addEventListener("online", listener);
      return () => window.removeEventListener("online", listener);
    },
    onOffline: (listener) => {
      window.addEventListener("offline", listener);
      return () => window.removeEventListener("offline", listener);
    },
  };
}

interface PendingSample extends GpsLocationSample {
  receivedAtUnixMs: number;
}

interface InFlightSample {
  messageId: string;
  sample: PendingSample;
}

const EARTH_RADIUS_METERS = 6_371_008.8;
const maxAccuracyMeters = gpsPolicy.maximum_accepted_accuracy_meters;
const maxSampleAgeMs = gpsPolicy.maximum_sample_age_ms;
const maxFutureSkewMs = gpsPolicy.maximum_future_skew_ms;
const minimumSendIntervalMs = gpsPolicy.minimum_send_interval_ms;
const movementThresholdMeters = gpsPolicy.movement_threshold_meters;
const stationaryHeartbeatIntervalMs =
  gpsPolicy.stationary_heartbeat_interval_ms;
const acknowledgementTimeoutMs = gpsPolicy.telemetry_ack_timeout_ms;
const staleAfterMs = gpsPolicy.stale_ui_after_ms;

function finite(value: number): boolean {
  return Number.isFinite(value);
}

export function haversineDistanceMeters(
  first: Pick<GpsLocationSample, "latitude" | "longitude">,
  second: Pick<GpsLocationSample, "latitude" | "longitude">,
): number {
  const toRadians = (degrees: number): number => (degrees * Math.PI) / 180;
  const latitudeDelta = toRadians(second.latitude - first.latitude);
  const longitudeDelta = toRadians(second.longitude - first.longitude);
  const firstLatitude = toRadians(first.latitude);
  const secondLatitude = toRadians(second.latitude);
  const haversine =
    Math.sin(latitudeDelta / 2) ** 2 +
    Math.cos(firstLatitude) *
      Math.cos(secondLatitude) *
      Math.sin(longitudeDelta / 2) ** 2;
  return 2 * EARTH_RADIUS_METERS * Math.asin(Math.sqrt(haversine));
}

export function admitGpsPosition(
  position: GeolocationPosition,
  nowUnixMs: number,
): GpsLocationSample | null {
  const latitude = position.coords.latitude;
  const longitude = position.coords.longitude;
  const accuracy = position.coords.accuracy;
  const observedAtUnixMs = Math.trunc(position.timestamp);
  if (
    !finite(latitude) ||
    !finite(longitude) ||
    !finite(accuracy) ||
    !finite(observedAtUnixMs) ||
    latitude < -90 ||
    latitude > 90 ||
    longitude < -180 ||
    longitude > 180 ||
    accuracy <= 0 ||
    accuracy > maxAccuracyMeters ||
    nowUnixMs - observedAtUnixMs > maxSampleAgeMs ||
    observedAtUnixMs - nowUnixMs > maxFutureSkewMs
  ) {
    return null;
  }
  return { latitude, longitude, accuracy, observedAtUnixMs };
}

export class GpsTelemetryController {
  readonly #options: GpsTelemetryControllerOptions;
  readonly #now: () => number;
  #started = false;
  #permissionDenied = false;
  #watchId: number | null = null;
  #removeVisibilityListener: (() => void) | null = null;
  #removeOnlineListener: (() => void) | null = null;
  #removeOfflineListener: (() => void) | null = null;
  #lastEligibleReceivedAt: number | null = null;
  #stale = false;
  #lastSent: PendingSample | null = null;
  #lastSentAt: number | null = null;
  #pending: PendingSample | null = null;
  #inFlight: InFlightSample | null = null;
  #flushTimer: ReturnType<typeof setTimeout> | null = null;
  #acknowledgementTimer: ReturnType<typeof setTimeout> | null = null;
  #staleTimer: ReturnType<typeof setInterval> | null = null;

  constructor(options: GpsTelemetryControllerOptions) {
    this.#options = options;
    this.#now = options.now ?? Date.now;
  }

  start(): void {
    if (this.#started) return;
    this.#started = true;
    this.#lastEligibleReceivedAt = this.#now();
    this.#removeVisibilityListener = this.#options.platform.onVisibilityChange(
      () => this.#handleVisibilityChange(),
    );
    this.#removeOnlineListener = this.#options.platform.onOnline(() =>
      this.#handleOnline(),
    );
    this.#removeOfflineListener = this.#options.platform.onOffline(() =>
      this.#handleOffline(),
    );
    this.#staleTimer = setInterval(() => this.#updateStale(), 1000);
    if (
      this.#options.platform.isVisible() &&
      this.#options.platform.isOnline()
    ) {
      this.#startWatch();
    }
  }

  stop(): void {
    if (!this.#started) return;
    this.#started = false;
    this.#stopWatch();
    this.#discardBufferedSamples();
    this.#clearTimers();
    this.#removeVisibilityListener?.();
    this.#removeOnlineListener?.();
    this.#removeOfflineListener?.();
    this.#removeVisibilityListener = null;
    this.#removeOnlineListener = null;
    this.#removeOfflineListener = null;
    if (this.#staleTimer) clearInterval(this.#staleTimer);
    this.#staleTimer = null;
    this.#setStale(false);
  }

  retryPermission(): void {
    this.#permissionDenied = false;
    if (this.#started && this.#options.platform.isVisible()) {
      this.#startWatch();
    }
  }

  connectionLost(): void {
    this.#stopWatch();
    this.#discardBufferedSamples();
  }

  connectionRecovered(): void {
    if (
      this.#started &&
      !this.#permissionDenied &&
      this.#options.platform.isVisible() &&
      this.#options.platform.isOnline()
    ) {
      this.#startWatch();
    }
  }

  handleTelemetryStatus(message: {
    kind: string;
    payload: Record<string, unknown>;
  }): void {
    if (message.kind !== "telemetry_status" || !this.#inFlight) return;
    if (message.payload.message_id !== this.#inFlight.messageId) return;
    const disposition = message.payload.disposition;
    if (
      disposition !== "accepted" &&
      disposition !== "coalesced" &&
      disposition !== "dropped" &&
      disposition !== "rejected"
    ) {
      return;
    }
    this.#inFlight = null;
    if (this.#acknowledgementTimer) {
      clearTimeout(this.#acknowledgementTimer);
      this.#acknowledgementTimer = null;
    }
    this.#flushPending();
  }

  #handlePosition = (position: GeolocationPosition): void => {
    if (!this.#started) return;
    const receivedAtUnixMs = this.#now();
    const sample = admitGpsPosition(position, receivedAtUnixMs);
    if (!sample) return;
    const pendingSample = { ...sample, receivedAtUnixMs };
    this.#lastEligibleReceivedAt = receivedAtUnixMs;
    this.#setStale(false);
    this.#options.onLocation(sample);
    if (
      this.#permissionDenied ||
      !this.#options.platform.isVisible() ||
      !this.#options.platform.isOnline()
    ) {
      return;
    }
    if (
      this.#lastSent &&
      haversineDistanceMeters(this.#lastSent, pendingSample) <
        movementThresholdMeters &&
      this.#lastSentAt !== null &&
      receivedAtUnixMs - this.#lastSentAt < stationaryHeartbeatIntervalMs
    ) {
      return;
    }
    this.#pending = pendingSample;
    this.#flushPending();
  };

  #handlePositionError = (error: GeolocationPositionError): void => {
    if (error.code === error.PERMISSION_DENIED) {
      this.#permissionDenied = true;
      this.#stopWatch();
      this.#discardBufferedSamples();
      this.#options.onPermissionDenied?.();
    }
  };

  #handleVisibilityChange(): void {
    if (!this.#started) return;
    if (!this.#options.platform.isVisible()) {
      this.#stopWatch();
      this.#discardBufferedSamples();
      return;
    }
    this.#startWatch();
  }

  #handleOnline(): void {
    if (!this.#started) return;
    this.#startWatch();
  }

  #handleOffline(): void {
    if (!this.#started) return;
    this.#stopWatch();
    this.#discardBufferedSamples();
  }

  #startWatch(): void {
    if (
      this.#watchId !== null ||
      this.#permissionDenied ||
      !this.#options.platform.isVisible() ||
      !this.#options.platform.isOnline()
    ) {
      return;
    }
    try {
      this.#watchId = this.#options.platform.geolocation.watchPosition(
        this.#handlePosition,
        this.#handlePositionError,
        {
          enableHighAccuracy: gpsPolicy.enable_high_accuracy,
          maximumAge: gpsPolicy.maximum_age_ms,
          timeout: gpsPolicy.position_timeout_ms,
        },
      );
    } catch {
      this.#watchId = null;
    }
  }

  #stopWatch(): void {
    if (this.#watchId === null) return;
    this.#options.platform.geolocation.clearWatch(this.#watchId);
    this.#watchId = null;
  }

  #flushPending(): void {
    if (!this.#pending || this.#inFlight || !this.#started) return;
    const now = this.#now();
    if (now - this.#pending.observedAtUnixMs > maxSampleAgeMs) {
      this.#pending = null;
      return;
    }
    if (this.#lastSentAt !== null) {
      const elapsed = now - this.#lastSentAt;
      if (elapsed < minimumSendIntervalMs) {
        this.#scheduleFlush(minimumSendIntervalMs - elapsed);
        return;
      }
    }
    if (
      !this.#options.platform.isVisible() ||
      !this.#options.platform.isOnline()
    ) {
      this.#discardBufferedSamples();
      return;
    }
    const sample = this.#pending;
    const messageId = this.#options.sender.sendLocationTelemetry(sample);
    this.#pending = null;
    if (!messageId) {
      this.#discardBufferedSamples();
      return;
    }
    this.#lastSent = sample;
    this.#lastSentAt = now;
    this.#inFlight = { messageId, sample };
    this.#acknowledgementTimer = setTimeout(() => {
      this.#acknowledgementTimer = null;
      this.#inFlight = null;
      this.#pending = null;
      this.#lastSent = null;
      this.#lastSentAt = null;
      this.#options.onAcknowledgementTimeout?.();
    }, acknowledgementTimeoutMs);
  }

  #scheduleFlush(delayMs: number): void {
    if (this.#flushTimer) return;
    this.#flushTimer = setTimeout(() => {
      this.#flushTimer = null;
      this.#flushPending();
    }, delayMs);
  }

  #discardBufferedSamples(): void {
    this.#pending = null;
    this.#inFlight = null;
    this.#lastSent = null;
    this.#lastSentAt = null;
    this.#clearTimers();
  }

  #clearTimers(): void {
    if (this.#flushTimer) clearTimeout(this.#flushTimer);
    if (this.#acknowledgementTimer) clearTimeout(this.#acknowledgementTimer);
    this.#flushTimer = null;
    this.#acknowledgementTimer = null;
  }

  #updateStale(): void {
    if (!this.#started || this.#lastEligibleReceivedAt === null) return;
    this.#setStale(this.#now() - this.#lastEligibleReceivedAt >= staleAfterMs);
  }

  #setStale(stale: boolean): void {
    if (this.#stale === stale) return;
    this.#stale = stale;
    this.#options.onStale(stale);
  }
}
