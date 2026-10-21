import { useMutation } from "@connectrpc/connect-query";
import { Alert, Badge, Button, Card, Collapse, Group, NumberInput, PasswordInput, Radio, SegmentedControl, Select, SimpleGrid, Stack, Stepper, Table, Text, Textarea, TextInput, UnstyledButton } from "@mantine/core";
import { Dropzone, type FileWithPath } from "@mantine/dropzone";
import { useDisclosure } from "@mantine/hooks";
import { notifications } from "@mantine/notifications";
import { IconAlertCircle, IconBucket, IconCheck, IconDatabase, IconFileSpreadsheet, IconFileZip, IconFolder, IconNumbers, IconSparkles, IconTable } from "@tabler/icons-react";
import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useEffect, useRef, useState } from "react";
import { z } from "zod";

import { message } from "../../../api/client";
import type { DataCheck as Check } from "../../../api/gen/mimir/v1/template_pb";
import { DatasetKind, DatasetService, TrainingService, type Dataset } from "../../../api/gen/mimir/v1/training_pb";
import { allDatasetQueries, allTrainingQueries, listArchitectures, listDatasets, listTemplates, whoAmI } from "../../../api/queries";
import { DataCheck } from "../../../components/DataCheck";
import { ButtonLink } from "../../../components/links";
import { Page } from "../../../components/Page";
import { useWorkerChoices, WorkerSelect } from "../../../components/Placement";
import { bytes } from "../../../lib/format";
import { dataTypeOf, dataTypeOfKind, dataTypes, datasetKindOf, kindLabel, type DataType } from "../../../lib/templates";
import { repeatChoices } from "../../../lib/training";
import { pathOf, zip } from "../../../lib/zip";

export const Route = createFileRoute("/_app/models/teach")({
  validateSearch: z.object({
    template: z.string().optional(),
    example: z.boolean().optional(),
    data: z.enum(["table", "text", "photos", "photo_table"]).optional(),
    // arch is the model type to teach when the examples fit it, such as
    // one written in someone's own code.
    arch: z.string().optional(),
  }),
  loader: ({ context }) => Promise.all([
    context.queryClient.ensureQueryData(listDatasets),
    context.queryClient.ensureQueryData(listArchitectures),
    context.queryClient.ensureQueryData(listTemplates),
  ]),
  component: Teach,
});

const MaxCSV = 64 * 1024 * 1024;
const MaxZip = 200 * 1024 * 1024;

// How long to teach for. Plain words up front, the numbers underneath.
const presets = [
  { value: "quick", label: "Quick look", epochs: 3, hint: "A few minutes. Good for a first try." },
  { value: "normal", label: "Normal", epochs: 10, hint: "The usual choice." },
  { value: "thorough", label: "Thorough", epochs: 30, hint: "Takes longer; squeezes out the last few points." },
];

type Source = "existing" | "upload" | "database" | "storage" | "digits" | "example";

const drivers = [
  { value: "postgres", label: "PostgreSQL", example: "postgres://user:password@db.example.com:5432/shop" },
  { value: "mysql", label: "MySQL", example: "user:password@tcp(db.example.com:3306)/shop" },
  { value: "sqlite", label: "SQLite", example: "/srv/data/shop.db" },
];
// Where the examples live in S3-compatible storage. AWS and R2 fill in the address.
const providers = [
  { value: "r2", label: "Cloudflare R2" },
  { value: "s3", label: "AWS S3" },
  { value: "other", label: "Other S3-compatible" },
];
type Bucket = { provider: string; accountId: string; region: string; endpoint: string; bucket: string; accessKeyId: string; secretAccessKey: string };
const noBucket: Bucket = { provider: "r2", accountId: "", region: "", endpoint: "", bucket: "", accessKeyId: "", secretAccessKey: "" };
const bucketFilled = (b: Bucket) => !!b.bucket.trim() && !!b.accessKeyId.trim() && !!b.secretAccessKey.trim()
  && (b.provider !== "r2" || !!b.accountId.trim()) && (b.provider !== "other" || !!b.endpoint.trim());
type Upload = { name: string; size: number; bytes: Uint8Array; columns: string[]; rows: string[][]; summary: string; zip: boolean };

// Reads the header and first rows of a CSV without a parser; enough to pick a
// column and show a preview. The server does the real checking.
function peek(text: string) {
  const lines = text.split(/\r?\n/).filter((l) => l.trim() !== "");
  const split = (l: string) => l.split(",").map((c) => c.trim().replace(/^"|"$/g, ""));
  return { columns: split(lines[0] ?? ""), rows: lines.slice(1, 6).map(split), count: Math.max(lines.length - 1, 0) };
}

