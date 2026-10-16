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
