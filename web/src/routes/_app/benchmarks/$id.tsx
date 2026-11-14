import { useMutation } from "@connectrpc/connect-query";
import { Alert, Badge, Button, Card, Group, Loader, Modal, SimpleGrid, Stack, Table, Text, Title } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { IconAlertCircle, IconPlus } from "@tabler/icons-react";
import { useQueryClient, useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useState } from "react";

import { message } from "../../../api/client";
import { BenchmarkService, type BenchmarkScore } from "../../../api/gen/mimir/v1/benchmark_pb";
import { JobStatus } from "../../../api/gen/mimir/v1/training_pb";
import { allBenchmarkQueries, getBenchmark } from "../../../api/queries";
import { AnchorLink, ButtonLink } from "../../../components/links";
import { Page } from "../../../components/Page";
import { StatTile } from "../../../components/ui";
import { RunReport } from "../../../components/RunReport";
import { behind, headlineText, medal, metricHint, metricTitle } from "../../../lib/benchmark";
import { ago } from "../../../lib/format";
import { t } from "../../../i18n";

export const Route = createFileRoute("/_app/benchmarks/$id")({
  loader: ({ context, params }) => context.queryClient.ensureQueryData(getBenchmark(params.id)),
  errorComponent: ({ error }) => (
    <Alert color="red" icon={<IconAlertCircle />} className="mx-auto max-w-5xl">
      {message(error)} <Link to="/benchmarks">{t("Back to benchmarks")}</Link>
    </Alert>
  ),
  component: Leaderboard,
});

const waiting = (s: BenchmarkScore) => s.status === JobStatus.QUEUED || s.status === JobStatus.RUNNING;

