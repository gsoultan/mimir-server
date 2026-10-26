import { useMutation } from "@connectrpc/connect-query";
import { Alert, Badge, Button, Card, Group, Modal, Stack, Table, Text, TextInput, Title } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { IconAlertCircle, IconRepeat } from "@tabler/icons-react";
import { useQueryClient, useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useState, type ReactNode } from "react";
import { z } from "zod";

import { message } from "../../../api/client";
import { JobStatus, TrainingService, type Job } from "../../../api/gen/mimir/v1/training_pb";
import { allTrainingQueries, compareJobs, listArchitectures } from "../../../api/queries";
import { AnchorLink, ButtonLink } from "../../../components/links";
import { CompareChart } from "../../../components/CompareChart";
import { Page } from "../../../components/Page";
import { ago } from "../../../lib/format";
import { againName, bestPass, parseIds, runColor, shapeWord, took, trainingSeconds } from "../../../lib/compare";
import { active, score, statusColor, statusWord } from "../../../lib/training";

export const Route = createFileRoute("/_app/training/compare")({
  // ids lists the runs, comma-separated, in the order to show them.
  validateSearch: z.object({ ids: z.string().optional() }),
  loaderDeps: ({ search }) => ({ ids: parseIds(search.ids) }),
  loader: ({ context, deps }) => Promise.all([
    deps.ids.length >= 2 ? context.queryClient.ensureQueryData(compareJobs(deps.ids)) : undefined,
    context.queryClient.ensureQueryData(listArchitectures),
  ]),
  errorComponent: ({ error }) => (
    <Alert color="red" icon={<IconAlertCircle />} className="mx-auto max-w-5xl">
      {message(error)} <Link to="/training">Back to training</Link>
    </Alert>
  ),
  component: Compare,
});

function Compare() {
  const ids = parseIds(Route.useSearch().ids);
  if (ids.length < 2) {
    return (
      <Page title="Compare runs" intro="See how runs did side by side.">
        <Card withBorder radius="lg" padding="xl" className="text-center">
          <Text fw={600}>Pick two or more runs first</Text>
          <Text c="dimmed" mb="md">Tick the runs you want on the training page, then choose Compare.</Text>
          <Group justify="center"><ButtonLink to="/training">Go to training</ButtonLink></Group>
        </Card>
      </Page>
    );
  }
  return <Runs ids={ids} />;
}

const round = (n: number) => n.toLocaleString(undefined, { maximumFractionDigits: 4 });

