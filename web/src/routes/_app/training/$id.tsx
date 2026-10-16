import { useMutation } from "@connectrpc/connect-query";
import { Alert, Badge, Button, Card, Group, Progress, SegmentedControl, SimpleGrid, Stack, Table, Text, Title } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { IconAlertCircle, IconPlayerPlay, IconRefresh } from "@tabler/icons-react";
import { useQueryClient, useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useEffect } from "react";

import { message } from "../../../api/client";
import { TrainingService, JobStatus } from "../../../api/gen/mimir/v1/training_pb";
import { allModelQueries, allTrainingQueries, getJob, listArchitectures } from "../../../api/queries";
import { ButtonLink } from "../../../components/links";
import { Page } from "../../../components/Page";
import { TrainingChart } from "../../../components/TrainingChart";
import { ago } from "../../../lib/format";
import { active, progress, repeatChoices, repeatWord, score, statusColor, statusWord } from "../../../lib/training";

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
    onSuccess: async (res) => { await refresh(); notifications.show({ message: res.job && res.job.repeatSeconds > 0n ? `It will keep learning ${repeatWord(res.job.repeatSeconds)}.` : "It won't learn again on its own." }); },
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
                    ? <>Next pass {ago(j.nextRunTime)}: it fetches the examples again and continues from the model, replacing it only if it scores at least as well.</>
                    : j.parentId ? <>This was one pass of a model that keeps learning. <Link to="/training/$id" params={{ id: j.parentId }}>See the first run</Link>.</> : null}
                </Text>
                {j.note && <Text size="sm" mt={4}>{j.note}</Text>}
              </div>
            </Group>
            {(
              <SegmentedControl size="xs" value={String(j.repeatSeconds)} onChange={(v) => setRepeat.mutate({ id, repeatSeconds: BigInt(v) })} data={repeatChoices.some((c) => c.value === String(j.repeatSeconds)) ? repeatChoices : [...repeatChoices, { value: String(j.repeatSeconds), label: repeatWord(j.repeatSeconds) }]} disabled={setRepeat.isPending} />
            )}
          </Group>
        </Card>
      )}
      <Card withBorder radius="lg" padding="lg" aria-live="polite">
        <Group justify="space-between" mb="xs">
          <Group gap="xs">
            <Badge color={statusColor[j.status]} variant="light" size="lg">{statusWord[j.status]}</Badge>
            {j.status === JobStatus.RUNNING && <Text size="sm" c="dimmed">Pass {Math.min(j.epoch + 1, epochs)} of {epochs}{j.stepsPerEpoch > 0 && `, step ${j.step} of ${j.stepsPerEpoch}`}</Text>}
            {j.status === JobStatus.QUEUED && <Text size="sm" c="dimmed">Another run is ahead of it, or it's about to begin.</Text>}
            {j.status === JobStatus.CANCELLED && <Text size="sm" c="dimmed">Stopped after {j.epoch} of {epochs} passes. Resume to carry on from there.</Text>}
            {j.status === JobStatus.SUCCEEDED && <Text size="sm" c="dimmed">Finished {ago(j.endTime)} after {epochs} passes.</Text>}
          </Group>
          {last && last.accuracy > 0 && <Title order={3} fz="lg">{score(last.accuracy)}</Title>}
        </Group>
        {active(j) && <Progress value={progress(j)} size="lg" radius="xl" animated={j.status === JobStatus.RUNNING} aria-label="Progress" mb="md" />}
        {j.status === JobStatus.FAILED && <Alert color="red" icon={<IconAlertCircle />} mb="md">{j.error}</Alert>}
        {j.status === JobStatus.SUCCEEDED && (
          <Alert color="teal" mb="md">
            The model is ready. It {score(j.accuracy)} on {j.settings && j.settings.holdOut > 0 ? "examples it never saw while learning" : "the examples it learned from"}.{" "}
            <Link to="/models/$id" params={{ id: j.modelId }}>See the model</Link>.
          </Alert>
        )}
        <TrainingChart history={j.history} epochs={epochs} />
      </Card>

      <SimpleGrid cols={{ base: 1, md: 2 }}>
        <Card withBorder radius="lg" padding="lg">
          <Title order={3} fz="lg" mb="sm">Each pass</Title>
          {j.history.length === 0 ? <Text c="dimmed" size="sm">Nothing yet.</Text> : (
            <Table verticalSpacing="xs" fz="sm">
              <Table.Thead><Table.Tr><Table.Th>Pass</Table.Th><Table.Th>Right answers</Table.Th><Table.Th>Loss</Table.Th><Table.Th>Took</Table.Th></Table.Tr></Table.Thead>
              <Table.Tbody>
                {j.history.map((e) => (
                  <Table.Tr key={e.epoch}>
                    <Table.Td>{e.epoch}</Table.Td>
                    <Table.Td>{e.accuracy > 0 ? `${(e.accuracy * 100).toFixed(1)}%` : "–"}</Table.Td>
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
            {j.description && <Fact label="Description" value={j.description} />}
          </Stack>
        </Card>
      </SimpleGrid>
    </Page>
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
