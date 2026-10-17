import { useMutation } from "@connectrpc/connect-query";
import { Badge, Button, Card, Group, Progress, Stack, Text } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { IconSchool } from "@tabler/icons-react";
import { useQueryClient, useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";

import { TrainingService, JobStatus } from "../../../api/gen/mimir/v1/training_pb";
import { allTrainingQueries, listArchitectures, listJobs } from "../../../api/queries";
import { AnchorLink, ButtonLink } from "../../../components/links";
import { Page } from "../../../components/Page";
import { ago } from "../../../lib/format";
import { active, progress, repeatWord, score, statusColor, statusWord } from "../../../lib/training";

export const Route = createFileRoute("/_app/training/")({
  loader: ({ context }) => Promise.all([
    context.queryClient.ensureQueryData(listJobs),
    context.queryClient.ensureQueryData(listArchitectures),
  ]),
  component: Training,
});

function Training() {
  const { data } = useSuspenseQuery({ ...listJobs, refetchInterval: (q) => (q.state.data?.jobs.some(active) ? 2000 : false) });
  const { data: archs } = useSuspenseQuery(listArchitectures);
  const numbers = new Set(archs.architectures.filter((a) => a.output === "number").map((a) => a.name));
  const queryClient = useQueryClient();
  const refresh = () => queryClient.invalidateQueries({ queryKey: allTrainingQueries });
  const cancel = useMutation(TrainingService.method.cancelJob, { onSuccess: refresh });
  const resume = useMutation(TrainingService.method.resumeJob, { onSuccess: refresh });
  const remove = useMutation(TrainingService.method.deleteJob, { onSuccess: async () => { await refresh(); notifications.show({ message: "Run deleted." }); } });

  return (
    <Page
      title="Training"
      intro="Every model being taught here, newest first."
      actions={<ButtonLink to="/models/teach" leftSection={<IconSchool size={18} />}>Teach a new model</ButtonLink>}
    >
      {data.jobs.length === 0 ? (
        <Card withBorder radius="lg" padding="xl" className="text-center">
          <Text fw={600}>Nothing has been taught yet</Text>
          <Text c="dimmed" mb="md">Upload a table of examples or use the built-in handwritten digits, and watch a model learn.</Text>
          <Group justify="center"><ButtonLink to="/models/teach">Teach a new model</ButtonLink></Group>
        </Card>
      ) : (
        <Stack gap="sm">
          {data.jobs.map((j) => (
            <Card key={j.id} withBorder radius="lg" padding="md">
              <Group justify="space-between" wrap="nowrap" align="flex-start">
                <div className="min-w-0 flex-1">
                  <Group gap="xs" wrap="nowrap">
                    <AnchorLink fw={700} truncate to="/training/$id" params={{ id: j.id }} c="inherit">{j.name}</AnchorLink>
                    <Badge color={statusColor[j.status]} variant="light">{statusWord[j.status]}</Badge>
                    {j.repeatSeconds > 0n && <Badge color="grape" variant="light">Keeps learning {repeatWord(j.repeatSeconds)}</Badge>}
                  </Group>
                  <Text size="sm" c="dimmed">
                    From {j.datasetName || "deleted examples"} · {j.settings?.epochs} passes · started {ago(j.createTime)}
                    {j.status === JobStatus.SUCCEEDED && ` · ${score(j.accuracy, numbers.has(j.architecture))}`}
                  </Text>
                  {active(j) && <Progress value={progress(j)} mt="xs" size="sm" radius="xl" animated={j.status === JobStatus.RUNNING} aria-label="Progress" />}
                  {j.status === JobStatus.FAILED && <Text size="sm" c="red" mt={4}>{j.error}</Text>}
                  {j.note && <Text size="sm" mt={4}>{j.note}</Text>}
                </div>
                <Group gap="xs" wrap="nowrap">
                  {active(j) && <Button size="xs" variant="default" onClick={() => cancel.mutate({ id: j.id })}>Stop</Button>}
                  {(j.status === JobStatus.CANCELLED || j.status === JobStatus.FAILED) && <Button size="xs" variant="default" onClick={() => resume.mutate({ id: j.id })}>Resume</Button>}
                  {j.status === JobStatus.SUCCEEDED && <ButtonLink size="xs" to="/try" search={{ model: j.modelId }}>Try it</ButtonLink>}
                  {!active(j) && <Button size="xs" variant="subtle" color="red" onClick={() => remove.mutate({ id: j.id })}>Delete</Button>}
                </Group>
              </Group>
            </Card>
          ))}
        </Stack>
      )}
    </Page>
  );
}
