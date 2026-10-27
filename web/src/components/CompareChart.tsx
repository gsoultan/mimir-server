import { Group, Text } from "@mantine/core";
import { useElementSize } from "@mantine/hooks";
import { useState } from "react";

import type { Job } from "../api/gen/mimir/v1/training_pb";
import { runColor } from "../lib/compare";

// CompareChart draws each run's accuracy per pass on one axis, one color
// per run, with every run's score at the pass under the pointer.
export function CompareChart({ jobs, number }: { jobs: Job[]; number?: boolean }) {
  const [hover, setHover] = useState<number>();
  // Drawn at the box's own width, so labels stay readable on a phone.
  const { ref, width } = useElementSize();
  const w = Math.max(300, Math.round(width) || 640), h = 220, pl = 40, pr = 16, pt = 12, pb = 28;
  const n = Math.max(1, ...jobs.map((j) => Math.max(j.settings?.epochs ?? 0, j.history.length)));
  const x = (e: number) => pl + ((e - 1) / Math.max(n - 1, 1)) * (w - pl - pr);
  const scored = jobs.map((j) => j.history.filter((e) => e.accuracy > 0));
  // Runs often land close together, so the axis starts just below the
  // lowest score, on a 5% step, instead of at 0.
  const low = Math.min(1, ...scored.flat().map((e) => e.accuracy));
  const lo = Math.max(0, Math.floor((low - 0.02) * 20) / 20);
  const y = (v: number) => pt + ((1 - v) / (1 - lo)) * (h - pt - pb);
  const ticks = [0, 0.25, 0.5, 0.75, 1].map((t) => lo + t * (1 - lo));
  if (scored.every((s) => s.length === 0)) return <Text c="dimmed" size="sm">The chart fills in once a run has finished a pass.</Text>;

  function move(ev: React.PointerEvent<SVGSVGElement>) {
    const box = ev.currentTarget.getBoundingClientRect();
    const px = ((ev.clientX - box.left) / box.width) * w;
    const e = Math.round(((px - pl) / (w - pl - pr)) * Math.max(n - 1, 1)) + 1;
    setHover(Math.min(n, Math.max(1, e)));
  }
  const at = hover === undefined ? [] : jobs.map((j, i) => ({ j, i, e: j.history.find((e) => e.epoch === hover) })).filter((r) => r.e && r.e.accuracy > 0);
  const tipLeft = hover !== undefined && x(hover) > w * 0.6;

  return (
    <div>
      <div className="relative" ref={ref}>
        <svg viewBox={`0 0 ${w} ${h}`} className="w-full touch-none" role="img" aria-label={`${number ? "Close" : "Right"} answers per pass, one line per run`} onPointerMove={move} onPointerLeave={() => setHover(undefined)}>
          {ticks.map((v, k) => (
            <g key={k}>
              <line x1={pl} x2={w - pr} y1={y(v)} y2={y(v)} stroke="var(--mantine-color-default-border)" strokeDasharray={k === 0 || k === ticks.length - 1 ? undefined : "3 4"} />
              <text x={pl - 6} y={y(v) + 4} fontSize="11" textAnchor="end" fill="var(--mantine-color-dimmed)">{+(v * 100).toFixed(lo > 0.5 ? 1 : 0)}%</text>
            </g>
          ))}
          {Array.from({ length: n }, (_, i) => i + 1).filter((e) => n <= 12 || e === 1 || e === n || e % Math.ceil(n / 10) === 0).map((e) => (
            <text key={e} x={x(e)} y={h - 8} fontSize="11" textAnchor="middle" fill="var(--mantine-color-dimmed)">{e}</text>
          ))}
          {hover !== undefined && <line x1={x(hover)} x2={x(hover)} y1={pt} y2={h - pb} stroke="var(--mantine-color-dimmed)" strokeWidth="1" />}
          {scored.map((s, i) => s.length > 0 && (
            <g key={jobs[i]!.id}>
              <path d={s.map((e, k) => `${k === 0 ? "M" : "L"}${x(e.epoch).toFixed(1)},${y(e.accuracy).toFixed(1)}`).join(" ")} fill="none" stroke={runColor(i)} strokeWidth="2" strokeLinejoin="round" />
              {(s.length === 1 || hover !== undefined) && s.filter((e) => s.length === 1 || e.epoch === hover).map((e) => (
                <circle key={e.epoch} cx={x(e.epoch)} cy={y(e.accuracy)} r="4" fill={runColor(i)} stroke="var(--mantine-color-body)" strokeWidth="2" />
              ))}
            </g>
          ))}
        </svg>
        {hover !== undefined && at.length > 0 && (
          <div
            className="pointer-events-none absolute top-2 rounded-md border border-[var(--mantine-color-default-border)] bg-[var(--mantine-color-body)] px-3 py-2 shadow-sm"
            style={tipLeft ? { right: `${100 - (x(hover) / w) * 100 + 2}%` } : { left: `${(x(hover) / w) * 100 + 2}%` }}
          >
            <Text size="xs" fw={600} mb={2}>Pass {hover}</Text>
            {at.map(({ j, i, e }) => (
              <Group key={j.id} gap={6} wrap="nowrap">
                <span className="inline-block h-2 w-2 shrink-0 rounded-full" style={{ background: runColor(i) }} />
                <Text size="xs" truncate maw={160}>{j.name}</Text>
                <Text size="xs" fw={600} ml="auto" pl="xs">{(e!.accuracy * 100).toFixed(1)}%</Text>
              </Group>
            ))}
          </div>
        )}
      </div>
      <Group gap="md" mt={6} justify="center">
        {jobs.map((j, i) => (
          <Group key={j.id} gap={6} wrap="nowrap">
            <span className="inline-block h-[3px] w-5 rounded" style={{ background: runColor(i) }} />
            <Text size="xs" c="dimmed">{j.name}</Text>
          </Group>
        ))}
      </Group>
      <Text size="xs" c="dimmed" ta="center" mt={2}>{number ? "Close answers" : "Right answers"} on examples kept aside, after each pass.</Text>
    </div>
  );
}
