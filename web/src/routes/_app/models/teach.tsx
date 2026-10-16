import { useMutation } from "@connectrpc/connect-query";
import { Alert, Button, Card, Collapse, Group, NumberInput, PasswordInput, Radio, SegmentedControl, Select, SimpleGrid, Stack, Stepper, Table, Text, Textarea, TextInput, UnstyledButton } from "@mantine/core";
import { Dropzone } from "@mantine/dropzone";
import { useDisclosure } from "@mantine/hooks";
import { notifications } from "@mantine/notifications";
import { IconAlertCircle, IconCheck, IconDatabase, IconFileSpreadsheet, IconNumbers, IconTable } from "@tabler/icons-react";
import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useState } from "react";

import { message } from "../../../api/client";
import { DatasetKind, DatasetService, TrainingService, type Dataset } from "../../../api/gen/mimir/v1/training_pb";
import { allDatasetQueries, allTrainingQueries, listArchitectures, listDatasets, whoAmI } from "../../../api/queries";
import { Page } from "../../../components/Page";
import { bytes } from "../../../lib/format";
import { repeatChoices } from "../../../lib/training";

export const Route = createFileRoute("/_app/models/teach")({
  loader: ({ context }) => Promise.all([
    context.queryClient.ensureQueryData(listDatasets),
    context.queryClient.ensureQueryData(listArchitectures),
  ]),
  component: Teach,
});

const MaxCSV = 64 * 1024 * 1024;

// How long to teach for. Plain words up front, the numbers underneath.
const presets = [
  { value: "quick", label: "Quick look", epochs: 3, hint: "A few minutes. Good for a first try." },
  { value: "normal", label: "Normal", epochs: 10, hint: "The usual choice." },
  { value: "thorough", label: "Thorough", epochs: 30, hint: "Takes longer; squeezes out the last few points." },
];

type Source = "existing" | "upload" | "database" | "digits";

const drivers = [
  { value: "postgres", label: "PostgreSQL", example: "postgres://user:password@db.example.com:5432/shop" },
  { value: "mysql", label: "MySQL", example: "user:password@tcp(db.example.com:3306)/shop" },
  { value: "sqlite", label: "SQLite", example: "/srv/data/shop.db" },
];
type Upload = { file: File; bytes: Uint8Array; columns: string[]; rows: string[][]; count: number };

// Reads the header and first rows of a CSV without a parser; enough to pick a
// column and show a preview. The server does the real checking.
function peek(text: string) {
  const lines = text.split(/\r?\n/).filter((l) => l.trim() !== "");
  const split = (l: string) => l.split(",").map((c) => c.trim().replace(/^"|"$/g, ""));
  return { columns: split(lines[0] ?? ""), rows: lines.slice(1, 6).map(split), count: Math.max(lines.length - 1, 0) };
}

