import { Alert, Code, Card, Text, Tooltip } from "@mantine/core";
import { IconAlertTriangle } from "@tabler/icons-react";
import { useQuery } from "@tanstack/react-query";

import type { Model } from "../api/gen/mimir/v1/model_pb";
import { modelQuality } from "../api/queries";
import { score } from "../lib/training";
import { locale, t } from "../i18n";
import { count } from "../lib/templates";

const day = 86_400_000;

// InUse shows how a model does on real questions: its answers each day,
// and of those apps sent the right answer for, how many it got.
export function InUse({ model: m, number }: { model: Model; number: boolean }) {
  const { data } = useQuery(modelQuality(m.id));
  const days = new Map((data?.days ?? []).map((d) => [Number(d.day?.seconds ?? 0n) * 1000, d]));
  const today = Math.floor(Date.now() / day) * day;
  const span = Array.from({ length: 30 }, (_, i) => today - (29 - i) * day);
  const curl = `curl ${location.origin}/mimir.v1.PredictService/SendFeedback \\
  -H 'Authorization: Bearer mimir_...' \\
  -H 'Content-Type: application/json' \\
  -d '{"predictionId": "<from the prediction>", ${number ? `"value": 42.5` : `"label": "${m.labels[0] ?? t("the right answer")}"`}}'`;

  return (
    <Card withBorder radius="lg" padding="lg">
      <Text fw={700} mb="sm">{t("How it does in use, the last 30 days")}</Text>
      {data?.dropped && (
        <Alert color="orange" icon={<IconAlertTriangle />} mb="sm">{t("It got")}{" "}{Math.round(data.weekAccuracy * 100)}{" "}{t("of 100 right this week, by the answers apps sent back, against")}{" "}{Math.round(m.accuracy * 100)}{" "}{t("when it was trained. Look at its versions, or teach it again with newer examples.")}</Alert>
      )}
      <div className="flex h-16 items-end gap-[2px]" role="img" aria-label={t("Answers each day for the last 30 days")}>
        {span.map((tick) => {
          const d = days.get(tick);
          const answers = Number(d?.answers ?? 0n), fb = Number(d?.feedback ?? 0n), ok = Number(d?.correct ?? 0n);
          const most = Math.max(1, ...[...days.values()].map((x) => Number(x.answers)));
          const label = `${new Date(tick).toLocaleDateString(locale, { month: "short", day: "numeric" })}: ${count(answers, "answer")}${fb > 0 ? t(", {n1} of {fb} checked", { n1: score(ok / fb, number), fb }) : ""}`;
          return (
            <Tooltip key={tick} label={label} withinPortal>
              <div className="flex h-full flex-1 flex-col justify-end">
                <div
                  className="rounded-sm"
                  style={{
                    height: `${answers > 0 ? Math.max(6, (answers / most) * 100) : 2}%`,
                    background: fb === 0 ? "var(--mantine-color-gray-4)" : ok / fb >= m.accuracy - 0.1 ? "var(--mantine-color-teal-6)" : "var(--mantine-color-orange-6)",
                  }}
                />
              </div>
            </Tooltip>
          );
        })}
      </div>
      <Text size="xs" c="dimmed" mt={4} mb="sm">{t("Last 30 days. Green days got about as many right as in training, orange fewer, grey had no answers sent back.")}</Text>
      <Text size="sm" c="dimmed" mb="xs">{t("Each prediction comes back with a predictionId. When your app learns the right answer, send it back, within 30 days:")}</Text>
      <Code block>{curl}</Code>
    </Card>
  );
}
