import { z } from "zod";

const count = z.number().int().nonnegative();

export const serviceStateSchema = z.enum(["ok", "degraded", "down", "unknown"]);
export type ServiceState = z.infer<typeof serviceStateSchema>;

export const overviewServiceSchema = z.object({
  key: z.string(),
  name: z.string(),
  provider: z.string(),
  state: serviceStateSchema,
  // uptime_60d is a percentage (0–100); null until the status probe has recorded a day.
  uptime_60d: z.number().nullable(),
  p95_ms: z.number().int().nullable(),
});
export type OverviewService = z.infer<typeof overviewServiceSchema>;

export const activeDaySchema = z.object({ day: z.string(), windows: count, mac_linux: count, mobile: count });
export type ActiveDay = z.infer<typeof activeDaySchema>;

// GET /api/overview?days=7|30. Totals, the daily series, the telemetry-backed device metrics (zero or null until clients report active/session events; telemetry says whether they do), database-side pending work and the monitored services.
export const overviewSchema = z.object({
  users: count,
  new_users_30d: count,
  downloads: count,
  downloads_30d: count,
  downloads_prev_30d: count,
  crashes: count,
  open_crashes: count,
  range_days: z.number().int(),
  telemetry: z.object({ active: z.boolean(), sessions: z.boolean() }),
  active_devices_daily: z.array(activeDaySchema).nullable().transform(rows => rows ?? []),
  active_devices_7d: count,
  active_devices_prev_7d: count,
  platform_active_7d: z.record(z.string(), count),
  // crash_free_rate is a ratio (0–1) over the last 7 days; null without session telemetry.
  crash_free_rate: z.number().nullable(),
  crash_free_rate_prev: z.number().nullable(),
  pending: z.object({ community: count, reports_7d: count, crash_groups: count }),
  services_configured: z.boolean(),
  services: z.array(overviewServiceSchema),
});
export type Overview = z.infer<typeof overviewSchema>;