function Teach() {
  const search = Route.useSearch();
  const { data: templates } = useSuspenseQuery(listTemplates);
  const tpl = templates.templates.find((t) => t.id === search.template);
  const data = (tpl?.data ?? search.data) as DataType | undefined;
  if (!data) return <PickData />;
  return <Wizard key={`${tpl?.id}-${data}`} data={data} />;
}

// PickData asks what the examples look like before anything else.
function PickData() {
  const { data: templates } = useSuspenseQuery(listTemplates);
  const [picked, setPicked] = useState<DataType>("table");
  const d = dataTypeOf(picked);
  const fits = templates.templates.filter((t) => t.data === picked);
  const label = (id: string) => templates.categories.find((c) => c.id === id)?.label ?? id;
  return (
    <Page title="Teach a new model" intro="First, what does your data look like? Pick the closest one. You can change it later.">
      <div className="grid gap-6 lg:grid-cols-[minmax(0,1fr)_340px]">
        <SimpleGrid cols={{ base: 1, sm: 2 }}>
          {dataTypes.map((t) => (
            <UnstyledButton
              key={t.id}
              onClick={() => setPicked(t.id)}
              aria-pressed={picked === t.id}
              className={`flex flex-col gap-1.5 rounded-2xl border-2 bg-[var(--mantine-color-body)] p-4 text-left ${picked === t.id ? "border-[var(--mantine-color-teal-7)] ring-4 ring-[var(--mantine-color-teal-light)]" : "border-[var(--mantine-color-default-border)]"}`}
            >
              <pre aria-hidden="true" className="m-0 overflow-hidden rounded-lg bg-[var(--mantine-color-default-hover)] p-2.5 font-mono text-xs leading-relaxed">{t.pic}</pre>
              <Text fw={700} fz="lg">{t.title}</Text>
              <Text size="sm" c="dimmed">{t.hint}</Text>
              <Text size="xs" c="dimmed">For example: {t.examples}</Text>
            </UnstyledButton>
          ))}
        </SimpleGrid>
        <Card withBorder radius="lg" padding="lg" className="self-start">
          <Text fw={700}>Start from a template for {d.title.toLowerCase()}</Text>
          <Stack gap={6} mt="sm">
            {fits.slice(0, 6).map((t) => (
              <Link key={t.id} to="/templates/$id" params={{ id: t.id }} className="block rounded-lg text-inherit no-underline border border-[var(--mantine-color-default-border)] px-3 py-2 hover:border-[var(--mantine-color-teal-6)]">
                <Text size="sm" fw={600}>{t.name}</Text>
                <Text size="xs" c="dimmed">{label(t.category)} · {kindLabel(t.kind)}</Text>
              </Link>
            ))}
          </Stack>
          <ButtonLink to="/templates" search={{ data: picked }} variant="subtle" mt="xs" px={0}>See all {fits.length} templates for this</ButtonLink>
          <Text size="sm" c="dimmed" mt="md">Nothing fits?</Text>
          <ButtonLink to="/models/teach" search={{ data: picked }} mt={6} fullWidth>{d.scratch}</ButtonLink>
        </Card>
      </div>
    </Page>
  );
}

