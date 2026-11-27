import { Alert, Group, SimpleGrid, Stack, Table, Text, Title } from "@mantine/core";
import { IconAlertTriangle, IconBulb, IconCheck } from "@tabler/icons-react";
import { useState } from "react";

import type { RunReport as Report } from "../api/gen/mimir/v1/training_pb";
import { bytes } from "../lib/format";
import { balanced, number, short } from "../lib/report";
import { verdict } from "../lib/verdict";
import { t } from "../i18n";

// RunReport shows how a finished run's model did on the examples kept
// aside: one plain headline, the expert numbers, and one picture for its
// kind of answer.
export function RunReport({ report: r }: { report: Report }) {
  const on = r.onTraining
    ? t("the examples it learned from, since none were kept aside")
    : r.kind === "boxes"
      ? t("{n1} photos it never saw while learning", { n1: r.examples.toLocaleString() })
      : t("{n1} examples it never saw while learning", { n1: r.examples.toLocaleString() });
  const v = verdict(r);
  return (
    <Stack gap="md">
      {v && (
        <Alert
          color={v.level === "good" ? "teal" : v.level === "weak" ? "orange" : "red"}
          variant="light"
          icon={v.level === "good" ? <IconCheck /> : <IconAlertTriangle />}
          title={v.title}
        >
          {v.body && <Text size="sm">{v.body}</Text>}
          {v.next && <Text size="sm" mt={v.body ? 6 : 0}><IconBulb size={14} className="mr-1 inline-block align-[-2px]" aria-hidden="true" /><b>{t("What would help:")}</b> {v.next}</Text>}
        </Alert>
      )}
      <Text size="sm" c="dimmed">{t("Scored on")}{" "}{on}.</Text>
      {r.copies > 0n && <Text size="sm" c="dimmed">{r.copies === 1n
        ? t("1 row picked for scoring was an exact copy of a row it learned from, so it wasn't scored.")
        : t("{n1} rows picked for scoring were exact copies of rows it learned from, so they weren't scored.", { n1: r.copies.toLocaleString() })}</Text>}
      {unsure(r) && <Text size="sm" c="dimmed">{t("Only {n1} examples were scored, so on new ones the score could be quite different. More rows, or keeping more of them aside, makes it surer.", { n1: r.examples.toLocaleString() })}</Text>}
      {r.made > 0n && <Text size="sm" c="dimmed">{t("It also learned from {n1} made-up rows of the rarer answers. None of them were scored.", { n1: r.made.toLocaleString() })}</Text>}
      {r.kind === "number" && <Numbers r={r} />}
      {(r.kind === "class" || r.kind === "ranked") && <Labels r={r} />}
      {r.kind === "boxes" && <Boxes r={r} />}
      {r.kind === "text" && <Writing r={r} />}
      <SimpleGrid cols={{ base: 2, sm: 3 }} spacing="xs">
        <Stat label={t("Answer time")} value={`${number(r.millisPerExample, 2)} ms`} hint={t("per example, while scoring")} />
        <Stat label={t("Model size")} value={bytes(r.sizeBytes)} />
        <Stat label={t("Examples scored")} value={r.examples.toLocaleString()} />
      </SimpleGrid>
    </Stack>
  );
}

// unsure says the range a score likely falls in is wide: more than ten
// points of right answers, or a miss that could be half again as large.
function unsure(r: Report) {
  if (r.onTraining) return false;
  if (r.kind === "number") return r.maeHigh > 0 && r.maeHigh - r.maeLow > r.mae;
  return r.accuracyHigh > 0 && r.accuracyHigh - r.accuracyLow > 0.2;
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
        <Stat label={t("Usually off by (MAE)")} value={number(r.mae)} hint={r.maeHigh > 0 ? t("likely {low} to {high} on new examples", { low: number(r.maeLow), high: number(r.maeHigh) }) : undefined} />
        <Stat label={t("Big misses count more (RMSE)")} value={number(r.rmse)} />
        <Stat label={t("Explains the answers (R²)")} value={number(r.r2, 3)} hint={t("1 is perfect, 0 is no better than the average")} />
        <Stat label={t("Within 10%")} value={`${(r.within10 * 100).toFixed(1)}%`} />
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
      <svg viewBox={`0 0 ${w} ${h}`} className="w-full" role="img" aria-label={t("Guesses against real answers")}>
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
        <text x={(pl + w - pr) / 2} y={h - 4} fontSize="11" textAnchor="middle" fill="var(--mantine-color-dimmed)">{t("Real answer")}</text>
        <text x={pl} y={12} fontSize="11" fill="var(--mantine-color-dimmed)">{t("Its guess")}</text>
      </svg>
      <Text size="xs" c="dimmed" ta="center">
        {p ? t("Real {real}, guessed {guess}: off by {n3}.", { real: number(p.real), guess: number(p.guess), n3: number(Math.abs(p.guess - p.real)) }) : t("Each dot is one example. On the dashed line is exactly right.")}
      </Text>
    </div>
  );
}

