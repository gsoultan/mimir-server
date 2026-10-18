import { useMutation } from "@connectrpc/connect-query";
import { Alert, Button, Card, FileInput, Group, NumberInput, Progress, SegmentedControl, Select, SimpleGrid, Stack, Text, Textarea, TextInput, Title } from "@mantine/core";
import { IconAlertCircle, IconPhoto } from "@tabler/icons-react";
import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useEffect, useRef, useState } from "react";
import { z } from "zod";

import { message } from "../../api/client";
import { PredictService, type Column } from "../../api/gen/mimir/v1/model_pb";
import { listArchitectures, listModels } from "../../api/queries";
import { DrawPad, type DrawPadHandle } from "../../components/DrawPad";
import { ButtonLink } from "../../components/links";
import { Page } from "../../components/Page";
import { Writer } from "../../components/Writer";
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
  const [photo, setPhoto] = useState<File | null>(null);
  const [preview, setPreview] = useState<string>();
  useEffect(() => {
    if (!photo) return setPreview(undefined);
    const url = URL.createObjectURL(photo);
    setPreview(url);
    return () => URL.revokeObjectURL(url);
  }, [photo]);
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
  const pickModel = (
    <Select
      label="Model"
      className="max-w-sm"
      data={data.models.map((m) => ({ value: m.id, label: m.name }))}
      value={model.id}
      allowDeselect={false}
      onChange={(id) => { predict.reset(); pad.current?.clear(); setValues({}); setPhoto(null); void navigate({ search: { model: id ?? undefined } }); }}
    />
  );
  if (arch?.output === "text") {
    return (
      <Page title="Try a model" intro="Start a text and see how the model carries on.">
        {pickModel}
        <Writer key={model.id} modelId={model.id} />
      </Page>
    );
  }
  const best = predict.data?.guesses[0];
  // A digits model takes a drawing, a photo model a photo, and a table
  // model one value per column; photo-and-table models take both.
  const draw = arch?.input === "image" && (arch.inputShape.length > 0);
  const wantsPhoto = (arch?.input === "image" && !draw) || arch?.input === "photo_table";
  const columns: Column[] = draw || arch?.input === "image" ? [] : model.columns.length > 0
    ? model.columns.filter((c) => c.kind !== "photo")
    : model.features.map((name) => ({ name, kind: "number", values: [], fill: 0 }) as unknown as Column);
  const table = columns.length > 0 || wantsPhoto;
  const shape = model.inputShape.length > 0 ? model.inputShape : arch?.inputShape ?? [];
  const filled = (!wantsPhoto || !!photo) && columns.every((c) => c.kind !== "number" || (values[c.name] ?? "") !== "");
  const ask = async () => {
    if (draw) return predict.mutate({ modelId: model.id, input: { shape, values: Array.from(pad.current!.read()) }, topK: 3 });
    const fields: Record<string, string> = {};
    for (const c of columns) fields[c.name] = String(values[c.name] ?? "");
    predict.mutate({ modelId: model.id, fields, photo: photo ? new Uint8Array(await photo.arrayBuffer()) : new Uint8Array(), topK: 3 });
  };
  const change = (name: string, v: number | string) => { predict.reset(); setValues({ ...values, [name]: v }); };
  const number = predict.data?.number;

  return (
    <Page title="Try a model" intro={table ? `${wantsPhoto ? "Pick a photo" : "Fill in one example"} and see what the model answers.` : "Draw a number and see what the model thinks it is."}>
      {pickModel}
      <SimpleGrid cols={{ base: 1, md: 2 }} spacing="xl">
        {table ? (
          <Stack>
            <Card withBorder radius="lg" padding="lg">
              <Stack gap="sm">
                {wantsPhoto && (
                  <FileInput
                    label="Photo"
                    description="A JPG, PNG or GIF."
                    accept="image/jpeg,image/png,image/gif"
                    leftSection={<IconPhoto size={18} />}
                    placeholder="Choose a photo"
                    value={photo}
                    onChange={(f) => { predict.reset(); setPhoto(f); }}
                    clearable
                  />
                )}
                {preview && <img src={preview} alt="The photo you picked" className="max-h-48 self-start rounded-lg object-contain" />}
                {columns.map((c) => <Field key={c.name} column={c} value={values[c.name] ?? ""} onChange={(v) => change(c.name, v)} />)}
              </Stack>
            </Card>
            <Button size="lg" loading={predict.isPending} disabled={!filled} onClick={() => void ask()}>What is it?</Button>
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
          {number && predict.data ? (
            <Card withBorder radius="lg" padding="lg" aria-live="polite">
              <Text c="dimmed" size="sm">The model&apos;s answer</Text>
              <Title order={2} fz={40} className="font-[family-name:var(--font-display)]">{predict.data.value.toLocaleString(undefined, { maximumFractionDigits: 2 })}</Title>
              <Text size="xs" c="dimmed" mt="md">Answered in {(Number(predict.data.durationMicros) / 1000).toFixed(1)} ms.</Text>
            </Card>
          ) : best ? (
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
              <Text c="dimmed">{table ? `${wantsPhoto ? "Pick a photo" : "Fill in the details"}, then press "What is it?". ${arch?.output === "number" ? "The answer will be a number." : `The answer will be one of: ${model.labels.join(", ")}.`}` : 'Draw one digit, 0 to 9, as big as the box. Then press "What number is this?"'}</Text>
            </Card>
          )}
        </Stack>
      </SimpleGrid>
    </Page>
  );
}

// Field is one column's input, shaped by what the column holds.
function Field({ column: c, value, onChange }: { column: Column; value: number | string; onChange: (v: number | string) => void }) {
  switch (c.kind) {
    case "category":
      return <Select label={c.name} data={c.values} value={value === "" ? null : String(value)} onChange={(v) => onChange(v ?? "")} searchable clearable placeholder="Pick one" />;
    case "yesno":
      return (
        <Stack gap={4}>
          <Text size="sm" fw={500}>{c.name}</Text>
          <SegmentedControl className="self-start" data={[{ value: "yes", label: "Yes" }, { value: "no", label: "No" }]} value={String(value || "no")} onChange={onChange} />
        </Stack>
      );
    case "text":
      return <Textarea label={c.name} autosize minRows={2} value={String(value)} onChange={(e) => onChange(e.currentTarget.value)} />;
    case "number":
      return <NumberInput label={c.name} value={value} onChange={onChange} decimalScale={6} />;
  }
  return <TextInput label={c.name} value={String(value)} onChange={(e) => onChange(e.currentTarget.value)} />;
}
