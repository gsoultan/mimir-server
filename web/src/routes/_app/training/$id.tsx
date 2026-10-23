import { useMutation } from "@connectrpc/connect-query";
import { Alert, Badge, Button, Card, Group, NumberInput, Progress, SegmentedControl, SimpleGrid, Stack, Table, Text, Title } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { IconAlertCircle, IconPlayerPlay, IconRefresh } from "@tabler/icons-react";
import { useQuery, useQueryClient, useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useEffect, useState } from "react";

import { message } from "../../../api/client";
import { TrainingService, JobStatus, type Job } from "../../../api/gen/mimir/v1/training_pb";
import { allModelQueries, allTrainingQueries, getJob, listArchitectures, listJobs } from "../../../api/queries";
import { ButtonLink } from "../../../components/links";
import { Page } from "../../../components/Page";
import { useWorkerChoices } from "../../../components/Placement";
import { TrainingChart } from "../../../components/TrainingChart";
import { ago } from "../../../lib/format";
import { active, keepWord, progress, repeatChoicesFor, repeatWord, score, statusColor, statusWord } from "../../../lib/training";

export const Route = createFileRoute("/_app/training/$id")({
  loader: ({ context, params }) => Promise.all([
    context.queryClient.ensureQueryData(getJob(params.id)),
    context.queryClient.ensureQueryData(listArchitectures),
  ]),
  errorComponent: ({ error }) => (
    <Alert color="red" icon={<IconAlertCircle />} className="mx-auto max-w-5xl">
      {message(error)} <Link to="/training">Back to training</Link>
    </Alert>
  ),
  component: Run,
});

const round = (n: number) => n.toLocaleString(undefined, { maximumFractionDigits: 2 });

