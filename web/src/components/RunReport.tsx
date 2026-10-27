import { Group, SimpleGrid, Stack, Table, Text, Title } from "@mantine/core";
import { useState } from "react";

import type { RunReport as Report } from "../api/gen/mimir/v1/training_pb";
import { bytes } from "../lib/format";
import { balanced, number, short } from "../lib/report";

// RunReport shows how a finished run's model did on the examples kept
// aside: one plain headline, the expert numbers, and one picture for its
// kind of answer.
export function RunReport({ report: r }: { report: Report }) {
  const on = r.onTraining ? "the examples it learned from, since none were kept aside" : `${r.examples.toLocaleString()} examples it never saw while learning`;
  return (
    <Stack gap="md">
      <Text size="sm" c="dimmed">Scored on {on}.</Text>
      {r.kind === "number" && <Numbers r={r} />}
      {(r.kind === "class" || r.kind === "ranked") && <Labels r={r} />}
      <SimpleGrid cols={{ base: 2, sm: 3 }} spacing="xs">
        <Stat label="Answer time" value={`${number(r.millisPerExample, 2)} ms`} hint="per example, while scoring" />
        <Stat label="Model size" value={bytes(r.sizeBytes)} />
        <Stat label="Examples scored" value={r.examples.toLocaleString()} />
      </SimpleGrid>
    </Stack>
  );
}

function Stat({ label, value, hint }: { label: string; value: string; hint?: string }) {
  return (
    <div>
      <Text size="xs" c="dimmed">{label}</Text>
      <Text fw={600}>{value}</Text>
      {hint && <Text size="xs" c="dimmed">{hint}</Text>}
    </div>
  );
}

function Numbers({ r }: { r: Report }) {
  return (
    <>
      <SimpleGrid cols={{ base: 2, sm: 4 }} spacing="xs">
        <Stat label="Usually off by (MAE)" value={number(r.mae)} />
        <Stat label="Big misses count more (RMSE)" value={number(r.rmse)} />
        <Stat label="Explains the answers (R²)" value={number(r.r2, 3)} hint="1 is perfect, 0 is no better than the average" />
        <Stat label="Within 10%" value={`${(r.within10 * 100).toFixed(1)}%`} />
      </SimpleGrid>
      {r.points.length > 1 && <Scatter r={r} />}
    </>
  );
}

// Scatter puts each guess against the real answer; on the diagonal is
// exactly right.
function Scatter({ r }: { r: Report }) {
  const [hover, setHover] = useState<number>();
  const w = 360, h = 300, pl = 44, pr = 24, pt = 24, pb = 36;
  const all = r.points.flatMap((p) => [p.real, p.guess]);
  let lo = Math.min(...all), hi = Math.max(...all);
  if (hi === lo) { lo -= 1; hi += 1; }
  const pad = (hi - lo) * 0.05;
  lo -= pad; hi += pad;
  const x = (v: number) => pl + ((v - lo) / (hi - lo)) * (w - pl - pr);
  const y = (v: number) => pt + (1 - (v - lo) / (hi - lo)) * (h - pt - pb);
  const ticks = [0, 0.25, 0.5, 0.75, 1].map((t) => lo + t * (hi - lo));
  const p = hover === undefined ? undefined : r.points[hover];
  return (
    <div className="mx-auto w-full max-w-md">
      <svg viewBox={`0 0 ${w} ${h}`} className="w-full" role="img" aria-label="Guesses against real answers">
        {ticks.map((t, i) => (
          <g key={i}>
            <line x1={pl} x2={w - pr} y1={y(t)} y2={y(t)} stroke="var(--mantine-color-default-border)" strokeDasharray={i === 0 ? undefined : "3 4"} />
            <text x={pl - 6} y={y(t) + 4} fontSize="11" textAnchor="end" fill="var(--mantine-color-dimmed)">{short(t)}</text>
            <text x={x(t)} y={h - pb + 16} fontSize="11" textAnchor="middle" fill="var(--mantine-color-dimmed)">{short(t)}</text>
          </g>
        ))}
        <line x1={x(lo)} y1={y(lo)} x2={x(hi)} y2={y(hi)} stroke="var(--mantine-color-dimmed)" strokeWidth="1" strokeDasharray="5 4" />
        {r.points.map((q, i) => (
          <circle key={i} cx={x(q.real)} cy={y(q.guess)} r={hover === i ? 6 : 4} fill="var(--run-1)" fillOpacity={hover === i ? 1 : 0.6} stroke="var(--mantine-color-body)" strokeWidth="1"
            onPointerEnter={() => setHover(i)} onPointerLeave={() => setHover(undefined)} />
        ))}
        <text x={(pl + w - pr) / 2} y={h - 4} fontSize="11" textAnchor="middle" fill="var(--mantine-color-dimmed)">Real answer</text>
        <text x={pl} y={12} fontSize="11" fill="var(--mantine-color-dimmed)">Its guess</text>
      </svg>
      <Text size="xs" c="dimmed" ta="center">
        {p ? `Real ${number(p.real)}, guessed ${number(p.guess)}: off by ${number(Math.abs(p.guess - p.real))}.` : "Each dot is one example. On the dashed line is exactly right."}
      </Text>
    </div>
  );
}