function Runs({ ids }: { ids: string[] }) {
  const { data } = useSuspenseQuery({ ...compareJobs(ids), refetchInterval: (q) => (q.state.data?.jobs.some(active) ? 2000 : false) });
  const { data: archs } = useSuspenseQuery(listArchitectures);
  const [again, setAgain] = useState<Job>();
  const jobs = data.jobs;
  const differs = new Set(data.differs);
  const arch = (j: Job) => archs.architectures.find((a) => a.name === j.architecture);
  const isNumber = (j: Job) => arch(j)?.output === "number";
  const anyNumber = jobs.some(isNumber);
  const allNumber = jobs.every(isNumber);

  // Each row is one thing about the runs; key matches the server's
  // differs names where the row is a setting.
  const rows: { label: string; key?: string; cell: (j: Job) => ReactNode }[] = [
    { label: "Score", cell: (j) => (j.status === JobStatus.SUCCEEDED ? score(j.accuracy, isNumber(j)) : <Badge color={statusColor[j.status]} variant="light">{statusWord[j.status]}</Badge>) },
    { label: "Best pass", cell: (j) => { const b = bestPass(j); return b ? `${(b.accuracy * 100).toFixed(1)}% at pass ${b.epoch}` : "–"; } },
    ...(anyNumber ? [{ label: "Usually off by", cell: (j: Job) => { const e = j.history.at(-1)?.typicalError ?? 0; return isNumber(j) && e > 0 ? round(e) : "–"; } }] : []),
    { label: "Time learning", cell: (j) => (j.history.length > 0 ? took(trainingSeconds(j)) : "–") },
    { label: "Per pass, on average", cell: (j) => (j.history.length > 0 ? took(trainingSeconds(j) / j.history.length) : "–") },
    { label: "Model type", key: "architecture", cell: (j) => arch(j)?.title ?? j.architecture },
    { label: "Examples", key: "dataset", cell: (j) => j.datasetName || "Deleted" },
    { label: "Passes over the examples", key: "epochs", cell: (j) => `${j.history.length} of ${j.settings?.epochs ?? 0}` },
    { label: "Examples per step", key: "batch_size", cell: (j) => String(j.settings?.batchSize ?? "–") },
    { label: "Learning rate", key: "learning_rate", cell: (j) => round(j.settings?.learningRate ?? 0) },
    { label: "Kept aside for scoring", key: "hold_out", cell: (j) => `${Math.round((j.settings?.holdOut ?? 0) * 100)}%` },
    { label: "Shape", key: "layers", cell: (j) => shapeWord(j.settings?.layers ?? []) },
    { label: "Trained on", key: "worker", cell: (j) => (j.worker ? `Worker ${j.worker}` : "This server") },
    { label: "Started", cell: (j) => `${ago(j.createTime)} by ${j.createdBy}` },
  ];
  const best = jobs.find((j) => j.id === data.bestId);

  return (
    <Page
      title="Compare runs"
      intro={best ? <>{best.name} scored best: it {score(best.accuracy, isNumber(best))}.{differs.size > 0 && " Highlighted rows are the settings that differ."}</> : "None of these runs has finished yet."}
      actions={<ButtonLink variant="default" to="/training">Back to training</ButtonLink>}
    >
      <Card withBorder radius="lg" padding="lg">
        <Title order={3} fz="lg" mb="sm">{allNumber ? "Close answers" : anyNumber ? "Right or close answers" : "Right answers"} per pass</Title>
        <CompareChart jobs={jobs} number={allNumber} />
      </Card>

      <Card withBorder radius="lg" padding="lg">
        <Title order={3} fz="lg" mb="sm">Side by side</Title>
        <Table.ScrollContainer minWidth={220 + 200 * jobs.length}>
          <Table verticalSpacing="xs" fz="sm" withColumnBorders>
            <Table.Thead>
              <Table.Tr>
                <Table.Th w={220} />
                {jobs.map((j, i) => (
                  <Table.Th key={j.id}>
                    <Group gap={6} wrap="nowrap" align="center">
                      <span className="inline-block h-2.5 w-2.5 shrink-0 rounded-full" style={{ background: runColor(i) }} aria-hidden />
                      <AnchorLink to="/training/$id" params={{ id: j.id }} fw={700} c="inherit" truncate>{j.name}</AnchorLink>
                      {j.id === data.bestId && <Badge color="teal" variant="light" size="sm">Best</Badge>}
                    </Group>
                  </Table.Th>
                ))}
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {rows.map((r) => {
                const differ = r.key !== undefined && differs.has(r.key);
                return (
                  <Table.Tr key={r.label} bg={differ ? "var(--mantine-color-yellow-light)" : undefined}>
                    <Table.Td c="dimmed">
                      {r.label}
                      {differ && <Text span size="xs" fw={600} c="var(--mantine-color-text)" ml={6}>· differs</Text>}
                    </Table.Td>
                    {jobs.map((j) => <Table.Td key={j.id} fw={differ ? 600 : undefined}>{r.cell(j)}</Table.Td>)}
                  </Table.Tr>
                );
              })}
              <Table.Tr>
                <Table.Td />
                {jobs.map((j) => (
                  <Table.Td key={j.id}>
                    <Button size="xs" variant="default" leftSection={<IconRepeat size={16} />} disabled={!j.datasetName} title={j.datasetName ? undefined : "Its examples were deleted"} onClick={() => setAgain(j)}>
                      Train again with these settings
                    </Button>
                  </Table.Td>
                ))}
              </Table.Tr>
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>
      </Card>
      <TrainAgain job={again} title={again ? arch(again)?.title ?? again.architecture : ""} onClose={() => setAgain(undefined)} />
    </Page>
  );
}

// TrainAgain starts a new run with another run's model type, examples and
// settings, under a new name.
function TrainAgain({ job, title, onClose }: { job?: Job; title: string; onClose: () => void }) {
  return (
    <Modal opened={!!job} onClose={onClose} title="Train again with these settings" centered>
      {job && <AgainForm key={job.id} job={job} title={title} onDone={onClose} />}
    </Modal>
  );
}

function AgainForm({ job, title, onDone }: { job: Job; title: string; onDone: () => void }) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [name, setName] = useState(againName(job.name));
  const create = useMutation(TrainingService.method.createJob, {
    onSuccess: async (res) => {
      await queryClient.invalidateQueries({ queryKey: allTrainingQueries });
      onDone();
      notifications.show({ message: "Training again with the same settings." });
      if (res.job) await navigate({ to: "/training/$id", params: { id: res.job.id } });
    },
  });
  const s = job.settings;
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        create.mutate({ name: name.trim(), description: job.description, architecture: job.architecture, datasetId: job.datasetId, settings: s, workerId: job.runOn });
      }}
    >
      <Stack gap="sm">
        <Text size="sm" c="dimmed">
          A new {title} learns from {job.datasetName} with {s?.epochs} passes, {s?.batchSize} examples per step, a learning rate of {round(s?.learningRate ?? 0)} and {Math.round((s?.holdOut ?? 0) * 100)}% kept aside{s && s.layers.length > 0 ? `, shaped ${shapeWord(s.layers)}` : ""}. It reads the examples as they are now.
        </Text>
        <TextInput label="Name" data-autofocus required value={name} onChange={(e) => setName(e.currentTarget.value)} />
        {create.error && <Alert color="red" icon={<IconAlertCircle />}>{message(create.error)}</Alert>}
        <Group justify="flex-end">
          <Button variant="default" onClick={onDone}>Cancel</Button>
          <Button type="submit" loading={create.isPending} disabled={!name.trim()}>Start</Button>
        </Group>
      </Stack>
    </form>
  );
}
