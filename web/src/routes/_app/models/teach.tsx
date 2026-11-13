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
import { allDatasetQueries, allTrainingQueries, listArchitectures, listDatasets, listModels, listTemplates, whoAmI } from "../../../api/queries";
import { ColumnPicker } from "../../../components/ColumnPicker";
import { DataCheck } from "../../../components/DataCheck";
import { ModelShape } from "../../../components/ModelShape";
import { ButtonLink } from "../../../components/links";
import { Page } from "../../../components/Page";
import { useWorkerChoices, WorkerSelect } from "../../../components/Placement";
import { answer, guess, parse, profile, type Role } from "../../../lib/csv";
import { bytes } from "../../../lib/format";
import { dataTypeOf, dataTypeOfKind, dataTypes, datasetKindOf, kindLabel, type DataType } from "../../../lib/templates";
import { layersOf, type Size, type Step } from "../../../lib/shape";
import { repeatChoices, repeatChoicesFor } from "../../../lib/training";
import { pathOf, zip } from "../../../lib/zip";
import { t } from "../../../i18n";

export const Route = createFileRoute("/_app/models/teach")({
  validateSearch: z.object({
    template: z.string().optional(),
    example: z.boolean().optional(),
    data: z.enum(["table", "text", "photos", "photo_table", "sounds", "boxes"]).optional(),
    // arch is the model type to teach when the examples fit it, such as
    // one written in someone's own code.
    arch: z.string().optional(),
  }),
  loader: ({ context }) => Promise.all([
    context.queryClient.ensureQueryData(listDatasets),
    context.queryClient.ensureQueryData(listArchitectures),
    context.queryClient.ensureQueryData(listTemplates),
    context.queryClient.ensureQueryData(listModels),
  ]),
  component: Teach,
});

const MaxCSV = 64 * 1024 * 1024;
const MaxZip = 200 * 1024 * 1024;

// How long to teach for. Plain words up front, the numbers underneath.
const presets = [
  { value: "quick", label: t("Quick look"), epochs: 3, hint: t("A few minutes. Good for a first try.") },
  { value: "normal", label: t("Normal"), epochs: 10, hint: t("The usual choice.") },
  { value: "thorough", label: t("Thorough"), epochs: 30, hint: t("Takes longer; squeezes out the last few points.") },
];

type Source = "existing" | "upload" | "database" | "storage" | "digits" | "example";

const drivers = [
  { value: "postgres", label: "PostgreSQL", example: "postgres://user:password@db.example.com:5432/shop" },
  { value: "mysql", label: "MySQL", example: "user:password@tcp(db.example.com:3306)/shop" },
  { value: "sqlite", label: "SQLite", example: "/srv/data/shop.db" },
];
// Where the examples live in S3-compatible storage. AWS and R2 fill in the address.
const providers = [
  { value: "r2", label: t("Cloudflare R2") },
  { value: "s3", label: t("AWS S3") },
  { value: "other", label: t("Other S3-compatible") },
];
type Bucket = { provider: string; accountId: string; region: string; endpoint: string; bucket: string; accessKeyId: string; secretAccessKey: string };
const noBucket: Bucket = { provider: "r2", accountId: "", region: "", endpoint: "", bucket: "", accessKeyId: "", secretAccessKey: "" };
const bucketFilled = (b: Bucket) => !!b.bucket.trim() && !!b.accessKeyId.trim() && !!b.secretAccessKey.trim()
  && (b.provider !== "r2" || !!b.accountId.trim()) && (b.provider !== "other" || !!b.endpoint.trim());
// rows are the first rows of a table, enough to say what each column
// holds; total is how many the file has when all of it was read.
type Upload = { name: string; size: number; bytes: Uint8Array; columns: string[]; rows: string[][]; total?: number; summary: string; zip: boolean };

// How much of a table is read in the browser to look at its columns.
const PeekBytes = 512 * 1024;
const PeekRows = 2000;

// The batch size the server picks for a table of rows examples when it is
// left unset (training.SettingsFor): larger for large tables.
function tableBatch(rows: number) {
  let b = 32;
  while (b < 512 && b * 2 * 1000 <= rows) b *= 2;
  return b;
}

function Teach() {
  const search = Route.useSearch();
  const { data: templates } = useSuspenseQuery(listTemplates);
  const tpl = templates.templates.find((tm) => tm.id === search.template);
  const data = (tpl?.data ?? search.data) as DataType | undefined;
  if (!data) return <PickData />;
  return <Wizard key={`${tpl?.id}-${data}`} data={data} />;
}

