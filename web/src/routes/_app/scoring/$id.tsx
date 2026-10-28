import { useMutation } from "@connectrpc/connect-query";
import { Alert, Button, Card, Group, Progress, Select, Stack, Table, Text, Title } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { IconAlertCircle, IconDownload, IconPlayerPlay, IconPlayerStop, IconTrash } from "@tabler/icons-react";
import { useQueryClient, useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";

import { message } from "../../../api/client";
import { ScoringService } from "../../../api/gen/mimir/v1/scoring_pb";
import { JobStatus } from "../../../api/gen/mimir/v1/training_pb";
import { allScoringQueries, getScoring } from "../../../api/queries";
import { AnchorLink } from "../../../components/links";
import { Page } from "../../../components/Page";
import { ago, bytes } from "../../../lib/format";
import { progress, repeats, resultsURL, statusText, waiting } from "../../../lib/scoring";

export const Route = createFileRoute("/_app/scoring/$id")({
  loader: ({ context, params }) => context.queryClient.ensureQueryData(getScoring(params.id)),
  errorComponent: ({ error }) => (
    <Alert color="red" icon={<IconAlertCircle />} className="mx-auto max-w-5xl">
      {message(error)} <Link to="/scoring">Back to scoring</Link>
    </Alert>
  ),
  component: ScoringPage,
});

function ScoringPage() {
  const { id } = Route.useParams();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { data } = useSuspenseQuery({
    ...getScoring(id),
    refetchInterval: (q) => (q.state.data?.scoring && waiting(q.state.data.scoring) ? 1500 : false),
  });
  const s = data.scoring!;
  const refresh = () => queryClient.invalidateQueries({ queryKey: allScoringQueries });
  const run = useMutation(ScoringService.method.runScoring, { onSuccess: refresh });
  const cancel = useMutation(ScoringService.method.cancelScoring, { onSuccess: refresh });
  const repeat = useMutation(ScoringService.method.setScoringRepeat, { onSuccess: refresh });
  const remove = useMutation(ScoringService.method.deleteScoring, {
    onSuccess: async () => {
      await navigate({ to: "/scoring" });
      queryClient.removeQueries({ queryKey: getScoring(id).queryKey });
      await refresh();
      notifications.show({ message: `${s.name} was deleted.` });
    },
  });
  const error = run.error ?? cancel.error ?? repeat.error ?? remove.error;
  const busy = waiting(s);
  const fetched = s.source === "database" || s.source === "storage";
  // The answer's columns come after the rows' own, starting at the last
  // one named predicted (or predicted_2 when the rows had one).
  const firstOurs = s.columns.findLastIndex((c) => /^predicted(_\d+)?$/.test(c));

  return (
    <Page
      title={s.name}
      intro={<>Rows from {s.sourceDetail}, answered by <AnchorLink to="/models/$id" params={{ id: s.modelId }}>{s.modelName}</AnchorLink> · made by {s.createdBy} {ago(s.createTime)}</>}
      actions={
        <Group gap="xs">
          {s.resultBytes > 0n && (
            <Button component="a" href={resultsURL(s.id)} download leftSection={<IconDownload size={18} />}>Download results</Button>
          )}
          {busy
            ? <Button variant="default" color="red" leftSection={<IconPlayerStop size={18} />} loading={cancel.isPending} onClick={() => cancel.mutate({ id })}>Stop</Button>
            : <Button variant="default" leftSection={<IconPlayerPlay size={18} />} loading={run.isPending} onClick={() => run.mutate({ id })}>{fetched ? "Fetch and score again" : "Score again"}</Button>}
          <Button variant="subtle" color="red" leftSection={<IconTrash size={16} />} disabled={s.status === JobStatus.RUNNING} loading={remove.isPending} onClick={() => remove.mutate({ id })}>Delete</Button>
        </Group>
      }
    >
      {error && <Alert color="red" icon={<IconAlertCircle />}>{message(error)}</Alert>}
      <Card withBorder radius="lg" padding="lg">
        <Stack gap="xs">
          <Text fw={700}>{statusText(s)}</Text>
          {busy && <Progress value={progress(s)} animated />}
          {s.error && <Alert color="red" icon={<IconAlertCircle />}>{s.error}</Alert>}
          <Text size="sm" c="dimmed">
            {s.runs > 0 ? `Run ${s.runs} ${s.runs === 1 ? "time" : "times"}` : "Not finished yet"}
            {s.endTime ? ` · last ${ago(s.endTime)}` : ""}
            {s.resultBytes > 0n ? ` · results ${bytes(s.resultBytes)}` : ""}
            {s.repeatSeconds > 0n && s.nextRunTime ? ` · next ${ago(s.nextRunTime)}` : ""}
          </Text>
          {Number(s.failed) > 0 && !busy && <Text size="sm">Rows that couldn't be answered say why in the <b>problem</b> column.</Text>}
          {fetched && (
            <Select label="Score it again" w={220} data={repeats} value={s.repeatSeconds.toString()} allowDeselect={false}
              onChange={(v) => v !== null && repeat.mutate({ id, repeatSeconds: BigInt(v) }, { onSuccess: () => notifications.show({ message: v === "0" ? "It won't run again on its own." : "Saved." }) })} />
          )}
        </Stack>
      </Card>
      {data.sample.length > 0 && (
        <Card withBorder radius="lg" padding="lg">
          <Title order={3} fz="md" mb="xs">First rows</Title>
          <Table.ScrollContainer minWidth={320}>
            <Table fz="xs" withTableBorder>
              <Table.Thead>
                <Table.Tr>{s.columns.map((c, i) => <Table.Th key={c} c={firstOurs >= 0 && i >= firstOurs ? "teal" : undefined}>{c}</Table.Th>)}</Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {data.sample.map((r, i) => (
                  <Table.Tr key={i}>{r.values.map((v, k) => <Table.Td key={k} fw={k === firstOurs ? 600 : undefined}>{v}</Table.Td>)}</Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
          <Text size="sm" c="dimmed" mt="xs">The whole list is in the download, every row in the order it came.</Text>
        </Card>
      )}
    </Page>
  );
}
