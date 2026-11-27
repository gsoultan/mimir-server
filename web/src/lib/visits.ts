import { JobStatus } from "../api/gen/mimir/v1/training_pb";
import type { VisitPlan } from "../api/gen/mimir/v1/visit_pb";
import { locale, t } from "../i18n";

// How often a plan whose customers come from a database is made again.
export const planRepeats = [
  { value: "0", label: t("Just once") },
  { value: "schedule", label: t("At a time I choose") },
  { value: "86400", label: t("Every day, from now") },
  { value: "604800", label: t("Every week, from now") },
];

// Repeat is how a plan is made again: a planRepeats value, and for
// "schedule" the time of day (minutes after midnight) and weekdays
// (none is every day).
export type Repeat = { mode: string; atMinute: number; weekdays: number[] };

export const once: Repeat = { mode: "0", atMinute: 6 * 60, weekdays: [1, 2, 3, 4, 5, 6] };

// repeatOf reads how a plan is made again.
export function repeatOf(p: VisitPlan): Repeat {
  if (p.schedule && p.repeatSeconds > 0n) return { mode: "schedule", atMinute: p.schedule.atMinute, weekdays: [...p.schedule.weekdays] };
  return { ...once, mode: p.repeatSeconds.toString() };
}

// repeatRequest is a Repeat as the API takes it.
export function repeatRequest(r: Repeat): { repeatSeconds: bigint; schedule?: { atMinute: number; weekdays: number[] } } {
  if (r.mode === "schedule") return { repeatSeconds: 0n, schedule: { atMinute: r.atMinute, weekdays: r.weekdays } };
  return { repeatSeconds: BigInt(r.mode) };
}

// repeatText says when a plan is made again: "06:00 on Mon, Tue".
export function repeatText(p: VisitPlan): string {
  if (p.repeatSeconds <= 0n) return "";
  if (p.schedule) {
    const days = p.schedule.weekdays.length === 0 || p.schedule.weekdays.length === 7 ? t("every day") : t("on {days}", { days: p.schedule.weekdays.map((d) => weekdays[d]).join(", ") });
    return `${clock(p.schedule.atMinute)} ${days}`;
  }
  return planRepeats.find((r) => r.value === p.repeatSeconds.toString())?.label ?? t("On a schedule");
}

export const weekdays = [t("Sun"), t("Mon"), t("Tue"), t("Wed"), t("Thu"), t("Fri"), t("Sat")];

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
    case JobStatus.QUEUED: return t("Waiting");
    case JobStatus.RUNNING: return Number(p.total) > 0 ? t("{stage}: {n} of {total}", { stage: p.stage || t("Planning"), n: Number(p.done).toLocaleString(), total: Number(p.total).toLocaleString() }) : (p.stage || t("Reading the customers"));
    case JobStatus.SUCCEEDED: return t("{n1} visits for {n2} salespeople", { n1: Number(p.visits).toLocaleString(), n2: Number(p.salespeople).toLocaleString() });
    case JobStatus.FAILED: return t("Failed");
    case JobStatus.CANCELLED: return t("Stopped");
  }
  return "";
}

// planDays says which days a plan covers: "Mon 9 Nov to Sun 15 Nov".
export function planDays(p: { startDate: string; endDate: string }): string {
  if (!p.startDate) return "";
  const show = (d: string) => new Date(d + "T00:00:00").toLocaleDateString(locale, { weekday: "short", day: "numeric", month: "short" });
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
