import { JobStatus } from "../api/gen/mimir/v1/training_pb";
import type { VisitPlan } from "../api/gen/mimir/v1/visit_pb";

// How often a plan whose customers come from a database is made again.
export const planRepeats = [
  { value: "0", label: "Just once" },
  { value: "86400", label: "Every day" },
  { value: "604800", label: "Every week" },
];

export const weekdays = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];

export const planWaiting = (p: VisitPlan) => p.status === JobStatus.QUEUED || p.status === JobStatus.RUNNING;

// planProgress is how far a plan is, from 0 to 100.
export function planProgress(p: VisitPlan): number {
  if (p.status === JobStatus.SUCCEEDED) return 100;
  const total = Number(p.total);
  return total > 0 ? Math.min(100, (Number(p.done) / total) * 100) : 0;
}

// planStatus says where a plan is, in words.
export function planStatus(p: VisitPlan): string {
  switch (p.status) {
    case JobStatus.QUEUED: return "Waiting";
    case JobStatus.RUNNING: return Number(p.total) > 0 ? `${p.stage || "Planning"}: ${Number(p.done).toLocaleString()} of ${Number(p.total).toLocaleString()}` : (p.stage || "Reading the customers");
    case JobStatus.SUCCEEDED: return `${Number(p.visits).toLocaleString()} visits for ${Number(p.salespeople).toLocaleString()} salespeople`;
    case JobStatus.FAILED: return "Failed";
    case JobStatus.CANCELLED: return "Stopped";
  }
  return "";
}

// planDays says which days a plan covers: "Mon 9 Nov to Sun 15 Nov".
export function planDays(p: { startDate: string; endDate: string }): string {
  if (!p.startDate) return "";
  const show = (d: string) => new Date(d + "T00:00:00").toLocaleDateString(undefined, { weekday: "short", day: "numeric", month: "short" });
  return p.startDate === p.endDate ? show(p.startDate) : `${show(p.startDate)} to ${show(p.endDate)}`;
}

// clock shows minutes after midnight as 09:30.
export function clock(minute: number): string {
  const h = Math.floor(minute / 60), m = minute % 60;
  return `${String(h).padStart(2, "0")}:${String(m).padStart(2, "0")}`;
}

// visitsURL is where a plan's visits download from, as a table.
export const visitsURL = (id: string) => `/api/visit-plans/${encodeURIComponent(id)}/visits.csv`;

// fitPoints scales places into a w×h box with a margin, keeping their shape.
export function fitPoints(points: { lat: number; lon: number }[], w: number, h: number, margin = 16): (p: { lat: number; lon: number }) => [number, number] {
  if (points.length === 0) return () => [w / 2, h / 2];
  const lats = points.map((p) => p.lat), lons = points.map((p) => p.lon);
  const minLat = Math.min(...lats), maxLat = Math.max(...lats), minLon = Math.min(...lons), maxLon = Math.max(...lons);
  // A degree of longitude is shorter away from the equator.
  const squash = Math.cos(((minLat + maxLat) / 2) * (Math.PI / 180));
  const spanX = (maxLon - minLon) * squash, spanY = maxLat - minLat;
  if (spanX < 1e-9 && spanY < 1e-9) return () => [w / 2, h / 2];
  const scale = Math.min((w - 2 * margin) / Math.max(spanX, 1e-9), (h - 2 * margin) / Math.max(spanY, 1e-9));
  const offX = (w - spanX * scale) / 2, offY = (h - spanY * scale) / 2;
  return (p) => [offX + (p.lon - minLon) * squash * scale, h - (offY + (p.lat - minLat) * scale)];
}
