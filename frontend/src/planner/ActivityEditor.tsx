import type { ChangeEvent, ReactNode } from "react";

import type { ActivityInput } from "../api/types";

interface ActivityEditorProps {
  value: ActivityInput;
  onChange: (value: ActivityInput) => void;
}

function numberValue(event: ChangeEvent<HTMLInputElement>): number {
  const value = Number(event.target.value);
  return Number.isFinite(value) ? Math.trunc(value) : 0;
}

function isScheduled(
  schedule: ActivityInput["schedule"],
): schedule is Extract<ActivityInput["schedule"], { state: "scheduled" }> {
  return schedule.state === "scheduled";
}

function formatDuration(seconds: number): string {
  if (seconds % 3600 === 0) return String(seconds / 3600) + " hour(s)";
  if (seconds % 60 === 0) return String(seconds / 60) + " minute(s)";
  return String(seconds) + " second(s)";
}

export function ActivityEditor({
  value,
  onChange,
}: ActivityEditorProps): ReactNode {
  const update = (changes: Partial<ActivityInput>): void => {
    onChange({ ...value, ...changes });
  };

  const updateTiming = (changes: Partial<ActivityInput["timing"]>): void => {
    update({ timing: { ...value.timing, ...changes } });
  };

  const updateWindow = (
    changes: Partial<ActivityInput["timing"]["open_windows"][number]>,
  ): void => {
    const [window] = value.timing.open_windows;
    if (!window) return;
    updateTiming({
      open_windows: [
        { ...window, ...changes },
        ...value.timing.open_windows.slice(1),
      ],
    });
  };

  const setReservation = (enabled: boolean): void => {
    const timing = { ...value.timing };
    if (enabled) {
      timing.reservation_start_offset_ms = 0;
    } else {
      delete timing.reservation_start_offset_ms;
    }
    update({ timing });
  };

  const setDeadline = (enabled: boolean): void => {
    const timing = { ...value.timing };
    if (enabled) {
      timing.mandatory_deadline_offset_ms = 86_400_000;
    } else {
      delete timing.mandatory_deadline_offset_ms;
    }
    update({ timing });
  };

  const scheduledSchedule = isScheduled(value.schedule) ? value.schedule : null;

  return (
    <section
      className="trip-section activity-editor"
      aria-labelledby="activity-settings-title"
    >
      <div className="section-heading">
        <div>
          <p className="eyebrow">Activity settings</p>
          <h2 id="activity-settings-title">Review this stop</h2>
        </div>
        <span aria-label="Activity number">{value.ordinal + 1}</span>
      </div>

      <div className="form-grid">
        <label className="field-label">
          Travel mode
          <select
            className="text-input"
            value={value.inbound_travel_mode}
            onChange={(event) =>
              update({
                inbound_travel_mode: event.target.value as
                  "walking" | "driving",
              })
            }
          >
            <option value="driving">Driving</option>
            <option value="walking">Walking</option>
          </select>
        </label>
        <label className="field-label">
          Activity class
          <select
            className="text-input"
            value={value.activity_class}
            onChange={(event) =>
              update({
                activity_class: event.target.value as "fixed" | "flexible",
              })
            }
          >
            <option value="flexible">Flexible</option>
            <option value="fixed">Fixed</option>
          </select>
        </label>
        <label className="field-label">
          Priority
          <input
            className="text-input"
            type="number"
            value={value.priority_rank}
            onChange={(event) => update({ priority_rank: numberValue(event) })}
          />
        </label>
        <label className="field-label">
          Utility
          <input
            className="text-input"
            type="number"
            value={value.utility_score}
            onChange={(event) => update({ utility_score: numberValue(event) })}
          />
        </label>
      </div>

      <div className="editor-group">
        <p className="field-label">Schedule</p>
        <label className="field-label">
          <select
            className="text-input"
            value={value.schedule.state}
            onChange={(event) => {
              if (event.target.value === "scheduled") {
                update({
                  schedule: scheduledSchedule
                    ? scheduledSchedule
                    : {
                        state: "scheduled",
                        start_offset_ms: 0,
                        end_offset_ms: 3_600_000,
                      },
                });
              } else {
                update({ schedule: { state: "unscheduled" } });
              }
            }}
          >
            <option value="unscheduled">Unscheduled</option>
            <option value="scheduled">Scheduled</option>
          </select>
        </label>
        {scheduledSchedule ? (
          <div className="form-grid">
            <label className="field-label">
              Start offset (ms)
              <input
                className="text-input"
                type="number"
                min={0}
                max={86_399_999}
                value={scheduledSchedule.start_offset_ms}
                onChange={(event) =>
                  update({
                    schedule: {
                      ...scheduledSchedule,
                      start_offset_ms: numberValue(event),
                    },
                  })
                }
              />
            </label>
            <label className="field-label">
              End offset (ms)
              <input
                className="text-input"
                type="number"
                min={1}
                max={86_400_000}
                value={scheduledSchedule.end_offset_ms}
                onChange={(event) =>
                  update({
                    schedule: {
                      ...scheduledSchedule,
                      end_offset_ms: numberValue(event),
                    },
                  })
                }
              />
            </label>
          </div>
        ) : null}
      </div>

      <div className="editor-group">
        <p className="field-label">Availability window (offsets in ms)</p>
        <div className="form-grid">
          <label className="field-label">
            Opens
            <input
              className="text-input"
              type="number"
              min={0}
              max={86_399_999}
              value={value.timing.open_windows[0]?.opens_offset_ms ?? 0}
              onChange={(event) =>
                updateWindow({ opens_offset_ms: numberValue(event) })
              }
            />
          </label>
          <label className="field-label">
            Closes
            <input
              className="text-input"
              type="number"
              min={1}
              max={86_400_000}
              value={
                value.timing.open_windows[0]?.closes_offset_ms ?? 86_400_000
              }
              onChange={(event) =>
                updateWindow({ closes_offset_ms: numberValue(event) })
              }
            />
          </label>
        </div>
      </div>

      <div className="editor-group">
        <p className="field-label">Duration</p>
        <div className="form-grid">
          {(
            [
              ["Minimum", "min_duration_seconds"],
              ["Preferred", "preferred_duration_seconds"],
              ["Maximum", "max_duration_seconds"],
            ] as const
          ).map(([label, key]) => (
            <label className="field-label" key={key}>
              {label} seconds
              <input
                className="text-input"
                type="number"
                min={0}
                max={86_400}
                value={value.timing[key]}
                onChange={(event) =>
                  updateTiming({ [key]: numberValue(event) })
                }
              />
            </label>
          ))}
        </div>
        <p className="configuration-note">
          Current duration:{" "}
          {formatDuration(value.timing.preferred_duration_seconds)}
        </p>
      </div>

      <div className="editor-group">
        <p className="field-label">Reservations and deadlines</p>
        <label className="check-field">
          <input
            type="checkbox"
            checked={value.timing.reservation_start_offset_ms !== undefined}
            onChange={(event) => setReservation(event.target.checked)}
          />
          Has a reservation
        </label>
        {value.timing.reservation_start_offset_ms !== undefined ? (
          <div className="form-grid">
            <label className="field-label">
              Reservation start offset (ms)
              <input
                className="text-input"
                type="number"
                min={0}
                max={86_400_000}
                value={value.timing.reservation_start_offset_ms}
                onChange={(event) =>
                  updateTiming({
                    reservation_start_offset_ms: numberValue(event),
                  })
                }
              />
            </label>
          </div>
        ) : null}
        <label className="field-label">
          Grace period (seconds)
          <input
            className="text-input"
            type="number"
            min={0}
            max={4_294_967_295}
            value={value.timing.reservation_grace_seconds}
            onChange={(event) =>
              updateTiming({
                reservation_grace_seconds: numberValue(event),
              })
            }
          />
        </label>
        <label className="check-field">
          <input
            type="checkbox"
            checked={value.timing.mandatory_deadline_offset_ms !== undefined}
            onChange={(event) => setDeadline(event.target.checked)}
          />
          Has a deadline
        </label>
        {value.timing.mandatory_deadline_offset_ms !== undefined ? (
          <label className="field-label">
            Deadline offset (ms)
            <input
              className="text-input"
              type="number"
              min={0}
              max={86_400_000}
              value={value.timing.mandatory_deadline_offset_ms}
              onChange={(event) =>
                updateTiming({
                  mandatory_deadline_offset_ms: numberValue(event),
                })
              }
            />
          </label>
        ) : null}
      </div>

      <div className="form-grid">
        <label className="check-field">
          <input
            type="checkbox"
            checked={value.timing.mandatory}
            onChange={(event) =>
              updateTiming({ mandatory: event.target.checked })
            }
          />
          Mandatory
        </label>
        <label className="check-field">
          <input
            type="checkbox"
            checked={value.timing.can_move}
            onChange={(event) =>
              updateTiming({ can_move: event.target.checked })
            }
          />
          Can move
        </label>
        <label className="check-field">
          <input
            type="checkbox"
            checked={value.timing.can_skip}
            onChange={(event) =>
              updateTiming({ can_skip: event.target.checked })
            }
          />
          Can skip
        </label>
        <label className="check-field">
          <input type="checkbox" checked={false} disabled readOnly />
          Can shorten (disabled by contract)
        </label>
      </div>
    </section>
  );
}