function Teach() {
  const { data: sets } = useSuspenseQuery(listDatasets);
  const { data: archs } = useSuspenseQuery(listArchitectures);
  const { data: me } = useSuspenseQuery(whoAmI);
  const canConnect = me.permissions.includes("settings.manage");
  const navigate = useNavigate();
  const { queryClient } = Route.useRouteContext();
  const [step, setStep] = useState(0);
  const [source, setSource] = useState<Source>(sets.datasets.length > 0 ? "existing" : "upload");
  const [existing, setExisting] = useState(sets.datasets[0]?.id ?? "");
  const [upload, setUpload] = useState<Upload>();
  const [target, setTarget] = useState("");
  const [fileError, setFileError] = useState<string>();
  const [dataset, setDataset] = useState<Dataset>();
  const [arch, setArch] = useState("");
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [preset, setPreset] = useState("normal");
  const [expert, { toggle }] = useDisclosure(false);
  const [epochs, setEpochs] = useState<number | string>(10);
  const [batch, setBatch] = useState<number | string>(32);
  const [rate, setRate] = useState<number | string>(0.005);
  const [holdOut, setHoldOut] = useState<number | string>(20);
  const [driver, setDriver] = useState("postgres");
  const [dsn, setDsn] = useState("");
  const [query, setQuery] = useState("");
  const [examplesName, setExamplesName] = useState("");
  const [repeat, setRepeat] = useState("0");
  const tested = useMutation(DatasetService.method.testQuery, {
    onSuccess: (res) => setTarget(res.columns[res.columns.length - 1] ?? ""),
  });

  const createDataset = useMutation(DatasetService.method.createDataset, {
    onSuccess: async (res) => {
      await queryClient.invalidateQueries({ queryKey: allDatasetQueries });
      if (res.dataset) chose(res.dataset);
    },
  });
  const createJob = useMutation(TrainingService.method.createJob, {
    onSuccess: async (res) => {
      await queryClient.invalidateQueries({ queryKey: allTrainingQueries });
      notifications.show({ color: "teal", icon: <IconCheck size={18} />, message: "Teaching has started. You can watch it learn." });
      await navigate({ to: "/training/$id", params: { id: res.job!.id } });
    },
  });

  // Moves to step 2 with the examples picked.
  function chose(d: Dataset) {
    setDataset(d);
    const allowed = archs.architectures.filter((a) => d.architectures.includes(a.name));
    setArch((cur) => (allowed.some((a) => a.name === cur) ? cur : allowed[0]?.name ?? ""));
    if (!name) setName(d.kind === DatasetKind.MNIST ? "My digits model" : `${d.name.replace(/\.csv$/i, "")} model`);
    if (d.kind === DatasetKind.MNIST) { setBatch(64); setRate(0.001); }
    setStep(1);
  }

  async function pick(file: File) {
    setFileError(undefined);
    if (file.size > MaxCSV) return setFileError(`That file is ${bytes(file.size)}. The limit is ${bytes(MaxCSV)}.`);
    const buf = new Uint8Array(await file.arrayBuffer());
    const head = new TextDecoder().decode(buf.subarray(0, 64 * 1024));
    const p = peek(head);
    if (p.columns.length < 2) return setFileError("The first row should name at least two columns, separated by commas.");
    setUpload({ file, bytes: buf, ...p, count: p.count });
    setTarget(p.columns[p.columns.length - 1]!);
  }

  function next() {
    if (source === "existing") {
      const d = sets.datasets.find((d) => d.id === existing);
      if (d) chose(d);
    } else if (source === "database" && tested.data) {
      createDataset.mutate({ name: examplesName.trim() || "Examples from a query", kind: DatasetKind.TABLE, connection: { driver, dsn }, query, target });
    } else if (source === "upload" && upload) {
      createDataset.mutate({ name: upload.file.name, kind: DatasetKind.TABLE, csv: upload.bytes, target });
    } else if (source === "digits") {
      const have = sets.datasets.find((d) => d.kind === DatasetKind.MNIST);
      if (have) chose(have);
      else createDataset.mutate({ name: "Handwritten digits", kind: DatasetKind.MNIST });
    }
  }

  const allowed = archs.architectures.filter((a) => dataset?.architectures.includes(a.name));
  const ready0 = source === "digits" || (source === "existing" && !!existing) || (source === "upload" && !!upload && !!target) || (source === "database" && !!tested.data && !!target);
  const fromQuery = !!dataset?.query;
  const chosenEpochs = expert ? Number(epochs) : presets.find((p) => p.value === preset)!.epochs;
  const start = () => dataset && createJob.mutate({
    name, description, architecture: arch, datasetId: dataset.id, repeatSeconds: fromQuery ? BigInt(repeat) : 0n,
    // Outside the expert settings, zero lets the server pick what suits the examples.
    settings: { epochs: chosenEpochs, batchSize: expert ? Number(batch) : 0, learningRate: expert ? Number(rate) : 0, holdOut: expert ? Number(holdOut) / 100 : 0.2 },
  });

  return (
    <Page title="Teach a new model" intro="Give it examples, pick what kind of model, and watch it learn. Three short steps.">
      <Card withBorder radius="lg" padding="xl">
        <Stepper active={step} onStepClick={(s) => s < step && setStep(s)} size="sm">
          <Stepper.Step label="The examples" description="What to learn from">
            <Radio.Group value={source} onChange={(v) => setSource(v as Source)} label="Where are the examples?" mt="md">
              <Stack gap="sm" mt="sm">
                {sets.datasets.length > 0 && (
                  <Choice value="existing" icon={<IconTable size={22} />} title="Examples already here" hint="Reuse a table you uploaded before.">
                    {source === "existing" && (
                      <Select mt="sm" data={sets.datasets.map((d) => ({ value: d.id, label: `${d.name} · ${d.rows.toLocaleString()} rows · ${d.labels.length} answers${d.query ? " · from a database" : ""}` }))} value={existing} onChange={(v) => v && setExisting(v)} allowDeselect={false} aria-label="Examples" />
                    )}
                  </Choice>
                )}
                <Choice value="upload" icon={<IconFileSpreadsheet size={22} />} title="Upload a table" hint="A .csv file with a header row. One column holds the answer; the rest are numbers that describe each example.">
                  {source === "upload" && (
                    <Stack mt="sm" gap="sm">
                      <Dropzone onDrop={(files) => files[0] && pick(files[0])} onReject={() => setFileError("Pick a single .csv file.")} maxFiles={1} multiple={false} accept={["text/csv", ".csv"]} radius="md">
                        <Stack align="center" gap={2} py="md" className="pointer-events-none">
                          <Text fw={600}>{upload ? upload.file.name : "Drop your .csv file here"}</Text>
                          <Text size="sm" c="dimmed">{upload ? `${bytes(upload.file.size)} · ${upload.columns.length} columns` : "or click to choose one"}</Text>
                        </Stack>
                      </Dropzone>
                      {fileError && <Alert color="red" icon={<IconAlertCircle />}>{fileError}</Alert>}
                      {upload && (
                        <>
                          <Select label="Which column is the answer?" description="The model learns to predict this from the other columns." data={upload.columns} value={target} onChange={(v) => v && setTarget(v)} allowDeselect={false} />
                          <Table.ScrollContainer minWidth={300}>
                            <Table fz="xs" withTableBorder>
                              <Table.Thead><Table.Tr>{upload.columns.map((c) => <Table.Th key={c} c={c === target ? "teal" : undefined}>{c}</Table.Th>)}</Table.Tr></Table.Thead>
                              <Table.Tbody>{upload.rows.map((r, i) => <Table.Tr key={i}>{r.map((v, k) => <Table.Td key={k}>{v}</Table.Td>)}</Table.Tr>)}</Table.Tbody>
                            </Table>
                          </Table.ScrollContainer>
                        </>
                      )}
                    </Stack>
                  )}
                </Choice>
                {canConnect && (
                  <Choice value="database" icon={<IconDatabase size={22} />} title="From a database" hint="Run a query on PostgreSQL, MySQL or SQLite. The rows become the examples, and the model can keep learning as they change.">
                    {source === "database" && (
                      <Stack mt="sm" gap="sm">
                        <SegmentedControl value={driver} onChange={(v) => { setDriver(v); tested.reset(); }} data={drivers.map((d) => ({ value: d.value, label: d.label }))} />
                        {driver === "sqlite" ? (
                          <TextInput label="Database file" description="The path to the file on the server, for example /srv/data/shop.db." value={dsn} onChange={(e) => { setDsn(e.currentTarget.value); tested.reset(); }} />
                        ) : <PasswordInput label="Connection" description={`For example ${drivers.find((d) => d.value === driver)!.example}. Kept on the server; never shown again.`} value={dsn} onChange={(e) => { setDsn(e.currentTarget.value); tested.reset(); }} />}
                        <Textarea label="Query" description="One row per example. Number columns to learn from, plus the answer column." placeholder="SELECT weight_grams, height_cm, sweetness, fruit FROM harvest" autosize minRows={3} value={query} onChange={(e) => { setQuery(e.currentTarget.value); tested.reset(); }} classNames={{ input: "font-mono" }} />
                        <Group><Button variant="default" loading={tested.isPending} disabled={!dsn.trim() || !query.trim()} onClick={() => tested.mutate({ connection: { driver, dsn }, query })}>Try the query</Button></Group>
                        {tested.error && <Alert color="red" icon={<IconAlertCircle />}>{message(tested.error)}</Alert>}
                        {tested.data && (
                          <>
                            <Alert color="teal" icon={<IconCheck />}>{tested.data.rows.toLocaleString()} rows, {tested.data.columns.length} columns.</Alert>
                            <Select label="Which column is the answer?" description="The model learns to predict this from the other columns." data={tested.data.columns} value={target} onChange={(v) => v && setTarget(v)} allowDeselect={false} />
                            <Table.ScrollContainer minWidth={300}>
                              <Table fz="xs" withTableBorder>
                                <Table.Thead><Table.Tr>{tested.data.columns.map((c) => <Table.Th key={c} c={c === target ? "teal" : undefined}>{c}</Table.Th>)}</Table.Tr></Table.Thead>
                                <Table.Tbody>{tested.data.sample.map((r, i) => <Table.Tr key={i}>{r.values.map((v, k) => <Table.Td key={k}>{v}</Table.Td>)}</Table.Tr>)}</Table.Tbody>
                              </Table>
                            </Table.ScrollContainer>
                            <TextInput label="Name these examples" placeholder="Harvest from the shop database" maxLength={100} value={examplesName} onChange={(e) => setExamplesName(e.currentTarget.value)} />
                          </>
                        )}
                      </Stack>
                    )}
                  </Choice>
                )}
                <Choice value="digits" icon={<IconNumbers size={22} />} title="Handwritten digits" hint="70,000 pictures of the numbers 0 to 9, built in. Downloaded once on first use.">{null}</Choice>
              </Stack>
            </Radio.Group>
            {createDataset.error && <Alert mt="md" color="red" icon={<IconAlertCircle />}>{message(createDataset.error)}</Alert>}
          </Stepper.Step>

          <Stepper.Step label="What kind" description="Pick the model type">
            {dataset && (
              <Alert mt="md" color="teal" icon={<IconCheck />}>
                {dataset.name}: {dataset.rows.toLocaleString()} examples, {dataset.labels.length} possible answers
                {dataset.features.length > 0 && ` (from ${dataset.features.join(", ")})`}.
              </Alert>
            )}
            <Radio.Group value={arch} onChange={setArch} label="What kind of model?" mt="md">
              <Stack gap="sm" mt="sm">
                {allowed.map((a) => (
                  <Radio.Card key={a.name} value={a.name} radius="md" p="md" className="data-[checked]:border-[var(--mantine-color-teal-7)]">
                    <Group wrap="nowrap" align="flex-start">
                      <Radio.Indicator />
                      <div><Text fw={600}>{a.title}</Text><Text size="sm" c="dimmed">{a.description}</Text></div>
                    </Group>
                  </Radio.Card>
                ))}
              </Stack>
            </Radio.Group>
            <Stack mt="lg">
              <TextInput label="Name" required maxLength={100} value={name} onChange={(e) => setName(e.currentTarget.value)} />
              <Textarea label="Description" description="Optional. What is it for?" maxLength={500} autosize minRows={2} value={description} onChange={(e) => setDescription(e.currentTarget.value)} />
            </Stack>
          </Stepper.Step>

          <Stepper.Step label="How long" description="Then start">
            <Radio.Group value={preset} onChange={setPreset} label="How hard should it study?" mt="md">
              <SimpleGrid cols={{ base: 1, sm: 3 }} mt="sm">
                {presets.map((p) => (
                  <Radio.Card key={p.value} value={p.value} radius="md" p="md" disabled={expert} className="data-[checked]:border-[var(--mantine-color-teal-7)]">
                    <Group wrap="nowrap" align="flex-start">
                      <Radio.Indicator />
                      <div><Text fw={600}>{p.label}</Text><Text size="sm" c="dimmed">{p.epochs} passes over the examples. {p.hint}</Text></div>
                    </Group>
                  </Radio.Card>
                ))}
              </SimpleGrid>
            </Radio.Group>
            <UnstyledButton mt="md" onClick={toggle} className="text-sm text-[var(--mantine-color-teal-7)] underline">{expert ? "Hide expert settings" : "Expert settings"}</UnstyledButton>
            <Collapse expanded={expert}>
              <SimpleGrid cols={{ base: 1, sm: 2 }} mt="sm">
                <NumberInput label="Passes over the examples" description="Epochs" min={1} max={1000} value={epochs} onChange={setEpochs} />
                <NumberInput label="Examples per step" description="Batch size" min={1} max={4096} value={batch} onChange={setBatch} />
                <NumberInput label="Learning rate" description="How big each adjustment is" min={0.000001} max={1} step={0.0005} decimalScale={6} value={rate} onChange={setRate} />
                <NumberInput label="Kept aside for scoring" description="Percent of examples the model never learns from, used to measure it honestly" min={0} max={50} suffix="%" value={holdOut} onChange={setHoldOut} />
              </SimpleGrid>
            </Collapse>
            {fromQuery && (
              <Stack gap={4} mt="lg">
                <Text fw={500} size="sm">Keep learning</Text>
                <Text size="xs" c="dimmed">Run the query again on a schedule and continue teaching the same model with the new rows. The model is only replaced when the new pass scores at least as well.</Text>
                <SegmentedControl value={repeat} onChange={setRepeat} data={repeatChoices} className="self-start" />
              </Stack>
            )}
            <Text size="sm" c="dimmed" mt="md">
              {name || "The model"} will learn from {dataset?.rows.toLocaleString()} examples over {chosenEpochs} passes{fromQuery && repeat !== "0" && `, then keep learning ${repeatChoices.find((c) => c.value === repeat)!.label.toLowerCase()}`}. You can stop it any time and pick up later.
            </Text>
            {createJob.error && <Alert mt="md" color="red" icon={<IconAlertCircle />}>{message(createJob.error)}</Alert>}
          </Stepper.Step>
        </Stepper>

        <Group justify="space-between" mt="xl">
          <Button variant="default" onClick={() => setStep(step - 1)} disabled={step === 0 || createJob.isPending}>Back</Button>
          {step === 0 && <Button onClick={next} loading={createDataset.isPending} disabled={!ready0}>Next</Button>}
          {step === 1 && <Button onClick={() => setStep(2)} disabled={!arch || !name.trim()}>Next</Button>}
          {step === 2 && <Button loading={createJob.isPending} disabled={!dataset || !arch || !name.trim()} onClick={start}>Start teaching</Button>}
        </Group>
      </Card>
    </Page>
  );
}

function Choice({ value, icon, title, hint, children }: { value: Source; icon: React.ReactNode; title: string; hint: string; children: React.ReactNode }) {
  return (
    <Radio.Card value={value} radius="md" p="md" className="data-[checked]:border-[var(--mantine-color-teal-7)]">
      <Group wrap="nowrap" align="flex-start">
        <Radio.Indicator />
        <div className="min-w-0 flex-1">
          <Group gap="xs">{icon}<Text fw={600}>{title}</Text></Group>
          <Text size="sm" c="dimmed">{hint}</Text>
          <div onClick={(e) => e.stopPropagation()}>{children}</div>
        </div>
      </Group>
    </Radio.Card>
  );
}