function Run() {
  const { id } = Route.useParams();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { data } = useSuspenseQuery({ ...getJob(id), refetchInterval: (q) => (q.state.data?.job && active(q.state.data.job) ? 1500 : false) });
  const { data: archs } = useSuspenseQuery(listArchitectures);
  const j = data.job!;
  const arch = archs.architectures.find((a) => a.name === j.architecture);
  const refresh = async () => {
    await queryClient.invalidateQueries({ queryKey: allTrainingQueries });
    await queryClient.invalidateQueries({ queryKey: allModelQueries });
  };
  const cancel = useMutation(TrainingService.method.cancelJob, { onSuccess: refresh });
  const resume = useMutation(TrainingService.method.resumeJob, { onSuccess: refresh });
  const setRepeat = useMutation(TrainingService.method.setRepeat, {
    onSuccess: async (res) => { await refresh(); notifications.show({ message: res.job && res.job.repeatSeconds > 0n ? `It ${keepWord(res.job)}.` : "It won't learn again on its own." }); },
  });
  const remove = useMutation(TrainingService.method.deleteJob, {
    onSuccess: async () => { await navigate({ to: "/training" }); await refresh(); notifications.show({ message: "Run deleted." }); },
  });
  const epochs = j.settings?.epochs ?? 0;
  const last = j.history.at(-1);
  // A finished run made a model: make sure the model lists pick it up.
  useEffect(() => {
    if (j.status === JobStatus.SUCCEEDED) void queryClient.invalidateQueries({ queryKey: allModelQueries });
  }, [j.status, queryClient]);
  const error = cancel.error ?? resume.error ?? remove.error ?? setRepeat.error;
  const chain = j.parentId !== "" || j.repeatSeconds > 0n;
  const workers = useWorkerChoices(j.runOn !== "" && j.status === JobStatus.QUEUED);
  const number = arch?.output === "number";
  const waitingFor = workers.find((w) => w.id === j.runOn);

  return (
    <Page
      title={j.name}
      intro={<>{arch?.title ?? j.architecture} learning from {j.datasetName || "deleted examples"}{j.worker && ` on worker ${j.worker}`} · started by {j.createdBy} {ago(j.createTime)}</>}
      actions={
        <Group gap="xs">
          {active(j) && <Button variant="default" loading={cancel.isPending} onClick={() => cancel.mutate({ id })}>Stop</Button>}
          {(j.status === JobStatus.CANCELLED || j.status === JobStatus.FAILED) && <Button loading={resume.isPending} onClick={() => resume.mutate({ id })}>Resume</Button>}
          {j.status === JobStatus.SUCCEEDED && <ButtonLink to="/try" search={{ model: j.modelId }} leftSection={<IconPlayerPlay size={18} />}>Try it</ButtonLink>}
          {!active(j) && <Button variant="subtle" color="red" loading={remove.isPending} onClick={() => remove.mutate({ id })}>Delete</Button>}
        </Group>
      }
    >
      {error && <Alert color="red" icon={<IconAlertCircle />}>{message(error)}</Alert>}
      {chain && (
        <Card withBorder radius="lg" padding="lg">
          <Group justify="space-between" align="flex-start" wrap="wrap" gap="md">
            <Group gap="sm" wrap="nowrap" align="flex-start">
              <IconRefresh size={22} className="mt-0.5 shrink-0" />
              <div>
                <Text fw={600}>{j.repeatSeconds > 0n ? `Keeps learning ${repeatWord(j.repeatSeconds)}` : "Keep learning is off for this run"}</Text>
                <Text size="sm" c="dimmed">
                  {j.repeatSeconds > 0n
                    ? j.minNewRows > 0n
                      ? <>Next look {ago(j.nextRunTime)}: it fetches the examples again, and once there are {j.minNewRows.toLocaleString()} more rows than the {j.trainedRows.toLocaleString()} this pass learned from, it continues from the model, replacing it only if it scores at least as well.</>
                      : <>Next pass {ago(j.nextRunTime)}: it fetches the examples again and continues from the model, replacing it only if it scores at least as well.</>
                    : j.parentId ? <>This was one pass of a model that keeps learning. <Link to="/training/$id" params={{ id: j.parentId }}>See the first run</Link>.</> : null}
                </Text>
                {j.note && <Text size="sm" mt={4}>{j.note}</Text>}
                {j.repeatSeconds > 0n && j.checkTime && j.checkNote && <Text size="sm" mt={4}>Looked {ago(j.checkTime)}: {j.checkNote}</Text>}
              </div>
            </Group>
            <Stack gap="xs" align="flex-start" className="max-w-full">
              <div className="max-w-full overflow-x-auto">
              <SegmentedControl size="xs" value={String(j.repeatSeconds)} onChange={(v) => setRepeat.mutate({ id, repeatSeconds: BigInt(v), minNewRows: j.minNewRows })} data={repeatChoicesFor(j.minNewRows, j.repeatSeconds)} disabled={setRepeat.isPending} />
              </div>
              {j.repeatSeconds > 0n && <WaitForRows key={String(j.minNewRows)} job={j} pending={setRepeat.isPending} onSave={(n) => setRepeat.mutate({ id, repeatSeconds: n === 0n && j.repeatSeconds < 3600n ? 3600n : j.repeatSeconds, minNewRows: n })} />}
            </Stack>
          </Group>
        </Card>
      )}
      {chain && <Passes job={j} number={number} />}
      <Card withBorder radius="lg" padding="lg" aria-live="polite">
        <Group justify="space-between" mb="xs">
          <Group gap="xs">
            <Badge color={statusColor[j.status]} variant="light" size="lg">{statusWord[j.status]}</Badge>
            {j.status === JobStatus.RUNNING && <Text size="sm" c="dimmed">Pass {Math.min(j.epoch + 1, epochs)} of {epochs}{j.stepsPerEpoch > 0 && `, step ${j.step} of ${j.stepsPerEpoch}`}</Text>}
            {j.status === JobStatus.QUEUED && <Text size="sm" c="dimmed">{waitingFor ? (waitingFor.online ? `It starts on ${waitingFor.name} when that worker has room.` : `It waits for ${waitingFor.name}, which is offline. It starts as soon as that worker connects.`) : "Another run is ahead of it, or it's about to begin."}</Text>}
            {j.status === JobStatus.CANCELLED && <Text size="sm" c="dimmed">Stopped after {j.epoch} of {epochs} passes. Resume to carry on from there.</Text>}
            {j.status === JobStatus.SUCCEEDED && <Text size="sm" c="dimmed">Finished {ago(j.endTime)} after {epochs} passes.</Text>}
          </Group>
          {last && last.accuracy > 0 && <Title order={3} fz="lg">{score(last.accuracy, number)}</Title>}
        </Group>
        {active(j) && <Progress value={progress(j)} size="lg" radius="xl" animated={j.status === JobStatus.RUNNING} aria-label="Progress" mb="md" />}
        {j.status === JobStatus.FAILED && <Alert color="red" icon={<IconAlertCircle />} mb="md">{j.error}</Alert>}
        {j.status === JobStatus.SUCCEEDED && (
          <Alert color="teal" mb="md">
            The model is ready. It {score(j.accuracy, number)} on {j.settings && j.settings.holdOut > 0 ? "examples it never saw while learning" : "the examples it learned from"}{number && last && last.typicalError > 0 ? `, and is usually off by about ${round(last.typicalError)}` : ""}.{number && " Close means within a tenth of the real answer."}{" "}
            <Link to="/models/$id" params={{ id: j.modelId }}>See the model</Link>.
          </Alert>
        )}
        <TrainingChart history={j.history} epochs={epochs} number={number} />
      </Card>

      <SimpleGrid cols={{ base: 1, md: 2 }}>
        <Card withBorder radius="lg" padding="lg">
          <Title order={3} fz="lg" mb="sm">Each pass</Title>
          {j.history.length === 0 ? <Text c="dimmed" size="sm">Nothing yet.</Text> : (
            <Table verticalSpacing="xs" fz="sm">
              <Table.Thead><Table.Tr><Table.Th>Pass</Table.Th><Table.Th>{number ? "Close answers" : "Right answers"}</Table.Th>{number && <Table.Th>Usually off by</Table.Th>}<Table.Th>Loss</Table.Th><Table.Th>Took</Table.Th></Table.Tr></Table.Thead>
              <Table.Tbody>
                {j.history.map((e) => (
                  <Table.Tr key={e.epoch}>
                    <Table.Td>{e.epoch}</Table.Td>
                    <Table.Td>{e.accuracy > 0 ? `${(e.accuracy * 100).toFixed(1)}%` : "–"}</Table.Td>
                    {number && <Table.Td>{e.typicalError > 0 ? round(e.typicalError) : "–"}</Table.Td>}
                    <Table.Td>{e.loss.toFixed(4)}</Table.Td>
                    <Table.Td>{(Number(e.durationMillis) / 1000).toFixed(1)} s</Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          )}
        </Card>
        <Card withBorder radius="lg" padding="lg">
          <Title order={3} fz="lg" mb="sm">Settings</Title>
          <Stack gap={6}>
            <Fact label="Passes over the examples" value={String(epochs)} />
            <Fact label="Examples per step" value={String(j.settings?.batchSize)} />
            <Fact label="Learning rate" value={String(j.settings?.learningRate)} />
            <Fact label="Kept aside for scoring" value={`${Math.round((j.settings?.holdOut ?? 0) * 100)}%`} />
            {(j.settings?.layers.length ?? 0) > 0 && (
              <Fact label="Shape, designed" value={j.settings!.layers.map((l) => (l.kind === "dense" ? `${l.units} units` : `forget ${Math.round(l.rate * 100)}%`)).join(" → ")} />
            )}
            {j.description && <Fact label="Description" value={j.description} />}
          </Stack>
        </Card>
      </SimpleGrid>
    </Page>
  );
}

// WaitForRows sets how many new rows a pass waits for; 0 learns on every
// schedule.
function WaitForRows({ job, pending, onSave }: { job: Job; pending: boolean; onSave: (n: bigint) => void }) {
  const [rows, setRows] = useState<number | string>(Number(job.minNewRows));
  const n = BigInt(Math.max(0, Math.floor(Number(rows) || 0)));
  return (
    <Group gap="xs" align="flex-end" wrap="nowrap">
      <NumberInput size="xs" w={170} label="Wait for new rows" min={0} max={1_000_000_000} step={100} thousandSeparator="," value={rows} onChange={setRows} />
      <Button size="xs" variant="default" disabled={n === job.minNewRows} loading={pending} onClick={() => onSave(n)}>Save</Button>
    </Group>
  );
}

// Passes lists every pass of a model that keeps learning, newest first.
function Passes({ job, number }: { job: Job; number: boolean }) {
  const { data } = useQuery({ ...listJobs, refetchInterval: 10_000 });
  const root = job.parentId || job.id;
  const passes = (data?.jobs ?? []).filter((p) => p.id === root || p.parentId === root);
  if (passes.length < 2) return null;
  return (
    <Card withBorder radius="lg" padding="lg">
      <Title order={3} fz="lg" mb="sm">Every pass</Title>
      <Table.ScrollContainer minWidth={560}>
        <Table verticalSpacing="xs" fz="sm">
          <Table.Thead><Table.Tr><Table.Th>Started</Table.Th><Table.Th>Status</Table.Th><Table.Th>Rows</Table.Th><Table.Th>{number ? "Close answers" : "Right answers"}</Table.Th><Table.Th>What happened</Table.Th></Table.Tr></Table.Thead>
          <Table.Tbody>
            {passes.map((p) => (
              <Table.Tr key={p.id}>
                <Table.Td>{p.id === job.id ? ago(p.createTime) : <Link to="/training/$id" params={{ id: p.id }}>{ago(p.createTime)}</Link>}</Table.Td>
                <Table.Td><Badge color={statusColor[p.status]} variant="light">{statusWord[p.status]}</Badge></Table.Td>
                <Table.Td>{p.trainedRows > 0n ? p.trainedRows.toLocaleString() : "–"}</Table.Td>
                <Table.Td>{p.accuracy > 0 ? `${(p.accuracy * 100).toFixed(1)}%` : "–"}</Table.Td>
                <Table.Td>{p.status === JobStatus.FAILED ? p.error : p.note || (p.id === root ? "The first run." : "")}</Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      </Table.ScrollContainer>
    </Card>
  );
}

function Fact({ label, value }: { label: string; value: string }) {
  return (
    <Group justify="space-between" wrap="nowrap" gap="md">
      <Text size="sm" c="dimmed">{label}</Text>
      <Text size="sm" fw={600} ta="right">{value}</Text>
    </Group>
  );
}
