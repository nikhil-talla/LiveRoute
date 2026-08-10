import {
  admitGpsPosition,
  GpsTelemetryController,
  type GpsPlatform,
} from "./gpsTelemetry";

const firstMessageId = "11111111-1111-4111-8111-111111111111";
const secondMessageId = "22222222-2222-4222-8222-222222222222";

function position(
  timestamp: number,
  latitude = 41.824,
  longitude = -71.412,
  accuracy = 10,
): GeolocationPosition {
  return {
    timestamp,
    coords: { latitude, longitude, accuracy },
  } as GeolocationPosition;
}

function fakePlatform(): {
  platform: GpsPlatform;
  emitPosition: (value: GeolocationPosition) => void;
  emitError: (code: number) => void;
  setVisible: (value: boolean) => void;
  setOnline: (value: boolean) => void;
  watchOptions: PositionOptions[];
  clearWatch: ReturnType<typeof vi.fn>;
} {
  let visible = true;
  let online = true;
  let positionCallback: PositionCallback | null = null;
  let errorCallback: PositionErrorCallback | null = null;
  const visibilityListeners = new Set<() => void>();
  const onlineListeners = new Set<() => void>();
  const offlineListeners = new Set<() => void>();
  const watchOptions: PositionOptions[] = [];
  const clearWatch = vi.fn();
  const platform: GpsPlatform = {
    geolocation: {
      watchPosition: vi.fn((success, error, options) => {
        positionCallback = success;
        errorCallback = error;
        watchOptions.push(options ?? {});
        return watchOptions.length;
      }),
      clearWatch,
    },
    isVisible: () => visible,
    isOnline: () => online,
    onVisibilityChange: (listener) => {
      visibilityListeners.add(listener);
      return () => visibilityListeners.delete(listener);
    },
    onOnline: (listener) => {
      onlineListeners.add(listener);
      return () => onlineListeners.delete(listener);
    },
    onOffline: (listener) => {
      offlineListeners.add(listener);
      return () => offlineListeners.delete(listener);
    },
  };
  return {
    platform,
    emitPosition: (value) => positionCallback?.(value),
    emitError: (code) =>
      errorCallback?.({
        code,
        message: "test",
        PERMISSION_DENIED: 1,
        POSITION_UNAVAILABLE: 2,
        TIMEOUT: 3,
      } as GeolocationPositionError),
    setVisible: (value) => {
      visible = value;
      visibilityListeners.forEach((listener) => listener());
    },
    setOnline: (value) => {
      online = value;
      (value ? onlineListeners : offlineListeners).forEach((listener) =>
        listener(),
      );
    },
    watchOptions,
    clearWatch,
  };
}

describe("GPS telemetry admission", () => {
  it("rejects invalid, inaccurate, stale, and future samples", () => {
    const now = 1_000_000;
    expect(admitGpsPosition(position(now, 41, -72, 50), now)).not.toBeNull();
    expect(admitGpsPosition(position(now, 91), now)).toBeNull();
    expect(admitGpsPosition(position(now, 41, -72, 51), now)).toBeNull();
    expect(admitGpsPosition(position(now - 10_001), now)).toBeNull();
    expect(admitGpsPosition(position(now + 1_001), now)).toBeNull();
    expect(
      admitGpsPosition(position(now, 41, -72, Number.NaN), now),
    ).toBeNull();
  });

  it("uses exact watch options and sends only the newest pending movement", () => {
    let now = 1_000_000;
    const fake = fakePlatform();
    const sendLocationTelemetry = vi
      .fn()
      .mockReturnValueOnce(firstMessageId)
      .mockReturnValueOnce(secondMessageId);
    const controller = new GpsTelemetryController({
      platform: fake.platform,
      sender: { sendLocationTelemetry },
      now: () => now,
      onLocation: vi.fn(),
      onStale: vi.fn(),
    });

    controller.start();
    expect(fake.watchOptions).toEqual([
      { enableHighAccuracy: true, maximumAge: 2000, timeout: 10000 },
    ]);
    fake.emitPosition(position(now));
    expect(sendLocationTelemetry).toHaveBeenCalledTimes(1);

    now += 1_100;
    fake.emitPosition(position(now, 41.8242));
    fake.emitPosition(position(now, 41.8243));
    expect(sendLocationTelemetry).toHaveBeenCalledTimes(1);

    controller.handleTelemetryStatus({
      kind: "telemetry_status",
      payload: { message_id: firstMessageId, disposition: "accepted" },
    });
    expect(sendLocationTelemetry).toHaveBeenCalledTimes(2);
    expect(sendLocationTelemetry).toHaveBeenLastCalledWith(
      expect.objectContaining({ latitude: 41.8243, observedAtUnixMs: now }),
    );
    controller.stop();
  });

  it("enforces the stationary heartbeat and clears state across visibility recovery", () => {
    let now = 2_000_000;
    const fake = fakePlatform();
    const sendLocationTelemetry = vi
      .fn()
      .mockReturnValue(firstMessageId)
      .mockReturnValueOnce(firstMessageId)
      .mockReturnValueOnce(secondMessageId);
    const controller = new GpsTelemetryController({
      platform: fake.platform,
      sender: { sendLocationTelemetry },
      now: () => now,
      onLocation: vi.fn(),
      onStale: vi.fn(),
    });

    controller.start();
    fake.emitPosition(position(now));
    now += 4_999;
    fake.emitPosition(position(now));
    expect(sendLocationTelemetry).toHaveBeenCalledTimes(1);
    now += 1;
    fake.emitPosition(position(now));
    expect(sendLocationTelemetry).toHaveBeenCalledTimes(1);
    controller.handleTelemetryStatus({
      kind: "telemetry_status",
      payload: { message_id: firstMessageId, disposition: "accepted" },
    });
    expect(sendLocationTelemetry).toHaveBeenCalledTimes(2);

    fake.setVisible(false);
    expect(fake.clearWatch).toHaveBeenCalled();
    fake.setVisible(true);
    now += 1;
    fake.emitPosition(position(now));
    expect(sendLocationTelemetry).toHaveBeenCalledTimes(3);
    controller.stop();
  });

  it("stops on permission denial, reports stale state, and times out unacknowledged data", () => {
    vi.useFakeTimers();
    try {
      let now = 3_000_000;
      const fake = fakePlatform();
      const sendLocationTelemetry = vi.fn().mockReturnValue(firstMessageId);
      const onPermissionDenied = vi.fn();
      const onAcknowledgementTimeout = vi.fn();
      const onStale = vi.fn();
      const controller = new GpsTelemetryController({
        platform: fake.platform,
        sender: { sendLocationTelemetry },
        now: () => now,
        onLocation: vi.fn(),
        onStale,
        onPermissionDenied,
        onAcknowledgementTimeout,
      });

      controller.start();
      fake.emitError(1);
      expect(onPermissionDenied).toHaveBeenCalledOnce();
      now += 15_000;
      vi.advanceTimersByTime(15_000);
      expect(onStale).toHaveBeenCalledWith(true);
      fake.emitPosition(position(now));
      expect(sendLocationTelemetry).toHaveBeenCalledTimes(0);
      controller.retryPermission();
      fake.emitPosition(position(now));
      expect(sendLocationTelemetry).toHaveBeenCalledTimes(1);
      vi.advanceTimersByTime(10_000);
      expect(onAcknowledgementTimeout).toHaveBeenCalledOnce();
      controller.stop();
    } finally {
      vi.useRealTimers();
    }
  });
});
