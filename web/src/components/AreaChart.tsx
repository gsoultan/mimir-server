import { Text } from "@mantine/core";
import { useId, useRef, useState } from "react";

export type Point = { t: number; v: number };

// AreaChart draws one series over time as SVG, with a hover readout. max
// fixes the top of the scale (100 for percentages); otherwise it fits the
// data.
export function AreaChart({ points, max, color, format, span, label }: {
  points: Point[]; max?: number; color: string; format: (v: number) => string; span: number; label: string;
}) {
  const id = useId().replace(/:/g, "");
  const ref = useRef<SVGSVGElement>(null);
  const [hover, setHover] = useState<Point>();
  const w = 520, h = 150, pl = 44, pr = 8, pt = 10, pb = 22;
  const now = Date.now();
  const from = now - span;
  const top = max ?? niceMax(Math.max(...points.map((p) => p.v), 0));
  const x = (t: number) => pl + ((t - from) / span) * (w - pl - pr);
  const y = (v: number) => pt + (1 - Math.min(v, top) / (top || 1)) * (h - pt - pb);
  if (points.length === 0) {
    return <div className="flex h-[150px] items-center justify-center rounded-md bg-[var(--mantine-color-default-hover)]"><Text size="sm" c="dimmed">No readings for this time yet.</Text></div>;
  }
  // A gap of more than a few readings means it was offline; break the line.
  const gap = Math.max(span / 120, 3 * 60_000);
  const runs: Point[][] = [];
  for (const p of points) {
    const run = runs.at(-1);
    if (run && p.t - run.at(-1)!.t <= gap) run.push(p);
    else runs.push([p]);
  }
  const line = (r: Point[]) => r.map((p, i) => `${i ? "L" : "M"}${x(p.t).toFixed(1)},${y(p.v).toFixed(1)}`).join("");
  const area = (r: Point[]) => `${line(r)}L${x(r.at(-1)!.t).toFixed(1)},${y(0)}L${x(r[0]!.t).toFixed(1)},${y(0)}Z`;
  // Labels too close to an edge would be cut off.
  const ticks = timeTicks(from, now, span).filter((t) => x(t.t) >= pl + 16 && x(t.t) <= w - pr - 18);

  function move(e: React.PointerEvent) {
    const box = ref.current?.getBoundingClientRect();
    if (!box) return;
    const t = from + ((((e.clientX - box.left) / box.width) * w - pl) / (w - pl - pr)) * span;
    let best = points[0]!;
    for (const p of points) if (Math.abs(p.t - t) < Math.abs(best.t - t)) best = p;
    setHover(best);
  }

  return (
    <svg ref={ref} viewBox={`0 0 ${w} ${h}`} className="w-full touch-none select-none" role="img" aria-label={label} onPointerMove={move} onPointerLeave={() => setHover(undefined)}>
      <defs>
        <linearGradient id={`g${id}`} x1="0" x2="0" y1="0" y2="1">
          <stop offset="0%" stopColor={color} stopOpacity="0.35" />
          <stop offset="100%" stopColor={color} stopOpacity="0.02" />
        </linearGradient>
      </defs>
      {[0, 0.5, 1].map((f) => (
        <g key={f}>
          <line x1={pl} x2={w - pr} y1={y(top * f)} y2={y(top * f)} stroke="var(--mantine-color-default-border)" strokeDasharray={f === 0 ? undefined : "3 4"} />
          <text x={pl - 6} y={y(top * f) + 4} fontSize="10" textAnchor="end" fill="var(--mantine-color-dimmed)">{format(top * f)}</text>
        </g>
      ))}
      {ticks.map((t) => <text key={t.t} x={x(t.t)} y={h - 6} fontSize="10" textAnchor="middle" fill="var(--mantine-color-dimmed)">{t.label}</text>)}
      {runs.map((r, i) => (
        <g key={i}>
          <path d={area(r)} fill={`url(#g${id})`} />
          <path d={line(r)} fill="none" stroke={color} strokeWidth="2" strokeLinejoin="round" strokeLinecap="round" />
        </g>
      ))}
      {hover && (
        <g>
          <line x1={x(hover.t)} x2={x(hover.t)} y1={pt} y2={y(0)} stroke="var(--mantine-color-dimmed)" strokeDasharray="2 3" />
          <circle cx={x(hover.t)} cy={y(hover.v)} r="4" fill={color} stroke="var(--mantine-color-body)" strokeWidth="2" />
          <g transform={`translate(${Math.min(Math.max(x(hover.t) - 60, pl), w - pr - 120)},${pt})`}>
            <rect width="120" height="34" rx="6" fill="var(--mantine-color-body)" stroke="var(--mantine-color-default-border)" />
            <text x="60" y="14" fontSize="11" fontWeight="700" textAnchor="middle" fill="var(--mantine-color-text)">{format(hover.v)}</text>
            <text x="60" y="27" fontSize="10" textAnchor="middle" fill="var(--mantine-color-dimmed)">{clock(hover.t, span)}</text>
          </g>
        </g>
      )}
    </svg>
  );
}

function niceMax(v: number) {
  if (v <= 0) return 1;
  const p = 10 ** Math.floor(Math.log10(v));
  for (const m of [1, 2, 2.5, 5, 10]) if (v <= m * p) return m * p;
  return 10 * p;
}

function clock(t: number, span: number) {
  const d = new Date(t);
  return span > 36 * 3600_000
    ? d.toLocaleString(undefined, { weekday: "short", hour: "numeric", minute: "2-digit" })
    : d.toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" });
}

function timeTicks(from: number, to: number, span: number) {
  const step = span <= 3600_000 ? 15 * 60_000 : span <= 86400_000 ? 6 * 3600_000 : 86400_000;
  const out: { t: number; label: string }[] = [];
  const offset = new Date().getTimezoneOffset() * 60_000;
  for (let t = Math.ceil((from - offset) / step) * step + offset; t <= to; t += step) {
    const d = new Date(t);
    out.push({ t, label: step >= 86400_000 ? d.toLocaleDateString(undefined, { weekday: "short" }) : d.toLocaleTimeString(undefined, { hour: "numeric", minute: step < 3600_000 ? "2-digit" : undefined }) });
  }
  return out;
}
