import { z } from "zod";
import { serviceStateSchema } from "./cloud";

// One UTC day of a service's availability strip: none means no probe ran that day; down means some probed minutes were unavailable; degraded means slow or failing minutes without an outage.
export const statusDaySchema = z.object({ day: z.string(), state: z.enum(["ok", "degraded", "down", "none"]), uptime: z.number().nullable() });

export const statusServiceSchema = z.object({
  key: z.string(),
  name: z.string(),
  desc: z.string(),
  state: serviceStateSchema,
  p95_ms: z.number().nullable(),
  uptime_60d: z.number().nullable(),
  days: z.array(statusDaySchema),
});
export type StatusService = z.infer<typeof statusServiceSchema>;

export const incidentSchema = z.object({
  id: z.number(),
  service: z.string(),
  service_name: z.string(),
  title: z.string(),
  description: z.string(),
  state: z.enum(["open", "resolved"]),
  started_at: z.string(),
  resolved_at: z.string().nullable(),
  auto: z.boolean(),
});
export type Incident = z.infer<typeof incidentSchema>;

// GET /api/status: the latest minute probe (database plus every monitored upstream), 60 days of availability and the recent incidents, open ones first.
export const statusSchema = z.object({
  checked_at: z.string().nullable(),
  state: z.enum(["ok", "degraded", "down", "unknown"]),
  services: z.array(statusServiceSchema),
  incidents: z.array(incidentSchema),
});
export type Status = z.infer<typeof statusSchema>;