function Labels({ r }: { r: Report }) {
  const uneven = balanced(r);
  return (
    <>
      <SimpleGrid cols={{ base: 2, sm: 4 }} spacing="xs">
        <Stat label={t("Right answers")} value={`${(r.accuracy * 100).toFixed(1)}%`} hint={r.accuracyHigh > 0 ? t("likely {low}% to {high}% on new examples", { low: (r.accuracyLow * 100).toFixed(1), high: (r.accuracyHigh * 100).toFixed(1) }) : undefined} />
        <Stat label={t("Balanced score (macro F1)")} value={number(r.macroF1, 3)} hint={uneven ? t("the fairer score here: one answer is rare") : t("every answer counts the same")} />
        <Stat label={t("Sureness (log loss)")} value={number(r.logLoss, 3)} hint={t("lower is better")} />
        {r.kind === "ranked" && <Stat label={t("Right one in top {k}", { k: r.k })} value={`${((r.hitAt.at(-1) ?? 0) * 100).toFixed(1)}%`} />}
      </SimpleGrid>
      {r.kind === "ranked" && r.hitAt.length > 0 && <Ranked r={r} />}
      {r.confusion.length > 0 && <Confusion r={r} />}
      {r.classes.length > 0 && (
        <Table.ScrollContainer minWidth={420}>
          <Table verticalSpacing={4} fz="sm">
            <Table.Thead><Table.Tr><Table.Th>{t("Answer")}</Table.Th><Table.Th>{t("When it says this, it's right (precision)")}</Table.Th><Table.Th>{t("Finds it when it's there (recall)")}</Table.Th><Table.Th>F1</Table.Th><Table.Th>{t("Examples")}</Table.Th></Table.Tr></Table.Thead>
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

// Boxes scores a model that finds things in photos: overall, then for
// each thing it finds.
function Boxes({ r }: { r: Report }) {
  return (
    <>
      <SimpleGrid cols={{ base: 2, sm: 4 }} spacing="xs">
        <Stat label={t("Finds things (AP50)")} value={number(r.ap50, 3)} hint={t("1 is perfect; a box counts when it covers half the real one")} />
        <Stat label={t("Finds them closely (AP75)")} value={number(r.ap75, 3)} hint={t("a box counts when it covers three quarters")} />
      </SimpleGrid>
      {r.classes.length > 0 && (
        <Table.ScrollContainer minWidth={420}>
          <Table verticalSpacing={4} fz="sm">
            <Table.Thead><Table.Tr><Table.Th>{t("Thing")}</Table.Th><Table.Th>{t("When it finds this, it's right (precision)")}</Table.Th><Table.Th>{t("Finds it when it's there (recall)")}</Table.Th><Table.Th>AP50</Table.Th><Table.Th>{t("Real boxes")}</Table.Th></Table.Tr></Table.Thead>
            <Table.Tbody>
              {r.classes.map((c) => (
                <Table.Tr key={c.label}>
                  <Table.Td>{c.label}</Table.Td>
                  <Table.Td>{(c.precision * 100).toFixed(1)}%</Table.Td>
                  <Table.Td>{(c.recall * 100).toFixed(1)}%</Table.Td>
                  <Table.Td>{number(c.ap, 3)}</Table.Td>
                  <Table.Td>{c.support.toLocaleString()}</Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>
      )}
      <Text size="xs" c="dimmed">{t("Precision and recall count what it answers with, things it is at least 30% sure of.")}</Text>
    </>
  );
}

// Writing scores a fine-tuned language model on the answers of the
// examples kept aside, against how it did before this run taught it.
function Writing({ r }: { r: Report }) {
  const before = r.beforeLoss > 0;
  return (
    <SimpleGrid cols={{ base: 2, sm: 4 }} spacing="xs">
      <Stat label={t("Word pieces guessed right")} value={`${(r.accuracy * 100).toFixed(1)}%`} hint={before ? t("{pct}% before this run", { pct: (r.beforeAccuracy * 100).toFixed(1) }) : t("its first guess at each word piece of the answers")} />
      <Stat label={t("Surprise (perplexity)")} value={number(Math.exp(r.logLoss), 2)} hint={before ? t("{n} before this run; lower is better", { n: number(Math.exp(r.beforeLoss), 2) }) : t("lower is better")} />
      <Stat label={t("Word pieces scored")} value={r.tokens.toLocaleString()} />
    </SimpleGrid>
  );
}

function Ranked({ r }: { r: Report }) {
  return (
    <Group gap="lg" wrap="wrap">
      <Text size="sm">{t("Right one in the first:")}</Text>
      {r.hitAt.map((h, i) => <Text key={i} size="sm"><b>{i + 1}</b> {(h * 100).toFixed(0)}%</Text>)}
      <Text size="sm" c="dimmed">{t("MRR")}{" "}{number(r.mrr, 3)}{" "}{t("· NDCG@")}{r.k} {number(r.ndcg, 3)}</Text>
    </Group>
  );
}

// Confusion shows each real answer (row) against what the model said
// (column), shaded by the share of that row.
function Confusion({ r }: { r: Report }) {
  const labels = r.classes.map((c) => c.label);
  return (
    <div>
      <Title order={4} fz="sm" mb={4}>{t("What it mixed up")}</Title>
      <Text size="xs" c="dimmed" mb="xs">{t("Rows are the real answer, columns what it said. The diagonal is right.")}</Text>
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
                      <td key={j} title={t("{real} said as {guess}: {n} ({pct}%)", { real: labels.at(i), guess: labels.at(j), n: n.toLocaleString(), pct: (share * 100).toFixed(0) })}
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
