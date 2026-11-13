import { useMutation } from "@connectrpc/connect-query";
import { Alert, Button, Card, Group, Progress, Select, SimpleGrid, Stack, Table, Text, Title } from "@mantine/core";
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
import { StatTile, StatusPill } from "../../../components/ui";
import { ago, bytes } from "../../../lib/format";
import { progress, repeats, resultsURL, statusText, waiting } from "../../../lib/scoring";
import { t } from "../../../i18n";

export const Route = createFileRoute("/_app/scoring/$id")({
  loader: ({ context, params }) => context.queryClient.ensureQueryData(getScoring(params.id)),
  errorComponent: ({ error }) => (
    <Alert color="red" icon={<IconAlertCircle />} className="mx-auto max-w-5xl">
      {message(error)} <Link to="/scoring">{t("Back to scoring")}</Link>
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
      notifications.show({ message: t("{name} was deleted.", { name: s.name }) });
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
      crumbs={[{ label: t("Score a list"), to: "/scoring" }, { label: s.name }]}
      badge={<StatusPill tone={s.error || s.status === JobStatus.FAILED ? "bad" : busy ? "busy" : s.status === JobStatus.SUCCEEDED ? "ok" : "idle"}>{s.error || s.status === JobStatus.FAILED ? t("Failed") : busy ? t("Scoring") : s.status === JobStatus.SUCCEEDED ? t("Done") : t("Stopped")}</StatusPill>}
      intro={<>{t("Rows from")}{" "}{s.sourceDetail}{t(", answered by")}{" "}<AnchorLink to="/models/$id" params={{ id: s.modelId }}>{s.modelName}</AnchorLink>{" "}{t("· made by")}{" "}{s.createdBy} {ago(s.createTime)}</>}
      actions={
        <Group gap="xs">
          {s.resultBytes > 0n && (
            <Button component="a" href={resultsURL(s.id)} download leftSection={<IconDownload size={18} />}>{t("Download results")}</Button>
          )}
          {busy
            ? <Button variant="default" color="red" leftSection={<IconPlayerStop size={18} />} loading={cancel.isPending} onClick={() => cancel.mutate({ id })}>{t("Stop")}</Button>
            : <Button variant="default" leftSection={<IconPlayerPlay size={18} />} loading={run.isPending} onClick={() => run.mutate({ id })}>{fetched ? t("Fetch and score again") : t("Score again")}</Button>}
          <Button variant="subtle" color="red" leftSection={<IconTrash size={16} />} disabled={s.status === JobStatus.RUNNING} loading={remove.isPending} onClick={() => window.confirm(t("Delete {name} and its results?", { name: s.name })) && remove.mutate({ id })}>{t("Delete")}</Button>
        </Group>
      }
    >
      {error && <Alert color="red" icon={<IconAlertCircle />}>{message(error)}</Alert>}
      {Number(s.rows) > 0 && (
        <SimpleGrid cols={3} spacing={{ base: "xs", sm: "md" }}>
          <StatTile value={Number(s.done).toLocaleString()} label={t("rows answered")} hint={`of ${Number(s.rows).toLocaleString()}`} />
          <StatTile value={Number(s.failed).toLocaleString()} label={t("rows it couldn't answer")} hint={Number(s.failed) > 0 ? t("The problem column says why") : undefined} />
          <StatTile value={String(s.runs)} label={s.runs === 1 ? t("time scored") : t("times scored")} hint={s.endTime ? t("Last {endTime}", { endTime: ago(s.endTime) }) : undefined} />
        </SimpleGrid>
      )}
      {(busy || !!s.error || fetched || Number(s.rows) === 0) && <Card withBorder padding="lg">
        <Stack gap="xs">
          <Text fw={650}>{statusText(s)}</Text>
          {busy && <Progress value={progress(s)} color="violet" animated />}
          {s.error && <Alert color="red" icon={<IconAlertCircle />}>{s.error}</Alert>}
          <Text size="sm" c="dimmed">
            {s.runs > 0 ? (s.runs === 1 ? t("Run once") : t("Run {runs} times", { runs: s.runs })) : t("Not finished yet")}
            {s.endTime ? t(" · last {endTime}", { endTime: ago(s.endTime) }) : ""}
            {s.resultBytes > 0n ? t(" · results {resultBytes}", { resultBytes: bytes(s.resultBytes) }) : ""}
            {s.repeatSeconds > 0n && s.nextRunTime ? t(" · next {nextRunTime}", { nextRunTime: ago(s.nextRunTime) }) : ""}
          </Text>
          {fetched && (
            <Select label={t("Score it again")} w={220} data={repeats} value={s.repeatSeconds.toString()} allowDeselect={false}
              onChange={(v) => v !== null && repeat.mutate({ id, repeatSeconds: BigInt(v) }, { onSuccess: () => notifications.show({ message: v === "0" ? t("It won't run again on its own.") : t("Saved.") }) })} />
          )}
        </Stack>
      </Card>}
      {data.sample.length > 0 && (
        <section>
          <Title order={2} mb="sm">{t("First rows")}</Title>
          <Card withBorder padding={0}>
          <Table.ScrollContainer minWidth={320}>
            <Table fz="sm" highlightOnHover>
              <Table.Thead>
                <Table.Tr>{s.columns.map((c, i) => <Table.Th key={c} c={firstOurs >= 0 && i >= firstOurs ? "var(--mantine-primary-color-light-color)" : undefined}>{c}</Table.Th>)}</Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {data.sample.map((r, i) => (
                  <Table.Tr key={i}>{r.values.map((v, k) => <Table.Td key={k} fw={k === firstOurs ? 600 : undefined}>{v}</Table.Td>)}</Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
          </Card>
          <Text size="sm" c="dimmed" mt="xs">{t("The whole list is in the download, every row in the order it came.")}</Text>
        </section>
      )}
    </Page>
  );
}
