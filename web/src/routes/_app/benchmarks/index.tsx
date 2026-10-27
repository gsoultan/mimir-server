import { useMutation } from "@connectrpc/connect-query";
import { Alert, Badge, Button, Card, Group, Modal, NumberInput, Select, Stack, Text, TextInput, Textarea } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { IconAlertCircle, IconTrophy, IconUpload } from "@tabler/icons-react";
import { useQueryClient, useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";

import { message } from "../../../api/client";
import { BenchmarkService } from "../../../api/gen/mimir/v1/benchmark_pb";
import { DatasetKind } from "../../../api/gen/mimir/v1/training_pb";
import { allBenchmarkQueries, listBenchmarks, listDatasets } from "../../../api/queries";
import { AnchorLink } from "../../../components/links";
import { Page } from "../../../components/Page";
import { headlineText } from "../../../lib/benchmark";
import { ago } from "../../../lib/format";

export const Route = createFileRoute("/_app/benchmarks/")({
  loader: ({ context }) => Promise.all([
    context.queryClient.ensureQueryData(listBenchmarks),
    context.queryClient.ensureQueryData(listDatasets),
  ]),
  component: Benchmarks,
});

function Benchmarks() {
  const { data } = useSuspenseQuery(listBenchmarks);
  const { data: datasets } = useSuspenseQuery(listDatasets);
  const tables = datasets.datasets.filter((d) => d.kind === DatasetKind.TABLE);
  const [adding, setAdding] = useState(false);

  return (
    <Page
      title="Benchmarks"
      intro="A fixed test every model takes the same way, so you can see which is really better. Its rows are kept aside: training on the same examples leaves them out, so no model learns the answers to its own test."
      actions={<Button leftSection={<IconTrophy size={18} />} disabled={tables.length === 0} onClick={() => setAdding(true)}>New benchmark</Button>}
    >
      {data.benchmarks.length === 0 ? (
        <Card withBorder radius="lg" padding="xl" className="text-center">
          <Text fw={600}>No benchmarks yet</Text>
          <Text c="dimmed" mb="md">
            {tables.length === 0
              ? "Upload a table of examples first; a benchmark keeps some of its rows aside as the test."
              : "Pick a table of examples and keep a fifth of its rows aside. Every model taught from it is then scored on those same rows."}
          </Text>
          {tables.length > 0 && <Group justify="center"><Button onClick={() => setAdding(true)}>New benchmark</Button></Group>}
        </Card>
      ) : (
        <Stack gap="sm">
          {data.benchmarks.map((b) => (
            <Card key={b.id} withBorder radius="lg" padding="md">
              <Group justify="space-between" wrap="nowrap" align="flex-start">
                <div className="min-w-0 flex-1">
                  <Group gap="xs" wrap="nowrap">
                    <AnchorLink fw={700} truncate to="/benchmarks/$id" params={{ id: b.id }} c="inherit">{b.name}</AnchorLink>
                    <Badge variant="light">{b.kind === "number" ? "Numbers" : "Labels"}</Badge>
                  </Group>
                  <Text size="sm" c="dimmed">
                    {b.rows.toLocaleString()} rows from {b.datasetName || "deleted examples"} · answers {b.target} · made {ago(b.createTime)}
                  </Text>
                  <Text size="sm" mt={4}>
                    {b.scored === 0
                      ? "No model scored yet."
                      : <>Best so far: <b>{b.leaderName}</b>, {headlineText(b, b.leaderHeadline)} · {b.scored} {b.scored === 1 ? "model" : "models"} scored</>}
                  </Text>
                </div>
              </Group>
            </Card>
          ))}
        </Stack>
      )}
      <NewBenchmark open={adding} onClose={() => setAdding(false)} tables={tables.map((d) => ({ value: d.id, label: `${d.name} (${d.rows.toLocaleString()} rows)` }))} />
    </Page>
  );
}

function NewBenchmark({ open, onClose, tables }: { open: boolean; onClose: () => void; tables: { value: string; label: string }[] }) {
  const queryClient = useQueryClient();
  const [datasetId, setDatasetId] = useState<string | null>(tables.at(0)?.value ?? null);
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [percent, setPercent] = useState<number>(20);
  const [csv, setCsv] = useState<Uint8Array>();
  const [csvName, setCsvName] = useState("");
  const create = useMutation(BenchmarkService.method.createBenchmark, {
    onSuccess: async (res) => {
      await queryClient.invalidateQueries({ queryKey: allBenchmarkQueries });
      notifications.show({ message: `${res.benchmark?.name} is ready, with ${res.benchmark?.rows} rows.` });
      close();
    },
  });
  const close = () => {
    setName(""); setDescription(""); setCsv(undefined); setCsvName(""); create.reset();
    onClose();
  };
  const pickFile = async (file: File | undefined) => {
    if (!file) { setCsv(undefined); setCsvName(""); return; }
    setCsv(new Uint8Array(await file.arrayBuffer()));
    setCsvName(file.name);
  };

  return (
    <Modal opened={open} onClose={close} title="New benchmark" radius="lg" centered>
      <Stack gap="md">
        {create.error && <Alert color="red" icon={<IconAlertCircle />}>{message(create.error)}</Alert>}
        <TextInput label="Name" placeholder="Fruit test" value={name} onChange={(e) => setName(e.currentTarget.value)} required />
        <Textarea label="What it tests" placeholder="Optional" autosize minRows={2} value={description} onChange={(e) => setDescription(e.currentTarget.value)} />
        <Select label="Examples" data={tables} value={datasetId} onChange={setDatasetId} allowDeselect={false}
          description="The test rows come from this table, and training on it leaves them out." />
        {csv ? (
          <Group gap="xs">
            <Text size="sm">Test rows: <b>{csvName}</b></Text>
            <Button size="compact-xs" variant="subtle" onClick={() => pickFile(undefined)}>Use rows from the table instead</Button>
          </Group>
        ) : (
          <>
            <NumberInput label="Rows to keep aside" suffix="%" min={5} max={50} value={percent} onChange={(v) => setPercent(Number(v) || 20)}
              description="A fifth is usually enough: plenty to judge by, and most of the examples still go into learning." />
            <Button component="label" variant="default" leftSection={<IconUpload size={18} />}>
              Or upload the test rows
              <input type="file" accept=".csv,text/csv" hidden onChange={(e) => pickFile(e.currentTarget.files?.[0])} />
            </Button>
          </>
        )}
        <Group justify="flex-end">
          <Button variant="default" onClick={close}>Cancel</Button>
          <Button loading={create.isPending} disabled={!datasetId || name.trim() === ""}
            onClick={() => create.mutate({ name: name.trim(), description: description.trim(), datasetId: datasetId!, share: csv ? 0 : percent / 100, csv })}>
            Make the benchmark
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}
