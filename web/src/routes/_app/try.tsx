import { useMutation } from "@connectrpc/connect-query";
import { Alert, Button, Card, Group, NumberInput, Progress, Select, SimpleGrid, Stack, Text, Title } from "@mantine/core";
import { IconAlertCircle } from "@tabler/icons-react";
import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useRef, useState } from "react";
import { z } from "zod";

import { message } from "../../api/client";
import { PredictService } from "../../api/gen/mimir/v1/model_pb";
import { listArchitectures, listModels } from "../../api/queries";
import { DrawPad, type DrawPadHandle } from "../../components/DrawPad";
import { ButtonLink } from "../../components/links";
import { Page } from "../../components/Page";
import { percent } from "../../lib/format";

export const Route = createFileRoute("/_app/try")({
  validateSearch: z.object({ model: z.string().optional() }),
  loader: ({ context }) => Promise.all([
    context.queryClient.ensureQueryData(listModels),
    context.queryClient.ensureQueryData(listArchitectures),
  ]),
  component: Try,
});

// How sure the model is, in words.
function sureness(p: number) {
  if (p >= 0.9) return "Very sure";
  if (p >= 0.6) return "Fairly sure";
  return "Not sure";
}

function Try() {
  const { model: selected } = Route.useSearch();
  const navigate = Route.useNavigate();
  const { data } = useSuspenseQuery(listModels);
  const { data: archs } = useSuspenseQuery(listArchitectures);
  const pad = useRef<DrawPadHandle>(null);
  const [values, setValues] = useState<Record<string, number | string>>({});
  const predict = useMutation(PredictService.method.predict);

  const model = data.models.find((m) => m.id === selected) ?? data.models[0];
  if (!model) {
    return (
      <Page title="Try a model">
        <Card withBorder radius="lg" padding="xl">
          <Text mb="md">Add a model first, then come back to try it.</Text>
          <ButtonLink to="/models/new">Add a model</ButtonLink>
        </Card>
      </Page>
    );
  }
  const arch = archs.architectures.find((a) => a.name === model.architecture);
  const best = predict.data?.guesses[0];
  // A model taught from a table takes one number per column; a digits model takes a drawing.
  const table = model.features.length > 0;
  const shape = model.inputShape.length > 0 ? model.inputShape : arch?.inputShape ?? [];
  const filled = table && model.features.every((f) => values[f] !== undefined && values[f] !== "");
  const ask = () => predict.mutate({
    modelId: model.id,
    input: { shape, values: table ? model.features.map((f) => Number(values[f])) : Array.from(pad.current!.read()) },
    topK: 3,
  });

  return (
    <Page title="Try a model" intro={table ? "Type in the numbers for one example and see what the model answers." : "Draw a number and see what the model thinks it is."}>
      <Select
        label="Model"
        className="max-w-sm"
        data={data.models.map((m) => ({ value: m.id, label: m.name }))}
        value={model.id}
        allowDeselect={false}
        onChange={(id) => { predict.reset(); pad.current?.clear(); setValues({}); void navigate({ search: { model: id ?? undefined } }); }}
      />
      <SimpleGrid cols={{ base: 1, md: 2 }} spacing="xl">
        {table ? (
          <Stack>
            <Card withBorder radius="lg" padding="lg">
              <Stack gap="sm">
                {model.features.map((f) => (
                  <NumberInput key={f} label={f} value={values[f] ?? ""} onChange={(v) => { predict.reset(); setValues({ ...values, [f]: v }); }} decimalScale={6} />
                ))}
              </Stack>
            </Card>
            <Button size="lg" loading={predict.isPending} disabled={!filled} onClick={ask}>What is it?</Button>
          </Stack>
        ) : (
        <Stack>
          <DrawPad ref={pad} onStroke={() => predict.reset()} />
          <Group className="max-w-[340px]" wrap="nowrap" gap="sm">
            <Button variant="default" size="lg" onClick={() => { pad.current?.clear(); predict.reset(); }}>Clear</Button>
            <Button
              size="lg"
              className="flex-1"
              loading={predict.isPending}
              onClick={() => pad.current && ask()}
            >
              What number is this?
            </Button>
          </Group>
        </Stack>
        )}
        <Stack>
          {predict.error && <Alert color="red" icon={<IconAlertCircle />}>{message(predict.error)}</Alert>}
          {best ? (
            <Card withBorder radius="lg" padding="lg" aria-live="polite">
              <Group wrap="nowrap" gap="lg">
                <div className={`flex ${table ? "min-w-20 px-4 text-2xl" : "size-20 text-5xl"} h-20 shrink-0 items-center justify-center rounded-2xl bg-[var(--mantine-color-teal-light)] font-[family-name:var(--font-display)] font-bold`}>
                  {best.label}
                </div>
                <div>
                  <Title order={2} fz={22}>{table ? best.label : `It's a ${best.label}`}</Title>
                  <Text c="dimmed">{sureness(best.probability)}: {Math.round(best.probability * 100)} out of 100.</Text>
                </div>
              </Group>
              <Stack gap="xs" mt="lg">
                <Text size="sm" fw={600}>Top guesses</Text>
                {predict.data!.guesses.map((g) => (
                  <Group key={g.label} wrap="nowrap" gap="sm">
                    <Text w={table ? 96 : 24} fw={600} truncate>{g.label}</Text>
                    <Progress value={g.probability * 100} className="flex-1" size="lg" radius="xl" aria-label={`${g.label}: ${percent(g.probability)}`} />
                    <Text w={44} size="sm" ta="right">{percent(g.probability)}</Text>
                  </Group>
                ))}
                <Text size="xs" c="dimmed">Answered in {(Number(predict.data!.durationMicros) / 1000).toFixed(1)} ms.</Text>
              </Stack>
            </Card>
          ) : (
            <Card withBorder radius="lg" padding="lg" className="border-dashed">
              <Text c="dimmed">{table ? `Fill in ${model.features.length} numbers, then press "What is it?". The answer will be one of: ${model.labels.join(", ")}.` : 'Draw one digit, 0 to 9, as big as the box. Then press "What number is this?"'}</Text>
            </Card>
          )}
        </Stack>
      </SimpleGrid>
    </Page>
  );
}