function Labels({ r }: { r: Report }) {
  const uneven = balanced(r);
  return (
    <>
      <SimpleGrid cols={{ base: 2, sm: 4 }} spacing="xs">
        <Stat label="Right answers" value={`${(r.accuracy * 100).toFixed(1)}%`} />
        <Stat label="Balanced score (macro F1)" value={number(r.macroF1, 3)} hint={uneven ? "the fairer score here: one answer is rare" : "every answer counts the same"} />
        <Stat label="Sureness (log loss)" value={number(r.logLoss, 3)} hint="lower is better" />
        {r.kind === "ranked" && <Stat label={`Right one in top ${r.k}`} value={`${((r.hitAt.at(-1) ?? 0) * 100).toFixed(1)}%`} />}
      </SimpleGrid>
      {r.kind === "ranked" && r.hitAt.length > 0 && <Ranked r={r} />}
      {r.confusion.length > 0 && <Confusion r={r} />}
      {r.classes.length > 0 && (
        <Table.ScrollContainer minWidth={420}>
          <Table verticalSpacing={4} fz="sm">
            <Table.Thead><Table.Tr><Table.Th>Answer</Table.Th><Table.Th>When it says this, it's right (precision)</Table.Th><Table.Th>Finds it when it's there (recall)</Table.Th><Table.Th>F1</Table.Th><Table.Th>Examples</Table.Th></Table.Tr></Table.Thead>
            <Table.Tbody>
              {r.classes.map((c) => (
                <Table.Tr key={c.label}>
                  <Table.Td>{c.label}</Table.Td>
                  <Table.Td>{(c.precision * 100).toFixed(1)}%</Table.Td>
                  <Table.Td>{(c.recall * 100).toFixed(1)}%</Table.Td>
                  <Table.Td>{number(c.f1, 3)}</Table.Td>
                  <Table.Td>{c.support.toLocaleString()}</Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>
      )}
    </>
  );
}

function Ranked({ r }: { r: Report }) {
  return (
    <Group gap="lg" wrap="wrap">
      <Text size="sm">Right one in the first:</Text>
      {r.hitAt.map((h, i) => <Text key={i} size="sm"><b>{i + 1}</b> {(h * 100).toFixed(0)}%</Text>)}
      <Text size="sm" c="dimmed">MRR {number(r.mrr, 3)} · NDCG@{r.k} {number(r.ndcg, 3)}</Text>
    </Group>
  );
}

// Confusion shows each real answer (row) against what the model said
// (column), shaded by the share of that row.
function Confusion({ r }: { r: Report }) {
  const labels = r.classes.map((c) => c.label);
  return (
    <div>
      <Title order={4} fz="sm" mb={4}>What it mixed up</Title>
      <Text size="xs" c="dimmed" mb="xs">Rows are the real answer, columns what it said. The diagonal is right.</Text>
      <div className="max-w-full overflow-x-auto">
        <table className="border-separate border-spacing-[2px] text-xs">
          <thead>
            <tr><th />{labels.map((l) => <th key={l} className="max-w-20 truncate px-1 font-normal text-[var(--mantine-color-dimmed)]" title={l}>{l}</th>)}</tr>
          </thead>
          <tbody>
            {r.confusion.map((row, i) => {
              const total = row.counts.reduce((a, b) => a + Number(b), 0) || 1;
              return (
                <tr key={i}>
                  <th className="max-w-28 truncate pr-2 text-right font-normal text-[var(--mantine-color-dimmed)]" title={labels.at(i)}>{labels.at(i)}</th>
                  {row.counts.map((n, j) => {
                    const share = Number(n) / total;
                    return (
                      <td key={j} title={`${labels.at(i)} said as ${labels.at(j)}: ${n.toLocaleString()} (${(share * 100).toFixed(0)}%)`}
                        className="h-8 min-w-10 rounded px-1 text-center"
                        style={{ background: `color-mix(in srgb, var(--run-1) ${Math.round(share * 85)}%, transparent)`, color: share > 0.55 ? "white" : undefined, fontWeight: i === j ? 600 : undefined }}>
                        {n.toLocaleString()}
                      </td>
                    );
                  })}
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </div>
  );
}