// PickData asks what the examples look like before anything else.
function PickData() {
  const { data: templates } = useSuspenseQuery(listTemplates);
  const [picked, setPicked] = useState<DataType>("table");
  const d = dataTypeOf(picked);
  const fits = templates.templates.filter((tm) => tm.data === picked);
  const label = (id: string) => templates.categories.find((c) => c.id === id)?.label ?? id;
  return (
    <Page title={t("Teach a new model")} crumbs={[{ label: t("Build a model"), to: "/models/build" }, { label: t("Teach") }]} intro={t("First, what does your data look like? Pick the closest one. You can change it later.")}>
      <div className="grid gap-6 lg:grid-cols-[minmax(0,1fr)_340px]">
        <SimpleGrid cols={{ base: 1, sm: 2 }}>
          {dataTypes.map((kind) => (
            <UnstyledButton
              key={kind.id}
              onClick={() => setPicked(kind.id)}
              aria-pressed={picked === kind.id}
              className={`flex flex-col gap-1.5 rounded-2xl border-2 bg-[var(--mantine-color-body)] p-4 text-left ${picked === kind.id ? "border-[var(--mantine-color-teal-7)] ring-4 ring-[var(--mantine-color-teal-light)]" : "border-[var(--mantine-color-default-border)]"}`}
            >
              <pre aria-hidden="true" className="m-0 overflow-hidden rounded-lg bg-[var(--mantine-color-default-hover)] p-2.5 font-mono text-xs leading-relaxed">{kind.pic}</pre>
              <Text fw={700} fz="lg">{kind.title}</Text>
              <Text size="sm" c="dimmed">{kind.hint}</Text>
              <Text size="xs" c="dimmed">{t("For example:")}{" "}{kind.examples}</Text>
            </UnstyledButton>
          ))}
        </SimpleGrid>
        <Card withBorder radius="lg" padding="lg" className="self-start">
          <Text fw={700}>{t("Start from a template for")}{" "}{d.title.toLowerCase()}</Text>
          <Stack gap={6} mt="sm">
            {fits.slice(0, 6).map((tm) => (
              <Link key={tm.id} to="/templates/$id" params={{ id: tm.id }} className="block rounded-lg text-inherit no-underline border border-[var(--mantine-color-default-border)] px-3 py-2 hover:border-[var(--mantine-color-teal-6)]">
                <Text size="sm" fw={600}>{tm.name}</Text>
                <Text size="xs" c="dimmed">{label(tm.category)} · {kindLabel(tm.kind)}</Text>
              </Link>
            ))}
          </Stack>
          <ButtonLink to="/templates" search={{ data: picked }} variant="subtle" mt="xs" px={0}>{t("See all")}{" "}{fits.length}{" "}{t("templates for this")}</ButtonLink>
          <Text size="sm" c="dimmed" mt="md">{t("Nothing fits?")}</Text>
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
  const { data: models } = useSuspenseQuery(listModels);
  const tpl = templates.templates.find((tm) => tm.id === search.template);
  // A model type asked for up front, such as Find groups, may read the
  // answer column as something else, or need none.
  const presetArch = archs.architectures.find((a) => a.name === search.arch);
  const role = (presetArch?.target ?? "") as Role;
  const photos = dataType === "photos";
  const archive = dataType === "photos" || dataType === "photo_table" || dataType === "sounds" || dataType === "boxes";
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
  const [startFrom, setStartFrom] = useState("");
  const [size, setSize] = useState<Size>("medium");
  const [shapeSteps, setShapeSteps] = useState<Step[]>([]);
  const [name, setName] = useState(tpl?.name ?? "");
  const [description, setDescription] = useState(tpl?.question ?? "");
  const [preset, setPreset] = useState("normal");
  const [expert, { toggle }] = useDisclosure(false);
  // The other kinds of model wait behind a button, so the step is one
  // clear choice.
  const [moreKinds, { toggle: toggleKinds }] = useDisclosure(false);
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
  const [minRows, setMinRows] = useState<number | string>(0);
  const waitRows = Math.max(0, Math.floor(Number(minRows) || 0));
  const [runOn, setRunOn] = useState("");
  const folder = useRef<HTMLInputElement>(null);
  const workers = useWorkerChoices(me.permissions.includes("models.write"));
  const tested = useMutation(DatasetService.method.testQuery, {
    onSuccess: (res) => setTarget(guessTarget(res.columns, res.sample.map((r) => r.values), role)),
  });
  const looked = useMutation(DatasetService.method.testBucket, {
    onSuccess: (res) => setTarget(guessTarget(res.columns, res.sample.map((r) => r.values), role)),
  });
  const kindToMake = tpl ? datasetKindOf(tpl) : dataType === "photos" ? DatasetKind.PHOTOS : dataType === "photo_table" ? DatasetKind.PHOTO_TABLE : dataType === "sounds" ? DatasetKind.SOUNDS : dataType === "boxes" ? DatasetKind.BOXES : DatasetKind.TABLE;
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
      notifications.show({ color: "teal", icon: <IconCheck size={18} />, message: t("Teaching has started. You can watch it learn.") });
      await navigate({ to: "/training/$id", params: { id: res.job!.id } });
    },
  });

  // Keeps the examples picked. With a check to read, it stays on this
  // step; otherwise it moves on.
  function chose(d: Dataset, c?: Check) {
    setDataset(d);
    setCheck(c);
    const allowed = archs.architectures.filter((a) => d.architectures.includes(a.name) && (role ? a.name === search.arch : !a.target));
    setArch((cur) => {
      if (tpl && d.architectures.includes(tpl.architecture)) return tpl.architecture;
      if (search.arch && d.architectures.includes(search.arch)) return search.arch;
      if (allowed.some((a) => a.name === cur)) return cur;
      // Trying several and keeping the best is the safest first choice.
      return (allowed.find((a) => a.name.endsWith("-auto")) ?? allowed[0])?.name ?? "";
    });
    if (!name) setName(d.kind === DatasetKind.MNIST ? t("My digits model") : t("{n1} model", { n1: d.name.replace(/\.(csv|zip)$/i, "") }));
    if (d.kind === DatasetKind.MNIST) { setBatch(64); setRate(0.001); }
    if (d.kind === DatasetKind.TABLE) setBatch(tableBatch(Number(d.rows)));
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
      if (file.size > MaxCSV) return setFileError(t("That file is {size}. The limit is {MaxCSV}.", { size: bytes(file.size), MaxCSV: bytes(MaxCSV) }));
      const buf = new Uint8Array(await file.arrayBuffer());
      const whole = buf.length <= PeekBytes;
      const p = parse(new TextDecoder().decode(buf.subarray(0, PeekBytes)), { maxRows: PeekRows, cut: !whole });
      if (p.columns.length < 2) return setFileError(t("The first row should name at least two columns, separated by commas."));
      if (p.rows.length === 0) return setFileError(t("There are no rows under the header. Each row is one example."));
      setUpload({ name: file.name, size: file.size, bytes: buf, columns: p.columns, rows: p.rows, total: whole ? p.rows.length : undefined, summary: t("{size} · {length} columns{n3}", { size: bytes(file.size), length: p.columns.length, n3: whole ? t(" · {n1} rows", { n1: p.rows.length.toLocaleString() }) : "" }), zip: false });
      setTarget(guessTarget(p.columns, p.rows, role));
      return;
    }
    // A ZIP as it is, or a dropped folder packed into one.
    if (files.length === 1 && /\.zip$/i.test(files[0]!.name)) {
      const f = files[0]!;
      if (f.size > MaxZip) return setFileError(t("That file is {size}. The limit is {MaxZip}.", { size: bytes(f.size), MaxZip: bytes(MaxZip) }));
      setUpload({ name: f.name, size: f.size, bytes: new Uint8Array(await f.arrayBuffer()), columns: [], rows: [], summary: bytes(f.size), zip: true });
      return;
    }
    const total = files.reduce((n, f) => n + f.size, 0);
    if (total > MaxZip) return setFileError(t("Those files come to {total}. The limit is {MaxZip}.", { total: bytes(total), MaxZip: bytes(MaxZip) }));
    const entries = await Promise.all(files.map(async (f) => ({ path: pathOf(f), data: new Uint8Array(await f.arrayBuffer()) })));
    const folders = new Set(entries.map((e) => e.path.split("/").slice(0, -1).join("/")).filter(Boolean));
    const top = entries[0]?.path.split("/")[0] ?? "photos";
    setUpload({ name: `${top}.zip`, size: total, bytes: zip(entries), columns: [], rows: [], summary: t("{n1} files in {n2} folders", { n1: files.length.toLocaleString(), n2: folders.size.toLocaleString() }), zip: true });
  }

  function next() {
    if (dataset) return setStep(1);
    const templateId = tpl?.id ?? "";
    if (source === "existing") {
      const d = sets.datasets.find((d) => d.id === existing);
      if (d && tpl) checkDataset.mutate({ datasetId: d.id, templateId });
      else if (d) chose(d);
    } else if (source === "database" && (tested.data || tpl)) {
      createDataset.mutate({ name: examplesName.trim() || (tpl ? t("{name} data", { name: tpl.name }) : t("Examples from a query")), kind: DatasetKind.TABLE, connection: { driver, dsn }, query, target: tpl ? "" : target, templateId });
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
      else createDataset.mutate({ name: t("Handwritten digits"), kind: DatasetKind.MNIST });
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

  // Models that read the answer column as something else are offered
  // only when asked for.
  const allowed = archs.architectures.filter((a) => dataset?.architectures.includes(a.name) && (role ? a.name === search.arch : !a.target));
  // The kind to suggest, first in the list: trying several and keeping
  // the best, else what the server offers first.
  const suggested = (allowed.find((a) => a.name.endsWith("-auto")) ?? allowed[0])?.name ?? "";
  const ordered = [...allowed].sort((a, b) => Number(b.name === suggested) - Number(a.name === suggested));
  const why = (a: typeof allowed[number]) => a.name !== suggested ? undefined
    : a.name.endsWith("-auto") ? t("Tries several kinds on your rows and keeps the one that scores best. The safe choice when you're not sure; it takes a little longer.")
    : allowed.length > 1 ? t("The usual choice for this kind of data.") : undefined;
  const chosenArch = archs.architectures.find((a) => a.name === arch);
  const earlier = models.models.filter((m) => m.architecture === arch);
  const picked = !!tpl && !!dataset && dataset.architectures.includes(tpl.architecture);
  const busy = createDataset.isPending || checkDataset.isPending;
  const ready0 = !!dataset
    ? check?.verdict !== "bad"
    : source === "digits" || source === "example" || (source === "existing" && !!existing) || (source === "upload" && !!upload && (archive || !!tpl || canAnswer(upload.columns, upload.rows, target, role))) || (source === "database" && !!dsn.trim() && !!query.trim() && (!!tpl || (!!tested.data && canAnswer(tested.data.columns, tested.data.sample.map((r) => r.values), target, role))))
      || (source === "storage" && bucketFilled(bucket) && (archive || !!tpl || (!!looked.data && canAnswer(looked.data.columns, looked.data.sample.map((r) => r.values), target, role))));
  const fromQuery = !!dataset?.query || !!dataset?.storage;
  const inputs = Number((dataset?.inputShape ?? []).reduce((n, d) => n * d, 1n));
  const answers = dataset?.numberTarget
    ? { what: t("a number"), count: 1 }
    : { what: dataset?.labels.length === 2 ? dataset.labels.join(t(" or ")) : t("one of {n1} answers", { n1: dataset?.labels.length ?? 0 }), count: dataset?.labels.length ?? 2 };
  const chosenEpochs = expert ? Number(epochs) : presets.find((p) => p.value === preset)!.epochs;
  const start = () => dataset && createJob.mutate({
    name, description, architecture: arch, datasetId: dataset.id, repeatSeconds: fromQuery ? BigInt(repeat) : 0n, minNewRows: fromQuery && repeat !== "0" ? BigInt(waitRows) : 0n, workerId: runOn, startFromModelId: earlier.some((m) => m.id === startFrom) ? startFrom : "",
    // Outside the expert settings, zero lets the server pick what suits the examples.
    settings: {
      epochs: chosenEpochs, batchSize: expert ? Number(batch) : 0, learningRate: expert ? Number(rate) : 0, holdOut: expert ? Number(holdOut) / 100 : 0.2,
      layers: chosenArch?.designable ? layersOf(size, shapeSteps, inputs) : [],
    },
  });
  const steps = tpl ? [t("Your data"), t("Name it"), t("How long")] : [t("The examples"), t("What kind"), t("How long")];
  const dt = dataTypeOf(dataType);
  const uploadTitle = { table: t("Upload a table"), text: t("Upload a table with text"), photos: t("Upload your photos"), photo_table: t("Upload a ZIP"), sounds: t("Upload your sounds"), boxes: t("Upload your photos and boxes") }[dataType];
  const uploadHint = {
    table: t("A .csv file with a header row, exported from Excel, Google Sheets or your app. One column holds the answer."),
    text: t("A .csv file with a header row. Each text sits in one cell, next to its answer."),
    photos: t("A ZIP file, or drop the top folder with one folder per answer inside."),
    photo_table: t("The table as data.csv and a photos folder, zipped together. Or drop the folder that holds both."),
    sounds: t("A ZIP file of WAV recordings, or drop the top folder with one folder per answer inside."),
    boxes: t("The photos and a boxes.csv (photo, what, left, top, width, height), zipped together. Or drop the folder that holds both."),
  }[dataType];

  return (
    <Page title={t("Teach a new model")} crumbs={[{ label: t("Build a model"), to: "/models/build" }, { label: t("Teach") }]} intro={tpl ? undefined : t("{title}. Give it examples, pick what kind of model, and watch it learn.", { title: dt.title })}>
      {tpl ? (
        <Alert color="teal" variant="light" icon={<IconSparkles />}>
          <Group justify="space-between" gap="sm">
            <Text size="sm"><b>{t("Using the template")}{" "}{tpl.name}.</b>{" "}{t("We filled in what we could and will check your data against it.")}</Text>
            <ButtonLink to="/templates" variant="white" size="xs">{t("Change template")}</ButtonLink>
          </Group>
        </Alert>
      ) : (
        <Group gap="xs">
          <Text size="sm" c="dimmed">{t("Your data:")}{" "}<b>{dt.title}</b>.</Text>
          <ButtonLink to="/models/teach" search={{}} variant="subtle" size="compact-sm">{t("Change")}</ButtonLink>
        </Group>
      )}
      <Card withBorder radius="lg" padding="xl">
        <Stepper active={step} onStepClick={(s) => s < step && setStep(s)} size="sm">
          <Stepper.Step label={steps[0]} description={tpl ? t("Where it is") : t("What to learn from")}>
            <Radio.Group value={source} onChange={(v) => { setSource(v as Source); reset(); }} label={tpl ? t("Where is your data?") : t("Where are the examples?")} mt="md">
              <Stack gap="sm" mt="sm">
                {fitting.length > 0 && (
                  <Choice value="existing" icon={<IconTable size={22} />} title={t("Examples already here")} hint={t("Reuse examples you added before.")}>
                    {source === "existing" && (
                      <Select mt="sm" data={fitting.map((d) => ({ value: d.id, label: `${d.name} · ${d.rows.toLocaleString()} ${d.kind === DatasetKind.SOUNDS ? t("sounds") : archiveKind(d.kind) ? t("photos") : t("rows")}${d.query ? t(" · from a database") : d.storage ? t(" · from storage") : ""}` }))} value={existing} onChange={(v) => { if (v) { setExisting(v); reset(); } }} allowDeselect={false} aria-label={t("Examples")} />
                    )}
                  </Choice>
                )}
                <Choice value="upload" icon={archive ? <IconFileZip size={22} /> : <IconFileSpreadsheet size={22} />} title={uploadTitle} hint={uploadHint}>
                  {source === "upload" && (
                    <Stack mt="sm" gap="sm">
                      <Dropzone
                        onDrop={pick}
                        onReject={() => setFileError(archive ? t("Pick a .zip file, or drop a folder of photos.") : t("Pick a single .csv file."))}
                        maxFiles={archive ? 100_000 : 1}
                        multiple={archive}
                        accept={dataType === "sounds" ? ["application/zip", ".zip", "audio/wav", "audio/x-wav", ".wav"] : archive ? ["application/zip", ".zip", "image/jpeg", "image/png", "image/gif", "text/csv", ".csv"] : ["text/csv", ".csv"]}
                        radius="md"
                      >
                        <Stack align="center" gap={2} py="md" className="pointer-events-none">
                          <Text fw={600}>{upload ? upload.name : archive ? (dataType === "sounds" ? t("Drop your .zip file or sound folder here") : t("Drop your .zip file or photo folder here")) : t("Drop your .csv file here")}</Text>
                          <Text size="sm" c="dimmed">{upload ? upload.summary : t("or click to choose one")}</Text>
                        </Stack>
                      </Dropzone>
                      {archive && (
                        <>
                          <input ref={folder} type="file" multiple hidden {...{ webkitdirectory: "" }} onChange={(e) => { const fs = Array.from(e.currentTarget.files ?? []); void pick(fs as FileWithPath[]); e.currentTarget.value = ""; }} />
                          <Button variant="default" leftSection={<IconFolder size={18} />} className="self-start" onClick={() => folder.current?.click()}>{t("Choose a folder instead")}</Button>
                        </>
                      )}
                      {fileError && <Alert color="red" icon={<IconAlertCircle />}>{fileError}</Alert>}
                      {upload && !archive && (tpl
                        ? <Preview columns={upload.columns} rows={upload.rows.slice(0, 5)} />
                        : <ColumnPicker columns={upload.columns} rows={upload.rows} total={upload.total} role={role} value={target} onChange={(v) => { setTarget(v); reset(); }} />)}
                    </Stack>
                  )}
                </Choice>
                {canConnect && (
                  <Choice value="database" icon={<IconDatabase size={22} />} title={t("From a database")} hint={tpl ? t("We start the query for you. It can keep learning as rows change.") : t("Run a query on PostgreSQL, MySQL or SQLite. The rows become the examples, and the model can keep learning as they change.")}>
                    {source === "database" && (
                      <Stack mt="sm" gap="sm">
                        <SegmentedControl value={driver} onChange={(v) => { setDriver(v); tested.reset(); }} data={drivers.map((d) => ({ value: d.value, label: d.label }))} />
                        {driver === "sqlite" ? (
                          <TextInput label={t("Database file")} description={t("The path to the file on the server, for example /srv/data/shop.db.")} value={dsn} onChange={(e) => { setDsn(e.currentTarget.value); tested.reset(); }} />
                        ) : <PasswordInput label={t("Connection")} description={t("For example {example}. Kept on the server; never shown again.", { example: drivers.find((d) => d.value === driver)!.example })} value={dsn} onChange={(e) => { setDsn(e.currentTarget.value); tested.reset(); }} />}
                        <Textarea label={tpl ? t("Query, started from the template") : t("Query")} description={tpl ? t("Change the table and column names to match your database.") : t("One row per example: the columns to learn from, plus the answer column.")} placeholder="SELECT weight_grams, height_cm, sweetness, fruit FROM harvest" autosize minRows={3} value={query} onChange={(e) => { setQuery(e.currentTarget.value); tested.reset(); reset(); }} classNames={{ input: "font-mono" }} />
                        <Group><Button variant="default" loading={tested.isPending} disabled={!dsn.trim() || !query.trim()} onClick={() => tested.mutate({ connection: { driver, dsn }, query })}>{t("Try the query")}</Button></Group>
                        {tested.error && <Alert color="red" icon={<IconAlertCircle />}>{message(tested.error)}</Alert>}
                        {tested.data && (
                          <>
                            <Alert color="teal" icon={<IconCheck />}>{tested.data.rows.toLocaleString()}{" "}{t("rows,")}{" "}{tested.data.columns.length}{" "}{t("columns.")}</Alert>
                            {tpl
                              ? <Preview columns={tested.data.columns} rows={tested.data.sample.map((r) => r.values)} />
                              : <ColumnPicker columns={tested.data.columns} rows={tested.data.sample.map((r) => r.values)} total={Number(tested.data.rows)} role={role} value={target} onChange={setTarget} />}
                            <TextInput label={t("Name these examples")} placeholder={t("Harvest from the shop database")} maxLength={100} value={examplesName} onChange={(e) => setExamplesName(e.currentTarget.value)} />
                          </>
                        )}
                      </Stack>
                    )}
                  </Choice>
                )}
                {canStore && (
                  <Choice value="storage" icon={<IconBucket size={22} />} title={t("From storage")} hint={archive ? (dataType === "sounds" ? t("Read a .zip, or a folder of sounds, from Cloudflare R2, AWS S3 or other S3-compatible storage.") : t("Read a .zip, or a folder of photos, from Cloudflare R2, AWS S3 or other S3-compatible storage.")) : t("Read a .csv, or a folder of them, from Cloudflare R2, AWS S3 or other S3-compatible storage. The model can keep learning as new files land.")}>
                    {source === "storage" && (
                      <Stack mt="sm" gap="sm">
                        <SegmentedControl value={bucket.provider} onChange={(v) => editBucket({ provider: v })} data={providers} />
                        <SimpleGrid cols={{ base: 1, sm: 2 }}>
                          {bucket.provider === "r2" && <TextInput label={t("Account ID")} description={t("On the R2 page of your Cloudflare dashboard.")} value={bucket.accountId} onChange={(e) => editBucket({ accountId: e.currentTarget.value })} />}
                          {bucket.provider === "s3" && <TextInput label={t("Region")} placeholder="us-east-1" description={t("Where the bucket lives.")} value={bucket.region} onChange={(e) => editBucket({ region: e.currentTarget.value })} />}
                          {bucket.provider === "other" && <TextInput label={t("Address")} placeholder="https://minio.example.com" description={t("The storage's S3 address.")} value={bucket.endpoint} onChange={(e) => editBucket({ endpoint: e.currentTarget.value })} />}
                          <TextInput label={t("Bucket")} placeholder="training-data" value={bucket.bucket} onChange={(e) => editBucket({ bucket: e.currentTarget.value })} />
                          {bucket.provider === "other" && <TextInput label={t("Region")} placeholder="us-east-1" description={t("Leave empty if the storage doesn't use one.")} value={bucket.region} onChange={(e) => editBucket({ region: e.currentTarget.value })} />}
                          <TextInput label={t("Access key ID")} value={bucket.accessKeyId} onChange={(e) => editBucket({ accessKeyId: e.currentTarget.value })} autoComplete="off" />
                          <PasswordInput label={t("Secret access key")} description={t("Kept on the server; never shown again. Read access is enough.")} value={bucket.secretAccessKey} onChange={(e) => editBucket({ secretAccessKey: e.currentTarget.value })} autoComplete="off" />
                        </SimpleGrid>
                        <TextInput
                          label={t("File or folder")}
                          placeholder={dataType === "sounds" ? "sounds/machines/" : archive ? "photos/pets/" : "exports/sales.csv"}
                          description={archive
                            ? (dataType === "photos" || dataType === "sounds" ? t("A .zip, or a folder with one folder of {dataType} per answer inside.", { dataType }) : dataType === "boxes" ? t("A .zip, or a folder holding boxes.csv and the photos it names.") : t("A .zip, or a folder holding data.csv and the photos it names."))
                            : t("A .csv file, or a folder of .csv files with the same header row. Empty reads the whole bucket.")}
                          value={bucketPath}
                          onChange={(e) => { setBucketPath(e.currentTarget.value); looked.reset(); reset(); }}
                          classNames={{ input: "font-mono" }}
                        />
                        <Group><Button variant="default" loading={looked.isPending} disabled={!bucketFilled(bucket)} onClick={() => looked.mutate({ bucket, path: bucketPath.trim(), kind: kindToMake })}>{t("Look in the bucket")}</Button></Group>
                        {looked.error && <Alert color="red" icon={<IconAlertCircle />}>{message(looked.error)}</Alert>}
                        {looked.data && (
                          <>
                            <Alert color="teal" icon={<IconCheck />}>
                              {looked.data.files === 1n ? t("1 file") : t("{n1} files", { n1: looked.data.files.toLocaleString() })}, {bytes(Number(looked.data.sizeBytes))}{looked.data.columns.length > 0 && t(": {rows} rows, {columns} columns", { rows: looked.data.rows.toLocaleString(), columns: looked.data.columns.length })}.
                              {looked.data.columns.length === 0 && looked.data.names.length > 0 && <Text size="xs" c="dimmed" mt={4} className="font-mono">{looked.data.names.slice(0, 5).join(", ")}{looked.data.files > 5n ? ", …" : ""}</Text>}
                            </Alert>
                            {looked.data.columns.length > 0 && (
                              <>
                                {tpl
                                  ? <Preview columns={looked.data.columns} rows={looked.data.sample.map((r) => r.values)} />
                                  : <ColumnPicker columns={looked.data.columns} rows={looked.data.sample.map((r) => r.values)} total={Number(looked.data.rows)} role={role} value={target} onChange={setTarget} />}
                              </>
                            )}
                            <TextInput label={t("Name these examples")} placeholder={`${bucket.bucket || "bucket"}/${bucketPath}`.replace(/\/+$/, "")} maxLength={100} value={examplesName} onChange={(e) => setExamplesName(e.currentTarget.value)} />
                          </>
                        )}
                      </Stack>
                    )}
                  </Choice>
                )}
                {tpl && (
                  <Choice value="example" icon={<IconSparkles size={22} />} title={photos ? t("The example photos") : t("The example data")} hint={t("{n1}, made up. Good for a first look.", { n1: photos ? t("{n1} photos", { n1: tpl.folders.length * 60 }) : tpl.data === "photo_table" ? t("600 rows with photos") : t("2,000 rows") })}>{null}</Choice>
                )}
                {photos && !tpl && <Choice value="digits" icon={<IconNumbers size={22} />} title={t("Handwritten digits")} hint={t("70,000 pictures of the numbers 0 to 9, built in. Downloaded once on first use.")}>{null}</Choice>}
              </Stack>
            </Radio.Group>
            {busy && !dataset && <Text size="sm" c="dimmed" mt="md">{t("Reading and checking your data…")}</Text>}
            {createDataset.error && <Alert mt="md" color="red" icon={<IconAlertCircle />}>{message(createDataset.error)}</Alert>}
            {checkDataset.error && <Alert mt="md" color="red" icon={<IconAlertCircle />}>{message(checkDataset.error)}</Alert>}
            {dataset && check && (
              <div className="mt-6">
                <DataCheck check={check} template={tpl?.id} photos={dataset.kind === DatasetKind.PHOTOS} sounds={dataset.kind === DatasetKind.SOUNDS} onMatch={match} busy={checkDataset.isPending} />
              </div>
            )}
          </Stepper.Step>

          <Stepper.Step label={steps[1]} description={tpl ? t("And what it's for") : t("Pick the model type")}>
            {dataset && (
              <Alert mt="md" color="teal" icon={<IconCheck />}>
                {dataset.name}: {dataset.rows.toLocaleString()} {role ? t("rows") : t("examples")}{dataset.labels.length > 0 && !dataset.numberTarget && !role ? t(", {length} possible answers", { length: dataset.labels.length }) : ""}.
              </Alert>
            )}
            {picked ? (
              <Card withBorder radius="md" mt="md" padding="md">
                <Text size="sm">{t("The template picked the kind of model for you:")}{" "}<b>{chosenArch?.title}</b>. {chosenArch?.description}</Text>
              </Card>
            ) : (
              <Radio.Group value={arch} onChange={setArch} label={t("What kind of model?")} description={allowed.length > 1 ? t("The recommended one suits most tables. The others are for when you know what you want, or want to see why it answers as it does.") : undefined} mt="md">
                <Stack gap="sm" mt="sm">
                  {ordered.filter((a) => moreKinds || allowed.length <= 2 || a.name === suggested || a.name === arch).map((a) => (
                    <Radio.Card key={a.name} value={a.name} radius="md" p="md">
                      <Group wrap="nowrap" align="flex-start">
                        <Radio.Indicator />
                        <div>
                          <Group gap="xs"><Text fw={600}>{a.title}</Text>{a.name === suggested && allowed.length > 1 && <Badge size="sm">{t("Recommended")}</Badge>}{a.output === "number" && <Badge size="sm" variant="light" color="orange">{t("Answers a number")}</Badge>}{a.custom && <Badge size="sm" variant="light" color="grape">{t("Your own code")}</Badge>}</Group>
                          {why(a) && <Text size="sm" fw={500} c="teal.8">{why(a)}</Text>}
                          <Text size="sm" c="dimmed">{a.description}</Text>
                        </div>
                      </Group>
                    </Radio.Card>
                  ))}
                  {allowed.length > 2 && (
                    <Button variant="subtle" size="compact-sm" className="self-start" onClick={toggleKinds}>
                      {moreKinds ? t("Show fewer kinds") : t("Choose another kind ({n1} more)", { n1: allowed.length - 1 })}
                    </Button>
                  )}
                </Stack>
              </Radio.Group>
            )}
            {chosenArch && !chosenArch.learned && earlier.length > 0 && (
              <Select
                mt="md"
                label={t("Start from an earlier model")}
                description={t("It keeps what that model learned to see and learns these answers on top, so it needs fewer examples and less time.")}
                data={[{ value: "", label: t("No, start fresh") }, ...earlier.map((m) => ({ value: m.id, label: m.name }))]}
                value={startFrom}
                onChange={(v) => setStartFrom(v ?? "")}
                allowDeselect={false}
              />
            )}
            {chosenArch?.designable && dataset && (
              <ModelShape
                inputs={inputs}
                columns={dataset.columns.length > 0 ? dataset.columns.map((c) => c.name) : dataset.features}
                answers={answers}
                rows={Number(dataset.rows)}
                size={size}
                steps={shapeSteps}
                onChange={(sz, st) => { setSize(sz); setShapeSteps(st); }}
              />
            )}
            <Stack mt="lg">
              <TextInput label={t("Name")} required maxLength={100} value={name} onChange={(e) => setName(e.currentTarget.value)} />
              <Textarea label={t("Description")} description={t("Optional. What is it for?")} maxLength={500} autosize minRows={2} value={description} onChange={(e) => setDescription(e.currentTarget.value)} />
            </Stack>
          </Stepper.Step>

          <Stepper.Step label={steps[2]} description={t("Then start")}>
            {chosenArch?.learned ? (
              <Text mt="md" size="sm">{chosenArch.title}{" "}{t("learns from the whole table in one go")}{chosenArch.target ? "" : t(", and stops on its own once more would not help")}{t(", so there is nothing to choose here.")}</Text>
            ) : (
            <Radio.Group value={preset} onChange={setPreset} label={t("How hard should it study?")} mt="md">
              <SimpleGrid cols={{ base: 1, sm: 3 }} mt="sm">
                {presets.map((p) => (
                  <Radio.Card key={p.value} value={p.value} radius="md" p="md" disabled={expert}>
                    <Group wrap="nowrap" align="flex-start">
                      <Radio.Indicator />
                      <div><Text fw={600}>{p.label}</Text><Text size="sm" c="dimmed">{p.epochs}{" "}{t("passes over the examples.")}{" "}{tpl && p.value === "normal" ? t("The template's pick.") : p.hint}</Text></div>
                    </Group>
                  </Radio.Card>
                ))}
              </SimpleGrid>
            </Radio.Group>
            )}
            <UnstyledButton mt="md" onClick={toggle} className="text-sm text-[var(--mantine-color-teal-7)] underline">{expert ? t("Hide expert settings") : t("Expert settings")}</UnstyledButton>
            <Collapse expanded={expert}>
              <SimpleGrid cols={{ base: 1, sm: 2 }} mt="sm">
                {!chosenArch?.learned && (
                  <>
                    <NumberInput label={t("Passes over the examples")} description={t("Epochs")} min={1} max={1000} value={epochs} onChange={setEpochs} />
                    <NumberInput label={t("Examples per step")} description={t("Batch size")} min={1} max={4096} value={batch} onChange={setBatch} />
                    <NumberInput label={t("Learning rate")} description={t("How big each adjustment is")} min={0.000001} max={1} step={0.0005} decimalScale={6} value={rate} onChange={setRate} />
                  </>
                )}
                <NumberInput label={t("Kept aside for scoring")} description={t("Percent of examples the model never learns from, used to measure it honestly")} min={0} max={50} suffix="%" value={holdOut} onChange={setHoldOut} />
              </SimpleGrid>
            </Collapse>
            {workers.length > 0 && (
              <div className="mt-6 max-w-md">
                <WorkerSelect
                  workers={workers}
                  value={runOn}
                  onChange={setRunOn}
                  forTraining
                  label={t("Train on")}
                  description={runOn ? (workers.find((w) => w.id === runOn)?.trainingOnly ? t("It trains there; the model then answers wherever there's room. If it's offline, the run waits for it.") : t("It trains there and answers there afterwards. If it's offline, the run waits for it.")) : t("The least busy worker that can teach starts it.")}
                />
              </div>
            )}
            {fromQuery && (
              <Stack gap={4} mt="lg">
                <Text fw={500} size="sm">{t("Keep learning")}</Text>
                <Text size="xs" c="dimmed">{dataset?.storage ? t("Read the files again on a schedule") : t("Run the query again on a schedule")}{" "}{t("and continue teaching the same model with the new rows. The model is only replaced when the new pass scores at least as well.")}</Text>
                <SegmentedControl value={repeat} onChange={setRepeat} data={repeatChoicesFor(waitRows)} className="self-start" />
                {repeat !== "0" && (
                  <NumberInput
                    mt="xs"
                    maw={320}
                    label={t("Wait for new rows")}
                    description={waitRows > 0 ? t("It looks {n1} and only learns again once there are {n2} more rows than last time.", { n1: repeatChoices.find((c) => c.value === repeat)!.label.toLowerCase(), n2: waitRows.toLocaleString() }) : t("0 learns on every schedule. Set a number to learn only once that many new rows have arrived.")}
                    min={0}
                    max={1_000_000_000}
                    step={100}
                    thousandSeparator=","
                    value={minRows}
                    onChange={(v) => {
                      setMinRows(v);
                      if (!(Number(v) > 0) && repeat === "900") setRepeat("3600");
                    }}
                  />
                )}
              </Stack>
            )}
            <Text size="sm" c="dimmed" mt="md">
              {t("{name} will learn from {rows} examples {how}{repeat}.", {
                name: name || t("The model"),
                rows: dataset?.rows.toLocaleString(),
                how: chosenArch?.learned ? t("in one go") : t("over {chosenEpochs} passes", { chosenEpochs }),
                repeat: fromQuery && repeat !== "0" ? t(", then keep learning {n1}{n2}", { n1: repeatChoices.find((c) => c.value === repeat)!.label.toLowerCase(), n2: waitRows > 0 ? t(" once {n1} new rows are in", { n1: waitRows.toLocaleString() }) : "" }) : "",
              })}{" "}{archive ? t("When it's done you can try it with one photo, or send it new ones from your apps.") : t("When it's done you can try it with one row, or send it new ones from your apps.")}{" "}{t("You can stop it any time and pick up later.")}</Text>
            {createJob.error && <Alert mt="md" color="red" icon={<IconAlertCircle />}>{message(createJob.error)}</Alert>}
          </Stepper.Step>
        </Stepper>

        <Group justify="space-between" mt="xl">
          {step === 0 && tpl
            ? <ButtonLink variant="default" to="/templates/$id" params={{ id: tpl.id }}>{t("Back")}</ButtonLink>
            : <Button variant="default" onClick={() => setStep(step - 1)} disabled={step === 0 || createJob.isPending}>{t("Back")}</Button>}
          {step === 0 && (
            <Button onClick={next} loading={busy} disabled={!ready0}>
              {dataset ? (check?.verdict === "bad" ? t("Fix the data first") : t("Continue")) : source === "existing" && !tpl ? t("Next") : t("Check my data")}
            </Button>
          )}
          {step === 1 && <Button onClick={() => setStep(2)} disabled={!arch || !name.trim()}>{t("Continue")}</Button>}
          {step === 2 && <Button loading={createJob.isPending} disabled={!dataset || !arch || !name.trim()} onClick={start}>{t("Start teaching")}</Button>}
        </Group>
      </Card>
      {!tpl && (
        <Text size="sm" c="dimmed">{t("Not sure how to shape your data?")}{" "}<Link to="/templates" search={dataType ? { data: dataType } : {}}>{t("Start from a template")}</Link>.
        </Text>
      )}
    </Page>
  );
}

// guessTarget picks the answer column to start with, from what the first
// rows hold: for a recommender the column that looks like the item, for a
// decision model the one that looks like the action, otherwise the last
// column that can be an answer.
function guessTarget(columns: string[], rows: string[][], role: Role) {
  return guess(profile({ columns, rows }), role);
}

// canAnswer reports whether target can be the answer column, from what
// the first rows hold: not an ID, a date or an empty column. A model that
// reads every column needs none.
function canAnswer(columns: string[], rows: string[][], target: string, role: Role) {
  if (role === "none") return true;
  const c = profile({ columns, rows }).find((c) => c.name === target);
  return !!c && answer(c, role).level !== "bad";
}

// Preview shows the first rows of a table a template will match.
function Preview({ columns, rows }: { columns: string[]; rows: string[][] }) {
  return (
    <Table.ScrollContainer minWidth={300}>
      <Table fz="xs" withTableBorder>
        <Table.Thead><Table.Tr>{columns.map((c) => <Table.Th key={c}>{c}</Table.Th>)}</Table.Tr></Table.Thead>
        <Table.Tbody>{rows.map((r, i) => <Table.Tr key={i}>{r.map((v, k) => <Table.Td key={k}>{v}</Table.Td>)}</Table.Tr>)}</Table.Tbody>
      </Table>
    </Table.ScrollContainer>
  );
}

function archiveKind(k: DatasetKind) {
  return k === DatasetKind.PHOTOS || k === DatasetKind.PHOTO_TABLE || k === DatasetKind.MNIST;
}

function Choice({ value, icon, title, hint, children }: { value: Source; icon: React.ReactNode; title: string; hint: string; children: React.ReactNode }) {
  return (
    <Radio.Card value={value} radius="md" p="md">
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
