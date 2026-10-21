import { useMutation } from "@connectrpc/connect-query";
import { Alert, Button, Card, FileInput, Group, NumberInput, Progress, SegmentedControl, Select, SimpleGrid, Stack, Text, Textarea, TextInput, Title } from "@mantine/core";
import { IconAlertCircle, IconPhoto } from "@tabler/icons-react";
import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useEffect, useRef, useState } from "react";
import { z } from "zod";

import { message } from "../../api/client";
import { PredictService, type Column } from "../../api/gen/mimir/v1/model_pb";
import { listArchitectures, listModels, whoAmI } from "../../api/queries";
import { Chat } from "../../components/Chat";
import { DrawPad, type DrawPadHandle } from "../../components/DrawPad";
import { ButtonLink } from "../../components/links";
import { Page } from "../../components/Page";
import { Writer } from "../../components/Writer";
import { percent } from "../../lib/format";

export const Route = createFileRoute("/_app/try")({
  validateSearch: z.object({ model: z.string().optional(), mode: z.enum(["write"]).optional() }),
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
  const { model: selected, mode: writeMode } = Route.useSearch();
  const mode = writeMode ?? "chat";
  const { data: me } = useSuspenseQuery(whoAmI);
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
          <Text mb="md">Build or upload a model first, then come back to try it.</Text>
          <ButtonLink to="/models/build">Build a model</ButtonLink>
        </Card>
      </Page>
    );
  }
  const arch = archs.architectures.find((a) => a.name === model.architecture);
  const pickModel = (
    <Select
      label="Model"
      className="w-full max-w-sm"
      data={data.models.map((m) => ({ value: m.id, label: m.name }))}
      value={model.id}
      allowDeselect={false}
      onChange={(id) => { predict.reset(); pad.current?.clear(); setValues({}); setPhoto(null); void navigate({ search: { model: id ?? undefined } }); }}
    />
  );
  if (arch?.output === "text") {
    const modes = (
      <div className="flex flex-wrap items-end gap-3">
        <div className="w-full sm:w-80">{pickModel}</div>
        <SegmentedControl data={[{ value: "chat", label: "Chat" }, { value: "write", label: "Carry on a text" }]} value={mode} onChange={(v) => void navigate({ search: { model: model.id, mode: v === "write" ? "write" : undefined } })} />
      </div>
    );
    return (
      <Page title="Try a model" intro={mode === "chat" ? "Chat with a language model, the way your apps will." : "Start a text and see how the model carries on."}>
        {mode === "chat"
          ? <Chat key={model.id} model={model} canWrite={me.permissions.includes("models.write")} header={modes} />
          : <>{modes}<Writer key={model.id} modelId={model.id} /></>}
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
  const shape = model.inputShape.length > 0 ? model.inputShape : arch?.inputShape ?? [];
  // A model that reads plain numbers, with no columns to name them, takes
  // a list of them.
  const raw = !draw && !wantsPhoto && columns.length === 0;
  const table = columns.length > 0 || wantsPhoto || raw;
  const want = shape.reduce((n, d) => n * Number(d), 1);
  const rawValues = String(values[""] ?? "").split(/[\s,]+/).filter(Boolean).map(Number);
  // A custom model's own code reads its columns as numbers, in order.
  const inOrder = !!arch?.custom && model.columns.length === 0;
  const ranked = arch?.output === "ranked";
  const topK = ranked ? Math.max(Math.min(arch.items, model.labels.length), 1) : 3;
  const filled = raw
    ? rawValues.length === want && rawValues.every(Number.isFinite)
    : (!wantsPhoto || !!photo) && columns.every((c) => c.kind !== "number" || (values[c.name] ?? "") !== "");
  const ask = async () => {
    if (draw) return predict.mutate({ modelId: model.id, input: { shape, values: Array.from(pad.current!.read()) }, topK: 3 });
    if (raw) return predict.mutate({ modelId: model.id, input: { shape, values: rawValues }, topK });
    if (inOrder) return predict.mutate({ modelId: model.id, input: { shape, values: columns.map((c) => Number(values[c.name] ?? 0)) }, topK });
    const fields: Record<string, string> = {};
    for (const c of columns) fields[c.name] = String(values[c.name] ?? "");
    predict.mutate({ modelId: model.id, fields, photo: photo ? new Uint8Array(await photo.arrayBuffer()) : new Uint8Array(), topK });
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
                {raw && (
                  <Textarea
                    label={`The ${want === 1 ? "number" : `${want} numbers`} it reads`}
                    description={want === 1 ? "One number" : "Separated by commas or spaces"}
                    autosize
                    minRows={2}
                    value={String(values[""] ?? "")}
                    onChange={(e) => change("", e.currentTarget.value)}
                    error={rawValues.length > want ? `That's ${rawValues.length}; it reads ${want}.` : undefined}
                  />
                )}
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
                  <Title order={2} fz={22}>{ranked ? `First pick: ${best.label}` : table ? best.label : `It's a ${best.label}`}</Title>
                  <Text c="dimmed">{sureness(best.probability)}: {Math.round(best.probability * 100)} out of 100.</Text>
                </div>
              </Group>
              <Stack gap="xs" mt="lg">
                <Text size="sm" fw={600}>{ranked ? `Top ${predict.data!.guesses.length}, best first` : "Top guesses"}</Text>
                {predict.data!.guesses.map((g, i) => (
                  <Group key={g.label} wrap="nowrap" gap="sm">
                    <Text w={table ? 96 : 24} fw={600} truncate>{ranked ? `${i + 1}. ${g.label}` : g.label}</Text>
                    <Progress value={g.probability * 100} className="flex-1" size="lg" radius="xl" aria-label={`${g.label}: ${percent(g.probability)}`} />
                    <Text w={44} size="sm" ta="right">{percent(g.probability)}</Text>
                  </Group>
                ))}
                <Text size="xs" c="dimmed">Answered in {(Number(predict.data!.durationMicros) / 1000).toFixed(1)} ms.</Text>
              </Stack>
            </Card>
          ) : (
            <Card withBorder radius="lg" padding="lg" className="border-dashed">
              <Text c="dimmed">{table ? `${wantsPhoto ? "Pick a photo" : "Fill in the details"}, then press "What is it?". ${arch?.output === "number" ? "The answer will be a number." : ranked ? `The answer lists the ${topK} that fit best${model.labels.length <= 12 ? ` of: ${model.labels.join(", ")}` : ""}.` : `The answer will be one of: ${model.labels.slice(0, 12).join(", ")}${model.labels.length > 12 ? "…" : ""}.`}` : 'Draw one digit, 0 to 9, as big as the box. Then press "What number is this?"'}</Text>
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