function Leaderboard() {
  const { id } = Route.useParams();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { data } = useSuspenseQuery({
    ...getBenchmark(id),
    refetchInterval: (q) => (q.state.data?.scores.some(waiting) ? 1500 : false),
  });
  const b = data.benchmark!;
  const [adding, setAdding] = useState(false);
  const [open, setOpen] = useState<string>();
  const refresh = () => queryClient.invalidateQueries({ queryKey: allBenchmarkQueries });
  const score = useMutation(BenchmarkService.method.scoreModels, {
    onSuccess: async () => { await refresh(); setAdding(false); },
  });
  const remove = useMutation(BenchmarkService.method.deleteBenchmark, {
    onSuccess: async () => { await navigate({ to: "/benchmarks" }); await refresh(); notifications.show({ message: t("Benchmark deleted.") }); },
  });
  const shown = data.scores.find((s) => s.modelId === open);

  return (
    <Page
      title={b.name}
      crumbs={[{ label: t("Benchmarks"), to: "/benchmarks" }, { label: b.name }]}
      intro={<>{b.rows.toLocaleString()}{" "}{t("rows kept aside from")}{" "}{b.datasetName || t("deleted examples")}{t(", answering")}{" "}{b.target}{" "}{t("· made by")}{" "}{b.createdBy} {ago(b.createTime)}</>}
      actions={
        <Group gap="xs">
          {data.candidates.length > 0 && <Button leftSection={<IconPlus size={18} />} onClick={() => setAdding(true)}>{t("Score a model")}</Button>}
          <Button variant="subtle" color="red" loading={remove.isPending} onClick={() => window.confirm(t("Delete {name}? Its scores go too; the models stay.", { name: b.name })) && remove.mutate({ id })}>{t("Delete")}</Button>
        </Group>
      }
    >
      {(score.error ?? remove.error) && <Alert color="red" icon={<IconAlertCircle />}>{message(score.error ?? remove.error)}</Alert>}
      {b.description && <Text>{b.description}</Text>}
      {data.scores.some((s) => s.rank === 1) && (
        <SimpleGrid cols={{ base: 1, sm: 3 }}>
          <StatTile value={headlineText(b, data.scores.find((s) => s.rank === 1)!.headline)} label={t("{modelName}, the best so far", { modelName: data.scores.find((s) => s.rank === 1)!.modelName })} />
          <StatTile value={String(data.scores.length)} label={data.scores.length === 1 ? t("model scored") : t("models scored")} />
          <StatTile value={b.rows.toLocaleString()} label={t("rows in the test")} hint={t("Kept out of training")} />
        </SimpleGrid>
      )}
      <section>
        <Title order={2}>{t("Leaderboard")}</Title>
        <Text size="sm" c="dimmed" mb="sm">{metricHint(b)}{" "}{t("Every model answers the same rows, none of which it learned from.")}</Text>
        <Card withBorder padding={data.scores.length === 0 ? "lg" : 0}>
        {data.scores.length === 0 ? (
          <Text size="sm">{t("No model has taken this test yet. Score one, or teach a new model from")}{" "}{b.datasetName || t("these examples")}{" "}{t("and it is scored on its own.")}</Text>
        ) : (
          <Table.ScrollContainer minWidth={320}>
            <Table highlightOnHover>
              <Table.Thead>
                <Table.Tr>
                  <Table.Th w={60}>{t("Place")}</Table.Th>
                  <Table.Th>{t("Model")}</Table.Th>
                  <Table.Th>{metricTitle(b)}</Table.Th>
                  <Table.Th visibleFrom="sm">{t("Behind the best")}</Table.Th>
                  <Table.Th visibleFrom="sm">{t("Scored")}</Table.Th>
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {data.scores.map((s) => (
                  <Table.Tr key={s.modelId}>
                    <Table.Td>{s.rank > 0 ? <>{medal(s.rank)} {s.rank}</> : "—"}</Table.Td>
                    <Table.Td>
                      <Group gap="xs" wrap="nowrap">
                        <AnchorLink truncate fw={600} to="/models/$id" params={{ id: s.modelId }} c="inherit">{s.modelName}</AnchorLink>
                        {s.stale && <Badge color="yellow" variant="light" title={t("It has learned more since this score")}>{t("Learned since")}</Badge>}
                      </Group>
                      <Text size="xs" c="dimmed">{s.architecture}</Text>
                    </Table.Td>
                    <Table.Td>
                      {waiting(s) ? <Group gap={6}><Loader size={14} /><Text size="sm" c="dimmed">{s.status === JobStatus.QUEUED ? t("Waiting") : t("Scoring")}</Text></Group>
                        : s.status === JobStatus.FAILED ? <Text size="sm" c="red">{s.error}</Text>
                          : <Group gap="xs"><Text fw={600}>{headlineText(b, s.headline)}</Text>
                            {s.report && <Button size="compact-xs" variant="subtle" onClick={() => setOpen(s.modelId)}>{t("How it did")}</Button>}</Group>}
                    </Table.Td>
                    <Table.Td visibleFrom="sm"><Text size="sm" c="dimmed">{behind(b, data.scores, s) || "—"}</Text></Table.Td>
                    <Table.Td visibleFrom="sm"><Text size="sm" c="dimmed">{s.scoredTime ? ago(s.scoredTime) : "—"}</Text></Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        )}
        </Card>
      </section>
      <Modal opened={adding} onClose={() => setAdding(false)} title={t("Score a model")} radius="lg" centered>
        <Stack gap="sm">
          <Text size="sm" c="dimmed">{t("These models answer the same kind of question and read the columns this test has.")}</Text>
          {data.candidates.map((c) => (
            <Group key={c.modelId} justify="space-between" wrap="nowrap">
              <div className="min-w-0">
                <Text fw={600} truncate>{c.modelName}</Text>
                <Text size="xs" c="dimmed">{c.architecture}</Text>
              </div>
              <Button size="compact-sm" loading={score.isPending} onClick={() => score.mutate({ benchmarkId: id, modelIds: [c.modelId] })}>{t("Score it")}</Button>
            </Group>
          ))}
        </Stack>
      </Modal>
      <Modal opened={shown !== undefined} onClose={() => setOpen(undefined)} title={shown ? t("How {modelName} did", { modelName: shown.modelName }) : ""} radius="lg" size="lg" centered>
        {shown?.report && <RunReport report={shown.report} />}
      </Modal>
      <Text size="sm" c="dimmed">{t("Teaching a new model from")}{" "}{b.datasetName || t("these examples")}{" "}{t("scores it here on its own.")}{" "}<ButtonLink variant="subtle" size="compact-sm" to="/training">{t("Go to training")}</ButtonLink>
      </Text>
    </Page>
  );
}
