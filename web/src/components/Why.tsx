import { useMutation } from "@connectrpc/connect-query";
import { Alert, Box, Button, Card, Group, Progress, Stack, Text, Tooltip } from "@mantine/core";
import { IconAlertCircle, IconBulb } from "@tabler/icons-react";

import { message } from "../api/client";
import { PredictService, type ExplainResponse, type Model } from "../api/gen/mimir/v1/model_pb";
import { answerText, because, effectText } from "../lib/explain";

// Effects shows how much each column pushed an answer up or down: a bar
// to the right for up, to the left for down, against the biggest.
export function Effects({ x }: { x: ExplainResponse }) {
  const top = Math.max(...x.contributions.map((c) => Math.abs(c.effect)), 1e-9);
  return (
    <Stack gap={6}>
      {x.contributions.map((c) => {
        const share = (Math.abs(c.effect) / top) * 50;
        const up = c.effect >= 0;
        return (
          <Tooltip key={c.column} label={because(c, x.label, x.number)} multiline w={300}>
            <Group wrap="nowrap" gap="sm">
              <div className="w-36 min-w-0 shrink-0">
                <Text size="sm" fw={600} truncate>{c.column}</Text>
                <Text size="xs" c="dimmed" truncate>{c.value === "" ? "empty" : c.value} · typical {c.typical}</Text>
              </div>
              <Box className="relative h-3 flex-1 rounded-full" bg="var(--mantine-color-default-hover)">
                <Box className="absolute top-0 h-3 rounded-full" bg={up ? "teal" : "red"}
                  style={{ left: up ? "50%" : `${50 - share}%`, width: `${Math.max(share, 0.5)}%` }} />
                <Box className="absolute top-[-2px] h-4 w-px left-1/2" bg="var(--mantine-color-dimmed)" />
              </Box>
              <Text w={64} size="sm" ta="right" c={up ? "teal" : "red"}>{effectText(c.effect, x.number)}</Text>
            </Group>
          </Tooltip>
        );
      })}
    </Stack>
  );
}

// WhyAnswer explains one answer on asking.
export function WhyAnswer({ model, fields, label }: { model: Model; fields: Record<string, string>; label?: string }) {
  const explain = useMutation(PredictService.method.explain);
  const x = explain.data;
  if (!x) {
    return (
      <Stack gap="xs" mt="md">
        {explain.error && <Alert color="red" icon={<IconAlertCircle />}>{message(explain.error)}</Alert>}
        <Button variant="light" leftSection={<IconBulb size={18} />} loading={explain.isPending} onClick={() => explain.mutate({ modelId: model.id, fields, label: label ?? "" })}>
          Why this answer?
        </Button>
      </Stack>
    );
  }
  const what = x.number ? "the answer" : `the chance of ${x.label}`;
  return (
    <Stack gap="sm" mt="lg">
      <Text size="sm" fw={600}>Why this answer</Text>
      <Text size="sm" c="dimmed">
        For a typical row {what} is {answerText(x.typical, x.number)}; for this one it is {answerText(x.value, x.number)}.
        {x.contributions.length === 0 ? " Every column is the same as in the typical row." : " These columns made the difference:"}
      </Text>
      <Effects x={x} />
      {!x.exact && <Text size="xs" c="dimmed">Estimated from a sample of the ways the columns could combine.</Text>}
    </Stack>
  );
}

// ColumnsThatMatter shows which columns a model leans on most, worked out
// when asked.
export function ColumnsThatMatter({ model }: { model: Model }) {
  const explain = useMutation(PredictService.method.explainModel);
  const x = explain.data;
  return (
    <Card withBorder radius="lg" padding="lg">
      <Text fw={700} mb={4}>Columns that matter</Text>
      {!x ? (
        <Stack gap="sm" align="flex-start">
          <Text size="sm" c="dimmed">See which columns move its answers most, from the questions it was asked lately or the examples it learned from.</Text>
          {explain.error && <Alert color="red" icon={<IconAlertCircle />} w="100%">{message(explain.error)}</Alert>}
          <Button variant="default" leftSection={<IconBulb size={18} />} loading={explain.isPending} onClick={() => explain.mutate({ modelId: model.id })}>Work it out</Button>
        </Stack>
      ) : (
        <Stack gap={8}>
          <Text size="sm" c="dimmed" mb={4}>{x.source === "answers" ? (x.rows === 1 ? "From the latest question" : `From ${x.rows} recent questions`) : `From ${x.rows} of the examples it learned from`}: how much each column moves {x.number ? "its answer" : "the chance of its answer"} on average.</Text>
          {x.columns.map((c) => (
            <Group key={c.column} wrap="nowrap" gap="sm">
              <Text w={140} size="sm" fw={600} truncate>{c.column}</Text>
              <Progress value={c.share * 100} className="flex-1" size="lg" radius="xl" aria-label={`${c.column}: ${Math.round(c.share * 100)}%`} />
              <Text w={64} size="sm" ta="right">{x.number ? c.importance.toLocaleString(undefined, { maximumFractionDigits: 2 }) : `${Math.round(c.importance * 100)} pts`}</Text>
            </Group>
          ))}
        </Stack>
      )}
    </Card>
  );
}