function Wizard({ data: dataType }: { data: DataType }) {
  const search = Route.useSearch();
  const { data: sets } = useSuspenseQuery(listDatasets);
  const { data: archs } = useSuspenseQuery(listArchitectures);
  const { data: templates } = useSuspenseQuery(listTemplates);
  const { data: me } = useSuspenseQuery(whoAmI);
  const tpl = templates.templates.find((t) => t.id === search.template);
  const photos = dataType === "photos";
  const archive = dataType === "photos" || dataType === "photo_table";
  const canStore = me.permissions.includes("databases.query");
  const canConnect = canStore && !archive;
  const navigate = useNavigate();
  const { queryClient } = Route.useRouteContext();
  const fitting = sets.datasets.filter((d) => dataTypeOfKind(d.kind).includes(dataType));
  const [step, setStep] = useState(0);
  const [source, setSource] = useState<Source>(search.example && tpl ? "example" : fitting.length > 0 && !tpl ? "existing" : "upload");
  const [existing, setExisting] = useState(fitting[0]?.id ?? "");
  const [upload, setUpload] = useState<Upload>();
  const [target, setTarget] = useState("");
  const [fileError, setFileError] = useState<string>();
  const [dataset, setDataset] = useState<Dataset>();
  const [check, setCheck] = useState<Check>();
  const [arch, setArch] = useState("");
  const [name, setName] = useState(tpl?.name ?? "");
  const [description, setDescription] = useState(tpl?.question ?? "");
  const [preset, setPreset] = useState("normal");
  const [expert, { toggle }] = useDisclosure(false);
  const [epochs, setEpochs] = useState<number | string>(10);
  const [batch, setBatch] = useState<number | string>(32);
  const [rate, setRate] = useState<number | string>(0.005);
  const [holdOut, setHoldOut] = useState<number | string>(20);
  const [driver, setDriver] = useState("postgres");
  const [dsn, setDsn] = useState("");
  const [query, setQuery] = useState(tpl?.query ?? "");
  const [examplesName, setExamplesName] = useState("");
  const [bucket, setBucket] = useState<Bucket>(noBucket);
  const [bucketPath, setBucketPath] = useState("");
  const [repeat, setRepeat] = useState("0");
  const [runOn, setRunOn] = useState("");
  const folder = useRef<HTMLInputElement>(null);
  const workers = useWorkerChoices(me.permissions.includes("models.write"));
  const tested = useMutation(DatasetService.method.testQuery, {
    onSuccess: (res) => setTarget(res.columns[res.columns.length - 1] ?? ""),
  });
  const looked = useMutation(DatasetService.method.testBucket, {
    onSuccess: (res) => setTarget(res.columns[res.columns.length - 1] ?? ""),
  });
  const kindToMake = tpl ? datasetKindOf(tpl) : dataType === "photos" ? DatasetKind.PHOTOS : dataType === "photo_table" ? DatasetKind.PHOTO_TABLE : DatasetKind.TABLE;
  const editBucket = (b: Partial<Bucket>) => { setBucket((cur) => ({ ...cur, ...b })); looked.reset(); reset(); };

  const createDataset = useMutation(DatasetService.method.createDataset, {
    onSuccess: async (res) => {
      await queryClient.invalidateQueries({ queryKey: allDatasetQueries });
      if (res.dataset) chose(res.dataset, res.check);
    },
  });
  const checkDataset = useMutation(DatasetService.method.checkDataset, {
    onSuccess: async (res) => {
      await queryClient.invalidateQueries({ queryKey: allDatasetQueries });
      if (res.dataset) chose(res.dataset, res.check);
    },
  });
  const createJob = useMutation(TrainingService.method.createJob, {
    onSuccess: async (res) => {
      await queryClient.invalidateQueries({ queryKey: allTrainingQueries });
      notifications.show({ color: "teal", icon: <IconCheck size={18} />, message: "Teaching has started. You can watch it learn." });
      await navigate({ to: "/training/$id", params: { id: res.job!.id } });
    },
  });

  // Keeps the examples picked. With a check to read, it stays on this
  // step; otherwise it moves on.
  function chose(d: Dataset, c?: Check) {
    setDataset(d);
    setCheck(c);
    const allowed = archs.architectures.filter((a) => d.architectures.includes(a.name));
    setArch((cur) => {
      if (tpl && d.architectures.includes(tpl.architecture)) return tpl.architecture;
      if (search.arch && d.architectures.includes(search.arch)) return search.arch;
      return allowed.some((a) => a.name === cur) ? cur : allowed[0]?.name ?? "";
    });
    if (!name) setName(d.kind === DatasetKind.MNIST ? "My digits model" : `${d.name.replace(/\.(csv|zip)$/i, "")} model`);
    if (d.kind === DatasetKind.MNIST) { setBatch(64); setRate(0.001); }
    if (!c) setStep(1);
  }

  // Try it with the example data: made once, then reused.
  const exampleName = tpl ? `${tpl.name} (example)` : "";
  const started = useRef(false);
  useEffect(() => {
    if (source !== "example" || !tpl || started.current || dataset) return;
    started.current = true;
    const have = sets.datasets.find((d) => d.name === exampleName);
    if (have) checkDataset.mutate({ datasetId: have.id, templateId: tpl.id });
    else createDataset.mutate({ name: exampleName, templateId: tpl.id, example: true });
  });

  function reset() {
    setDataset(undefined);
    setCheck(undefined);
    createDataset.reset();
    checkDataset.reset();
  }

  async function pick(files: FileWithPath[]) {
    setFileError(undefined);
    reset();
    if (files.length === 0) return;
    if (!archive) {
      const file = files[0]!;
      if (file.size > MaxCSV) return setFileError(`That file is ${bytes(file.size)}. The limit is ${bytes(MaxCSV)}.`);
      const buf = new Uint8Array(await file.arrayBuffer());
      const p = peek(new TextDecoder().decode(buf.subarray(0, 64 * 1024)));
      if (p.columns.length < 2) return setFileError("The first row should name at least two columns, separated by commas.");
      setUpload({ name: file.name, size: file.size, bytes: buf, columns: p.columns, rows: p.rows, summary: `${bytes(file.size)} · ${p.columns.length} columns`, zip: false });
      setTarget(p.columns[p.columns.length - 1]!);
      return;
    }
    // A ZIP as it is, or a dropped folder packed into one.
    if (files.length === 1 && /\.zip$/i.test(files[0]!.name)) {
      const f = files[0]!;
      if (f.size > MaxZip) return setFileError(`That file is ${bytes(f.size)}. The limit is ${bytes(MaxZip)}.`);
      setUpload({ name: f.name, size: f.size, bytes: new Uint8Array(await f.arrayBuffer()), columns: [], rows: [], summary: bytes(f.size), zip: true });
      return;
    }
    const total = files.reduce((n, f) => n + f.size, 0);
    if (total > MaxZip) return setFileError(`Those files come to ${bytes(total)}. The limit is ${bytes(MaxZip)}.`);
    const entries = await Promise.all(files.map(async (f) => ({ path: pathOf(f), data: new Uint8Array(await f.arrayBuffer()) })));
    const folders = new Set(entries.map((e) => e.path.split("/").slice(0, -1).join("/")).filter(Boolean));
    const top = entries[0]?.path.split("/")[0] ?? "photos";
    setUpload({ name: `${top}.zip`, size: total, bytes: zip(entries), columns: [], rows: [], summary: `${files.length.toLocaleString()} files in ${folders.size.toLocaleString()} folders`, zip: true });
  }

  function next() {
    if (dataset) return setStep(1);
    const templateId = tpl?.id ?? "";
    if (source === "existing") {
      const d = sets.datasets.find((d) => d.id === existing);
      if (d && tpl) checkDataset.mutate({ datasetId: d.id, templateId });
      else if (d) chose(d);
    } else if (source === "database" && (tested.data || tpl)) {
      createDataset.mutate({ name: examplesName.trim() || (tpl ? `${tpl.name} data` : "Examples from a query"), kind: DatasetKind.TABLE, connection: { driver, dsn }, query, target: tpl ? "" : target, templateId });
    } else if (source === "storage") {
      const where = `${bucket.bucket.trim()}/${bucketPath.trim().replace(/^\/+/, "")}`.replace(/\/+$/, "");
      createDataset.mutate({ name: examplesName.trim() || where, kind: kindToMake, bucket, path: bucketPath.trim(), target: tpl || archive ? "" : target, templateId });
    } else if (source === "upload" && upload) {
      const kind = kindToMake;
      createDataset.mutate(archive
        ? { name: upload.name, kind, archive: upload.bytes, templateId }
        : { name: upload.name, kind, csv: upload.bytes, target: tpl ? "" : target, templateId });
    } else if (source === "digits") {
      const have = sets.datasets.find((d) => d.kind === DatasetKind.MNIST);
      if (have) chose(have);
      else createDataset.mutate({ name: "Handwritten digits", kind: DatasetKind.MNIST });
    } else if (source === "example" && tpl) {
      started.current = false;
      reset();
    }
  }

  // match pairs a template column with one of the dataset's, keeps it, and
  // checks again.
  function match(column: string, found: string) {
    if (!dataset || !check) return;
    const m: Record<string, string> = {};
    for (const c of check.columns) if (c.found) m[c.name] = c.found;
    m[column] = found;
    checkDataset.mutate({ datasetId: dataset.id, templateId: tpl?.id ?? "", match: m, apply: true });
  }

  const allowed = archs.architectures.filter((a) => dataset?.architectures.includes(a.name));
  const chosenArch = archs.architectures.find((a) => a.name === arch);
  const picked = !!tpl && !!dataset && dataset.architectures.includes(tpl.architecture);
  const busy = createDataset.isPending || checkDataset.isPending;
  const ready0 = !!dataset
    ? check?.verdict !== "bad"
    : source === "digits" || source === "example" || (source === "existing" && !!existing) || (source === "upload" && !!upload && (archive || !!target || !!tpl)) || (source === "database" && !!dsn.trim() && !!query.trim() && (!!tpl || (!!tested.data && !!target)))
      || (source === "storage" && bucketFilled(bucket) && (archive || !!tpl || (!!looked.data && !!target)));
  const fromQuery = !!dataset?.query || !!dataset?.storage;
  const chosenEpochs = expert ? Number(epochs) : presets.find((p) => p.value === preset)!.epochs;
  const start = () => dataset && createJob.mutate({
    name, description, architecture: arch, datasetId: dataset.id, repeatSeconds: fromQuery ? BigInt(repeat) : 0n, workerId: runOn,
    // Outside the expert settings, zero lets the server pick what suits the examples.
    settings: { epochs: chosenEpochs, batchSize: expert ? Number(batch) : 0, learningRate: expert ? Number(rate) : 0, holdOut: expert ? Number(holdOut) / 100 : 0.2 },
  });
  const steps = tpl ? ["Your data", "Name it", "How long"] : ["The examples", "What kind", "How long"];
  const dt = dataTypeOf(dataType);
  const uploadTitle = { table: "Upload a table", text: "Upload a table with text", photos: "Upload your photos", photo_table: "Upload a ZIP" }[dataType];
  const uploadHint = {
    table: "A .csv file with a header row, exported from Excel, Google Sheets or your app. One column holds the answer.",
    text: "A .csv file with a header row. Each text sits in one cell, next to its answer.",
    photos: "A ZIP file, or drop the top folder with one folder per answer inside.",
    photo_table: "The table as data.csv and a photos folder, zipped together. Or drop the folder that holds both.",
  }[dataType];

  return (
    <Page title="Teach a new model" intro={tpl ? undefined : `${dt.title}. Give it examples, pick what kind of model, and watch it learn.`}>
      {tpl ? (
        <Alert color="teal" variant="light" icon={<IconSparkles />}>
          <Group justify="space-between" gap="sm">
            <Text size="sm"><b>Using the template {tpl.name}.</b> We filled in what we could and will check your data against it.</Text>
            <ButtonLink to="/templates" variant="white" size="xs">Change template</ButtonLink>
          </Group>
        </Alert>
      ) : (
        <Group gap="xs">
          <Text size="sm" c="dimmed">Your data: <b>{dt.title}</b>.</Text>
          <ButtonLink to="/models/teach" search={{}} variant="subtle" size="compact-sm">Change</ButtonLink>
        </Group>
      )}
      <Card withBorder radius="lg" padding="xl">
        <Stepper active={step} onStepClick={(s) => s < step && setStep(s)} size="sm">
          <Stepper.Step label={steps[0]} description={tpl ? "Where it is" : "What to learn from"}>
            <Radio.Group value={source} onChange={(v) => { setSource(v as Source); reset(); }} label={tpl ? "Where is your data?" : "Where are the examples?"} mt="md">
              <Stack gap="sm" mt="sm">
                {fitting.length > 0 && (
                  <Choice value="existing" icon={<IconTable size={22} />} title="Examples already here" hint="Reuse examples you added before.">
                    {source === "existing" && (
                      <Select mt="sm" data={fitting.map((d) => ({ value: d.id, label: `${d.name} · ${d.rows.toLocaleString()} ${archiveKind(d.kind) ? "photos" : "rows"}${d.query ? " · from a database" : d.storage ? " · from storage" : ""}` }))} value={existing} onChange={(v) => { if (v) { setExisting(v); reset(); } }} allowDeselect={false} aria-label="Examples" />
                    )}
                  </Choice>
                )}
                <Choice value="upload" icon={archive ? <IconFileZip size={22} /> : <IconFileSpreadsheet size={22} />} title={uploadTitle} hint={uploadHint}>
                  {source === "upload" && (
                    <Stack mt="sm" gap="sm">
                      <Dropzone
                        onDrop={pick}
                        onReject={() => setFileError(archive ? "Pick a .zip file, or drop a folder of photos." : "Pick a single .csv file.")}
                        maxFiles={archive ? 100_000 : 1}
                        multiple={archive}
                        accept={archive ? ["application/zip", ".zip", "image/jpeg", "image/png", "image/gif", "text/csv", ".csv"] : ["text/csv", ".csv"]}
                        radius="md"
                      >
                        <Stack align="center" gap={2} py="md" className="pointer-events-none">
                          <Text fw={600}>{upload ? upload.name : archive ? "Drop your .zip file or photo folder here" : "Drop your .csv file here"}</Text>
                          <Text size="sm" c="dimmed">{upload ? upload.summary : "or click to choose one"}</Text>
                        </Stack>
                      </Dropzone>
                      {archive && (
                        <>
                          <input ref={folder} type="file" multiple hidden {...{ webkitdirectory: "" }} onChange={(e) => { const fs = Array.from(e.currentTarget.files ?? []); void pick(fs as FileWithPath[]); e.currentTarget.value = ""; }} />
                          <Button variant="default" leftSection={<IconFolder size={18} />} className="self-start" onClick={() => folder.current?.click()}>Choose a folder instead</Button>
                        </>
                      )}
                      {fileError && <Alert color="red" icon={<IconAlertCircle />}>{fileError}</Alert>}
                      {upload && !archive && (
                        <>
                          {!tpl && <Select label="Which column is the answer?" description="The model learns to predict this from the other columns." data={upload.columns} value={target} onChange={(v) => { if (v) { setTarget(v); reset(); } }} allowDeselect={false} />}
                          <Table.ScrollContainer minWidth={300}>
                            <Table fz="xs" withTableBorder>
                              <Table.Thead><Table.Tr>{upload.columns.map((c) => <Table.Th key={c} c={c === target && !tpl ? "teal" : undefined}>{c}</Table.Th>)}</Table.Tr></Table.Thead>
                              <Table.Tbody>{upload.rows.map((r, i) => <Table.Tr key={i}>{r.map((v, k) => <Table.Td key={k}>{v}</Table.Td>)}</Table.Tr>)}</Table.Tbody>
                            </Table>
                          </Table.ScrollContainer>
                        </>
                      )}
                    </Stack>
                  )}
                </Choice>
                {canConnect && (
                  <Choice value="database" icon={<IconDatabase size={22} />} title="From a database" hint={tpl ? "We start the query for you. It can keep learning as rows change." : "Run a query on PostgreSQL, MySQL or SQLite. The rows become the examples, and the model can keep learning as they change."}>
                    {source === "database" && (
                      <Stack mt="sm" gap="sm">
                        <SegmentedControl value={driver} onChange={(v) => { setDriver(v); tested.reset(); }} data={drivers.map((d) => ({ value: d.value, label: d.label }))} />
                        {driver === "sqlite" ? (
                          <TextInput label="Database file" description="The path to the file on the server, for example /srv/data/shop.db." value={dsn} onChange={(e) => { setDsn(e.currentTarget.value); tested.reset(); }} />
                        ) : <PasswordInput label="Connection" description={`For example ${drivers.find((d) => d.value === driver)!.example}. Kept on the server; never shown again.`} value={dsn} onChange={(e) => { setDsn(e.currentTarget.value); tested.reset(); }} />}
                        <Textarea label={tpl ? "Query, started from the template" : "Query"} description={tpl ? "Change the table and column names to match your database." : "One row per example: the columns to learn from, plus the answer column."} placeholder="SELECT weight_grams, height_cm, sweetness, fruit FROM harvest" autosize minRows={3} value={query} onChange={(e) => { setQuery(e.currentTarget.value); tested.reset(); reset(); }} classNames={{ input: "font-mono" }} />
                        <Group><Button variant="default" loading={tested.isPending} disabled={!dsn.trim() || !query.trim()} onClick={() => tested.mutate({ connection: { driver, dsn }, query })}>Try the query</Button></Group>
                        {tested.error && <Alert color="red" icon={<IconAlertCircle />}>{message(tested.error)}</Alert>}
                        {tested.data && (
                          <>
                            <Alert color="teal" icon={<IconCheck />}>{tested.data.rows.toLocaleString()} rows, {tested.data.columns.length} columns.</Alert>
                            {!tpl && <Select label="Which column is the answer?" description="The model learns to predict this from the other columns." data={tested.data.columns} value={target} onChange={(v) => v && setTarget(v)} allowDeselect={false} />}
                            <Table.ScrollContainer minWidth={300}>
                              <Table fz="xs" withTableBorder>
                                <Table.Thead><Table.Tr>{tested.data.columns.map((c) => <Table.Th key={c} c={c === target && !tpl ? "teal" : undefined}>{c}</Table.Th>)}</Table.Tr></Table.Thead>
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
                {canStore && (
                  <Choice value="storage" icon={<IconBucket size={22} />} title="From storage" hint={archive ? "Read a .zip, or a folder of photos, from Cloudflare R2, AWS S3 or other S3-compatible storage." : "Read a .csv, or a folder of them, from Cloudflare R2, AWS S3 or other S3-compatible storage. The model can keep learning as new files land."}>
                    {source === "storage" && (
                      <Stack mt="sm" gap="sm">
                        <SegmentedControl value={bucket.provider} onChange={(v) => editBucket({ provider: v })} data={providers} />
                        <SimpleGrid cols={{ base: 1, sm: 2 }}>
                          {bucket.provider === "r2" && <TextInput label="Account ID" description="On the R2 page of your Cloudflare dashboard." value={bucket.accountId} onChange={(e) => editBucket({ accountId: e.currentTarget.value })} />}
                          {bucket.provider === "s3" && <TextInput label="Region" placeholder="us-east-1" description="Where the bucket lives." value={bucket.region} onChange={(e) => editBucket({ region: e.currentTarget.value })} />}
                          {bucket.provider === "other" && <TextInput label="Address" placeholder="https://minio.example.com" description="The storage's S3 address." value={bucket.endpoint} onChange={(e) => editBucket({ endpoint: e.currentTarget.value })} />}
                          <TextInput label="Bucket" placeholder="training-data" value={bucket.bucket} onChange={(e) => editBucket({ bucket: e.currentTarget.value })} />
                          {bucket.provider === "other" && <TextInput label="Region" placeholder="us-east-1" description="Leave empty if the storage doesn't use one." value={bucket.region} onChange={(e) => editBucket({ region: e.currentTarget.value })} />}
                          <TextInput label="Access key ID" value={bucket.accessKeyId} onChange={(e) => editBucket({ accessKeyId: e.currentTarget.value })} autoComplete="off" />
                          <PasswordInput label="Secret access key" description="Kept on the server; never shown again. Read access is enough." value={bucket.secretAccessKey} onChange={(e) => editBucket({ secretAccessKey: e.currentTarget.value })} autoComplete="off" />
                        </SimpleGrid>
                        <TextInput
                          label="File or folder"
                          placeholder={archive ? "photos/pets/" : "exports/sales.csv"}
                          description={archive
                            ? (dataType === "photos" ? "A .zip, or a folder with one folder of photos per answer inside." : "A .zip, or a folder holding data.csv and the photos it names.")
                            : "A .csv file, or a folder of .csv files with the same header row. Empty reads the whole bucket."}
                          value={bucketPath}
                          onChange={(e) => { setBucketPath(e.currentTarget.value); looked.reset(); reset(); }}
                          classNames={{ input: "font-mono" }}
                        />
                        <Group><Button variant="default" loading={looked.isPending} disabled={!bucketFilled(bucket)} onClick={() => looked.mutate({ bucket, path: bucketPath.trim(), kind: kindToMake })}>Look in the bucket</Button></Group>
                        {looked.error && <Alert color="red" icon={<IconAlertCircle />}>{message(looked.error)}</Alert>}
                        {looked.data && (
                          <>
                            <Alert color="teal" icon={<IconCheck />}>
                              {looked.data.files === 1n ? "1 file" : `${looked.data.files.toLocaleString()} files`}, {bytes(Number(looked.data.sizeBytes))}{looked.data.columns.length > 0 && `: ${looked.data.rows.toLocaleString()} rows, ${looked.data.columns.length} columns`}.
                              {looked.data.columns.length === 0 && looked.data.names.length > 0 && <Text size="xs" c="dimmed" mt={4} className="font-mono">{looked.data.names.slice(0, 5).join(", ")}{looked.data.files > 5n ? ", …" : ""}</Text>}
                            </Alert>
                            {looked.data.columns.length > 0 && (
                              <>
                                {!tpl && <Select label="Which column is the answer?" description="The model learns to predict this from the other columns." data={looked.data.columns} value={target} onChange={(v) => v && setTarget(v)} allowDeselect={false} />}
                                <Table.ScrollContainer minWidth={300}>
                                  <Table fz="xs" withTableBorder>
                                    <Table.Thead><Table.Tr>{looked.data.columns.map((c) => <Table.Th key={c} c={c === target && !tpl ? "teal" : undefined}>{c}</Table.Th>)}</Table.Tr></Table.Thead>
                                    <Table.Tbody>{looked.data.sample.map((r, i) => <Table.Tr key={i}>{r.values.map((v, k) => <Table.Td key={k}>{v}</Table.Td>)}</Table.Tr>)}</Table.Tbody>
                                  </Table>
                                </Table.ScrollContainer>
                              </>
                            )}
                            <TextInput label="Name these examples" placeholder={`${bucket.bucket || "bucket"}/${bucketPath}`.replace(/\/+$/, "")} maxLength={100} value={examplesName} onChange={(e) => setExamplesName(e.currentTarget.value)} />
                          </>
                        )}
                      </Stack>
                    )}
                  </Choice>
                )}
                {tpl && (
                  <Choice value="example" icon={<IconSparkles size={22} />} title={photos ? "The example photos" : "The example data"} hint={`${photos ? `${tpl.folders.length * 60} photos` : tpl.data === "photo_table" ? "600 rows with photos" : "2,000 rows"}, made up. Good for a first look.`}>{null}</Choice>
                )}
                {photos && !tpl && <Choice value="digits" icon={<IconNumbers size={22} />} title="Handwritten digits" hint="70,000 pictures of the numbers 0 to 9, built in. Downloaded once on first use.">{null}</Choice>}
              </Stack>
            </Radio.Group>
            {busy && !dataset && <Text size="sm" c="dimmed" mt="md">Reading and checking your data…</Text>}
            {createDataset.error && <Alert mt="md" color="red" icon={<IconAlertCircle />}>{message(createDataset.error)}</Alert>}
            {checkDataset.error && <Alert mt="md" color="red" icon={<IconAlertCircle />}>{message(checkDataset.error)}</Alert>}
            {dataset && check && (
              <div className="mt-6">
                <DataCheck check={check} template={tpl?.id} photos={dataset.kind === DatasetKind.PHOTOS} onMatch={match} busy={checkDataset.isPending} />
              </div>
            )}
          </Stepper.Step>

          <Stepper.Step label={steps[1]} description={tpl ? "And what it's for" : "Pick the model type"}>
            {dataset && (
              <Alert mt="md" color="teal" icon={<IconCheck />}>
                {dataset.name}: {dataset.rows.toLocaleString()} examples{dataset.labels.length > 0 && !dataset.numberTarget ? `, ${dataset.labels.length} possible answers` : ""}.
              </Alert>
            )}
            {picked ? (
              <Card withBorder radius="md" mt="md" padding="md">
                <Text size="sm">The template picked the kind of model for you: <b>{chosenArch?.title}</b>. {chosenArch?.description}</Text>
              </Card>
            ) : (
              <Radio.Group value={arch} onChange={setArch} label="What kind of model?" mt="md">
                <Stack gap="sm" mt="sm">
                  {allowed.map((a) => (
                    <Radio.Card key={a.name} value={a.name} radius="md" p="md" className="data-[checked]:border-[var(--mantine-color-teal-7)]">
                      <Group wrap="nowrap" align="flex-start">
                        <Radio.Indicator />
                        <div><Group gap="xs"><Text fw={600}>{a.title}</Text>{a.output === "number" && <Badge size="sm" variant="light" color="orange">Answers a number</Badge>}{a.custom && <Badge size="sm" variant="light" color="grape">Your own code</Badge>}</Group><Text size="sm" c="dimmed">{a.description}</Text></div>
                      </Group>
                    </Radio.Card>
                  ))}
                </Stack>
              </Radio.Group>
            )}
            <Stack mt="lg">
              <TextInput label="Name" required maxLength={100} value={name} onChange={(e) => setName(e.currentTarget.value)} />
              <Textarea label="Description" description="Optional. What is it for?" maxLength={500} autosize minRows={2} value={description} onChange={(e) => setDescription(e.currentTarget.value)} />
            </Stack>
          </Stepper.Step>

          <Stepper.Step label={steps[2]} description="Then start">
            <Radio.Group value={preset} onChange={setPreset} label="How hard should it study?" mt="md">
              <SimpleGrid cols={{ base: 1, sm: 3 }} mt="sm">
                {presets.map((p) => (
                  <Radio.Card key={p.value} value={p.value} radius="md" p="md" disabled={expert} className="data-[checked]:border-[var(--mantine-color-teal-7)]">
                    <Group wrap="nowrap" align="flex-start">
                      <Radio.Indicator />
                      <div><Text fw={600}>{p.label}</Text><Text size="sm" c="dimmed">{p.epochs} passes over the examples. {tpl && p.value === "normal" ? "The template's pick." : p.hint}</Text></div>
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
            {workers.length > 0 && (
              <div className="mt-6 max-w-md">
                <WorkerSelect
                  workers={workers}
                  value={runOn}
                  onChange={setRunOn}
                  forTraining
                  label="Train on"
                  description={runOn ? "It trains there and answers there afterwards. If it's offline, the run waits for it." : "The least busy worker that can teach starts it."}
                />
              </div>
            )}
            {fromQuery && (
              <Stack gap={4} mt="lg">
                <Text fw={500} size="sm">Keep learning</Text>
                <Text size="xs" c="dimmed">{dataset?.storage ? "Read the files again on a schedule" : "Run the query again on a schedule"} and continue teaching the same model with the new rows. The model is only replaced when the new pass scores at least as well.</Text>
                <SegmentedControl value={repeat} onChange={setRepeat} data={repeatChoices} className="self-start" />
              </Stack>
            )}
            <Text size="sm" c="dimmed" mt="md">
              {name || "The model"} will learn from {dataset?.rows.toLocaleString()} examples over {chosenEpochs} passes{fromQuery && repeat !== "0" && `, then keep learning ${repeatChoices.find((c) => c.value === repeat)!.label.toLowerCase()}`}. When it&apos;s done you can try it with one {archive ? "photo" : "row"}, or send it new ones from your apps. You can stop it any time and pick up later.
            </Text>
            {createJob.error && <Alert mt="md" color="red" icon={<IconAlertCircle />}>{message(createJob.error)}</Alert>}
          </Stepper.Step>
        </Stepper>

        <Group justify="space-between" mt="xl">
          {step === 0 && tpl
            ? <ButtonLink variant="default" to="/templates/$id" params={{ id: tpl.id }}>Back</ButtonLink>
            : <Button variant="default" onClick={() => setStep(step - 1)} disabled={step === 0 || createJob.isPending}>Back</Button>}
          {step === 0 && (
            <Button onClick={next} loading={busy} disabled={!ready0}>
              {dataset ? (check?.verdict === "bad" ? "Fix the data first" : "Continue") : source === "existing" && !tpl ? "Next" : "Check my data"}
            </Button>
          )}
          {step === 1 && <Button onClick={() => setStep(2)} disabled={!arch || !name.trim()}>Continue</Button>}
          {step === 2 && <Button loading={createJob.isPending} disabled={!dataset || !arch || !name.trim()} onClick={start}>Start teaching</Button>}
        </Group>
      </Card>
      {!tpl && (
        <Text size="sm" c="dimmed">
          Not sure how to shape your data? <Link to="/templates" search={dataType ? { data: dataType } : {}}>Start from a template</Link>.
        </Text>
      )}
    </Page>
  );
}

function archiveKind(k: DatasetKind) {
  return k === DatasetKind.PHOTOS || k === DatasetKind.PHOTO_TABLE || k === DatasetKind.MNIST;
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
