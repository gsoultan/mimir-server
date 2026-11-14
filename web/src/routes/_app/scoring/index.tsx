import { useMutation } from "@connectrpc/connect-query";
import {
  Alert, Badge, Button, Card, Group, Modal, PasswordInput, Progress, SegmentedControl, Select, SimpleGrid, Stack, Text, Textarea, TextInput, ThemeIcon,
} from "@mantine/core";
import { Dropzone } from "@mantine/dropzone";
import { notifications } from "@mantine/notifications";
import { IconAlertCircle, IconDownload, IconListCheck } from "@tabler/icons-react";
import { useQueryClient, useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { z } from "zod";

import { message } from "../../../api/client";
import { ScoringService } from "../../../api/gen/mimir/v1/scoring_pb";
import { DatasetKind, JobStatus } from "../../../api/gen/mimir/v1/training_pb";
import { allScoringQueries, listArchitectures, listDatasets, listModels, listScorings, whoAmI } from "../../../api/queries";
import { AnchorLink } from "../../../components/links";
import { Page } from "../../../components/Page";
import { Row, RowList } from "../../../components/ui";
import { ago, bytes } from "../../../lib/format";
import { progress, repeats, resultsURL, scores, statusText, waiting } from "../../../lib/scoring";
import { t } from "../../../i18n";

export const Route = createFileRoute("/_app/scoring/")({
  validateSearch: z.object({ model: z.string().optional() }),
  loader: ({ context }) => Promise.all([
    context.queryClient.ensureQueryData(listScorings),
    context.queryClient.ensureQueryData(listModels),
    context.queryClient.ensureQueryData(listArchitectures),
    context.queryClient.ensureQueryData(listDatasets),
  ]),
  component: Scorings,
});

function Scorings() {
  const { model } = Route.useSearch();
  const { data } = useSuspenseQuery({ ...listScorings, refetchInterval: (q) => (q.state.data?.scorings.some(waiting) ? 2000 : false) });
  const { data: models } = useSuspenseQuery(listModels);
  const { data: archs } = useSuspenseQuery(listArchitectures);
  const fit = models.models.filter((m) => scores(archs.architectures.find((a) => a.name === m.architecture)));
  const [adding, setAdding] = useState(model !== undefined);

  return (
    <Page
      title={t("Score a list")}
      crumbs={[{ label: t("Try a model"), to: "/try" }, { label: t("Score a list") }]}
      intro={t("Answer every row of a list at once, from a file, your database or storage. You get each row back with its answer, and a list from a database can be scored again on a schedule.")}
      actions={data.scorings.length > 0 && <Button leftSection={<IconListCheck size={18} />} disabled={fit.length === 0} onClick={() => setAdding(true)}>{t("Score a list")}</Button>}
    >
      {data.scorings.length === 0 ? (
        <Card withBorder padding="xl" className="text-center">
          <ThemeIcon size={44} radius="xl" variant="light" mx="auto" mb="sm"><IconListCheck size={22} /></ThemeIcon>
          <Text fw={650}>{t("Nothing scored yet")}</Text>
          <Text c="dimmed" mb="md">
            {fit.length === 0
              ? t("Teach a model from a table first. Models that read a row of a table can score a whole list.")
              : t("Pick a model and a list of rows, like this month's customers, and get each one's answer in a file to download.")}
          </Text>
          {fit.length > 0 && <Group justify="center"><Button leftSection={<IconListCheck size={18} />} onClick={() => setAdding(true)}>{t("Score a list")}</Button></Group>}
        </Card>
      ) : (
        <RowList>
          {data.scorings.map((s) => (
            <Row
              key={s.id}
              tone={s.error || s.status === JobStatus.FAILED ? "bad" : waiting(s) ? "busy" : "ok"}
              icon={<IconListCheck size={20} />}
              title={<Group gap="xs" wrap="nowrap">
                <AnchorLink fw={650} truncate to="/scoring/$id" params={{ id: s.id }} c="inherit">{s.name}</AnchorLink>
                {s.repeatSeconds > 0n && <Badge variant="light" color="gray">{repeats.find((r) => r.value === s.repeatSeconds.toString())?.label ?? t("On a schedule")}</Badge>}
              </Group>}
              detail={<>{s.modelName} · {s.sourceDetail} · {s.endTime ? t("last run {endTime}", { endTime: ago(s.endTime) }) : t("made {createTime}", { createTime: ago(s.createTime) })}<br /><Text span c={s.error ? "red" : undefined} size="sm">{s.error || statusText(s)}{s.resultBytes > 0n && !waiting(s) ? ` · ${bytes(s.resultBytes)}` : ""}</Text></>}
              extra={waiting(s) && <Progress value={progress(s)} color="violet" size="sm" mt={8} maw={380} animated aria-label={t("Progress")} />}
            >
              {s.resultBytes > 0n && !waiting(s) && <Button size="xs" variant="default" component="a" href={resultsURL(s.id)} download leftSection={<IconDownload size={14} />}>{t("Download")}</Button>}
            </Row>
          ))}
        </RowList>
      )}
      <NewScoring open={adding} onClose={() => setAdding(false)} modelId={model} models={fit.map((m) => ({ value: m.id, label: m.name }))} />
    </Page>
  );
}

const drivers = [
  { value: "postgres", label: "PostgreSQL", example: "postgres://user:password@db.example.com:5432/shop" },
  { value: "mysql", label: "MySQL", example: "user:password@tcp(db.example.com:3306)/shop" },
  { value: "sqlite", label: "SQLite", example: "/srv/data/shop.db" },
];
const providers = [
  { value: "r2", label: t("Cloudflare R2") },
  { value: "s3", label: t("AWS S3") },
  { value: "other", label: t("Other S3-compatible") },
];
type Bucket = { provider: string; accountId: string; region: string; endpoint: string; bucket: string; accessKeyId: string; secretAccessKey: string };
const noBucket: Bucket = { provider: "r2", accountId: "", region: "", endpoint: "", bucket: "", accessKeyId: "", secretAccessKey: "" };
const bucketFilled = (b: Bucket) => !!b.bucket.trim() && !!b.accessKeyId.trim() && !!b.secretAccessKey.trim()
  && (b.provider !== "r2" || !!b.accountId.trim()) && (b.provider !== "other" || !!b.endpoint.trim());

function NewScoring({ open, onClose, modelId, models }: { open: boolean; onClose: () => void; modelId?: string; models: { value: string; label: string }[] }) {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const { data: me } = useSuspenseQuery(whoAmI);
  const { data: datasets } = useSuspenseQuery(listDatasets);
  const canConnect = me.permissions.includes("databases.query");
  const tables = datasets.datasets.filter((d) => d.kind === DatasetKind.TABLE).map((d) => ({ value: d.id, label: t("{name} ({n2} rows)", { name: d.name, n2: Number(d.rows).toLocaleString() }) }));
  const [model, setModel] = useState<string | null>(models.find((m) => m.value === modelId)?.value ?? models.at(0)?.value ?? null);
  const [source, setSource] = useState("upload");
  const [file, setFile] = useState<{ name: string; csv: Uint8Array }>();
  const [dataset, setDataset] = useState<string | null>(tables.at(0)?.value ?? null);
  const [driver, setDriver] = useState("postgres");
  const [dsn, setDsn] = useState("");
  const [query, setQuery] = useState("");
  const [bucket, setBucket] = useState<Bucket>(noBucket);
  const [path, setPath] = useState("");
  const [repeat, setRepeat] = useState("0");
  const [name, setName] = useState("");
  const create = useMutation(ScoringService.method.createScoring, {
    onSuccess: async (res) => {
      await queryClient.invalidateQueries({ queryKey: allScoringQueries });
      notifications.show({ message: t("Scoring {name}.", { name: res.scoring?.name }) });
      close();
      await navigate({ to: "/scoring/$id", params: { id: res.scoring!.id } });
    },
  });
  const close = () => {
    setFile(undefined); setName(""); setRepeat("0"); create.reset();
    onClose();
  };
  const editBucket = (b: Partial<Bucket>) => setBucket({ ...bucket, ...b });
  const fetched = source === "database" || source === "storage";
  const ready = !!model && (
    (source === "upload" && !!file)
    || (source === "dataset" && !!dataset)
    || (source === "database" && !!dsn.trim() && !!query.trim())
    || (source === "storage" && bucketFilled(bucket)));
  const submit = () => create.mutate({
    modelId: model!,
    name: name.trim(),
    csv: source === "upload" ? file?.csv : undefined,
    datasetId: source === "dataset" ? dataset! : "",
    connection: source === "database" ? { driver, dsn } : undefined,
    query: source === "database" ? query.trim() : "",
    bucket: source === "storage" ? bucket : undefined,
    path: source === "storage" ? path.trim() : "",
    repeatSeconds: fetched ? BigInt(repeat) : 0n,
  });
  const sources = [
    { value: "upload", label: t("A file") },
    { value: "dataset", label: t("Examples") },
    ...(canConnect ? [{ value: "database", label: t("Database") }, { value: "storage", label: t("Storage") }] : []),
  ];

  return (
    <Modal opened={open} onClose={close} title={t("Score a list")} radius="lg" size="lg" centered>
      <Stack gap="md">
        {create.error && <Alert color="red" icon={<IconAlertCircle />}>{message(create.error)}</Alert>}
        <Select label={t("Model")} data={models} value={model} onChange={setModel} allowDeselect={false} searchable
          description={t("It answers every row, from the columns it learned from. Other columns come along untouched.")} />
        <div>
          <Text size="sm" fw={500} mb={4}>{t("Where the rows are")}</Text>
          <SegmentedControl fullWidth value={source} onChange={setSource} data={sources} />
        </div>
        {source === "upload" && (
          <Dropzone accept={["text/csv", ".csv"]} multiple={false} maxSize={64 << 20}
            onDrop={async ([f]) => f && setFile({ name: f.name, csv: new Uint8Array(await f.arrayBuffer()) })}
            onReject={() => notifications.show({ color: "red", message: t("Choose a .csv file of up to 64 MB.") })}>
            <Stack align="center" gap={2} py="md" className="pointer-events-none">
              <Text fw={600}>{file ? file.name : t("Drop your .csv file here")}</Text>
              <Text size="sm" c="dimmed">{file ? bytes(file.csv.length) : t("with a header row; it needs no answer column")}</Text>
            </Stack>
          </Dropzone>
        )}
        {source === "dataset" && (
          tables.length === 0
            ? <Text size="sm" c="dimmed">{t("No tables of examples yet.")}</Text>
            : <Select label={t("Examples")} data={tables} value={dataset} onChange={setDataset} allowDeselect={false}
              description={t("Handy for checking a model on rows whose answers you know: they stay in the results next to its answers.")} />
        )}
        {source === "database" && (
          <Stack gap="sm">
            <SegmentedControl value={driver} onChange={setDriver} data={drivers.map((d) => ({ value: d.value, label: d.label }))} />
            {driver === "sqlite"
              ? <TextInput label={t("Database file")} description={t("The path to the file on the server.")} value={dsn} onChange={(e) => setDsn(e.currentTarget.value)} />
              : <PasswordInput label={t("Connection")} description={t("For example {example}. Kept on the server; never shown again.", { example: drivers.find((d) => d.value === driver)!.example })} value={dsn} onChange={(e) => setDsn(e.currentTarget.value)} />}
            <Textarea label={t("Query")} description={t("One row per thing to score, with the columns the model reads.")} placeholder="SELECT id, plan, monthly_bill, tenure_months FROM customers WHERE active" autosize minRows={3} value={query} onChange={(e) => setQuery(e.currentTarget.value)} classNames={{ input: "font-mono" }} />
          </Stack>
        )}
        {source === "storage" && (
          <Stack gap="sm">
            <SegmentedControl value={bucket.provider} onChange={(v) => editBucket({ provider: v })} data={providers} />
            <SimpleGrid cols={{ base: 1, sm: 2 }}>
              {bucket.provider === "r2" && <TextInput label={t("Account ID")} value={bucket.accountId} onChange={(e) => editBucket({ accountId: e.currentTarget.value })} />}
              {bucket.provider === "s3" && <TextInput label={t("Region")} placeholder="us-east-1" value={bucket.region} onChange={(e) => editBucket({ region: e.currentTarget.value })} />}
              {bucket.provider === "other" && <TextInput label={t("Address")} placeholder="https://minio.example.com" value={bucket.endpoint} onChange={(e) => editBucket({ endpoint: e.currentTarget.value })} />}
              <TextInput label={t("Bucket")} value={bucket.bucket} onChange={(e) => editBucket({ bucket: e.currentTarget.value })} />
              <TextInput label={t("Access key ID")} value={bucket.accessKeyId} onChange={(e) => editBucket({ accessKeyId: e.currentTarget.value })} autoComplete="off" />
              <PasswordInput label={t("Secret access key")} description={t("Kept on the server; never shown again.")} value={bucket.secretAccessKey} onChange={(e) => editBucket({ secretAccessKey: e.currentTarget.value })} autoComplete="off" />
            </SimpleGrid>
            <TextInput label={t("File or folder")} placeholder="customers/2026-10.csv" description={t("A .csv file, or a folder of .csv files with the same header row.")}
              value={path} onChange={(e) => setPath(e.currentTarget.value)} />
          </Stack>
        )}
        {fetched && <Select label={t("Score it")} data={repeats} value={repeat} onChange={(v) => setRepeat(v ?? "0")} allowDeselect={false}
          description={t("Each time, the rows are fetched again and the results replaced.")} />}
        <TextInput label={t("Name")} placeholder={t("Optional")} maxLength={100} value={name} onChange={(e) => setName(e.currentTarget.value)} />
        <Group justify="flex-end">
          <Button variant="default" onClick={close}>{t("Cancel")}</Button>
          <Button loading={create.isPending} disabled={!ready} onClick={submit}>{t("Score it")}</Button>
        </Group>
      </Stack>
    </Modal>
  );
}
