import type { Timestamp } from "@bufbuild/protobuf/wkt";
import { timestampDate } from "@bufbuild/protobuf/wkt";

export function bytes(n: number | bigint): string {
  let v = Number(n);
  for (const unit of ["bytes", "KB", "MB", "GB"]) {
    if (v < 1000 || unit === "GB") return `${unit === "bytes" ? v : v.toFixed(1)} ${unit}`;
    v /= 1000;
  }
  return `${v} GB`;
}

const rtf = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });

export function ago(ts: Timestamp | undefined): string {
  if (!ts) return "";
  const s = (timestampDate(ts).getTime() - Date.now()) / 1000;
  const steps: [Intl.RelativeTimeFormatUnit, number][] = [
    ["second", 60], ["minute", 60], ["hour", 24], ["day", 30], ["month", 12], ["year", Infinity],
  ];
  let v = s;
  for (const [unit, next] of steps) {
    if (Math.abs(v) < next) return rtf.format(Math.round(v), unit);
    v /= next;
  }
  return "";
}

export function percent(p: number): string {
  return `${Math.round(p * 100)}%`;
}

// duration says how long, in the largest unit that fits: "3 days".
export function duration(seconds: number | bigint): string {
  const s = Number(seconds);
  const units: [string, number][] = [["day", 86400], ["hour", 3600], ["minute", 60]];
  for (const [unit, size] of units) {
    if (s >= size) {
      const n = Math.floor(s / size);
      return `${n} ${unit}${n === 1 ? "" : "s"}`;
    }
  }
  return "a moment";
}

// share is used of total as a percentage, 0 when total is unknown.
export function share(used: number | bigint, total: number | bigint): number {
  const t = Number(total);
  return t > 0 ? (Number(used) / t) * 100 : 0;
}

// load picks a color for how full something is.
export function loadColor(pct: number): string {
  return pct >= 90 ? "red" : pct >= 75 ? "yellow" : "teal";
}
