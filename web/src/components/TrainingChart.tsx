import { Text } from "@mantine/core";

import type { EpochResult } from "../api/gen/mimir/v1/training_pb";

// A small line chart of accuracy per pass, drawn as SVG so it needs no
// library and resizes with its box.
export function TrainingChart({ history, epochs }: { history: EpochResult[]; epochs: number }) {
  const w = 600, h = 180, pl = 36, pr = 12, pt = 12, pb = 26;
  const n = Math.max(epochs, history.length, 1);
  const x = (e: number) => pl + ((e - 1) / Math.max(n - 1, 1)) * (w - pl - pr);
  const y = (v: number) => pt + (1 - v) * (h - pt - pb);
  const acc = history.filter((e) => e.accuracy > 0);
  const line = acc.map((e, i) => `${i === 0 ? "M" : "L"}${x(e.epoch).toFixed(1)},${y(e.accuracy).toFixed(1)}`).join(" ");
  const maxLoss = Math.max(...history.map((e) => e.loss), 1e-9);
  const lossLine = history.map((e, i) => `${i === 0 ? "M" : "L"}${x(e.epoch).toFixed(1)},${y(e.loss / maxLoss).toFixed(1)}`).join(" ");
  if (history.length === 0) return <Text c="dimmed" size="sm">The chart fills in after the first pass.</Text>;
  return (
    <svg viewBox={`0 0 ${w} ${h}`} className="w-full" role="img" aria-label="Accuracy and loss per pass">
      {[0, 0.25, 0.5, 0.75, 1].map((v) => (
        <g key={v}>
          <line x1={pl} x2={w - pr} y1={y(v)} y2={y(v)} stroke="var(--mantine-color-gray-3)" strokeDasharray={v === 0 || v === 1 ? undefined : "3 4"} />
          <text x={pl - 6} y={y(v) + 4} fontSize="11" textAnchor="end" fill="var(--mantine-color-dimmed)">{Math.round(v * 100)}%</text>
        </g>
      ))}
      {Array.from({ length: n }, (_, i) => i + 1).filter((e) => n <= 12 || e === 1 || e === n || e % Math.ceil(n / 10) === 0).map((e) => (
        <text key={e} x={x(e)} y={h - 8} fontSize="11" textAnchor="middle" fill="var(--mantine-color-dimmed)">{e}</text>
      ))}
      <path d={lossLine} fill="none" stroke="var(--mantine-color-gray-5)" strokeWidth="1.5" strokeDasharray="4 3" />
      {acc.length > 0 && <path d={line} fill="none" stroke="var(--mantine-color-teal-7)" strokeWidth="2.5" strokeLinejoin="round" />}
      {acc.map((e) => <circle key={e.epoch} cx={x(e.epoch)} cy={y(e.accuracy)} r="3.5" fill="var(--mantine-color-teal-7)" />)}
      <g fontSize="11" fill="var(--mantine-color-dimmed)">
        <line x1={w - pr - 150} x2={w - pr - 130} y1={pt + 4} y2={pt + 4} stroke="var(--mantine-color-teal-7)" strokeWidth="2.5" />
        <text x={w - pr - 124} y={pt + 8}>Right answers</text>
        <line x1={w - pr - 54} x2={w - pr - 34} y1={pt + 4} y2={pt + 4} stroke="var(--mantine-color-gray-5)" strokeWidth="1.5" strokeDasharray="4 3" />
        <text x={w - pr - 28} y={pt + 8}>Loss</text>
      </g>
    </svg>
  );
}
