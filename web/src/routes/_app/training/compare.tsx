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
import { bytes } from "../../../lib/format";
import { number as num } from "../../../lib/report";
import { active, score, statusColor, statusWord } from "../../../lib/training";
import { locale, t } from "../../../i18n";

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
      {message(error)} <Link to="/training">{t("Back to training")}</Link>
    </Alert>
  ),
  component: Compare,
});

function Compare() {
  const ids = parseIds(Route.useSearch().ids);
  if (ids.length < 2) {
    return (
      <Page title={t("Compare runs")} crumbs={[{ label: t("Training"), to: "/training" }, { label: t("Compare") }]} intro={t("See how runs did side by side.")}>
        <Card withBorder radius="lg" padding="xl" className="text-center">
          <Text fw={600}>{t("Pick two or more runs first")}</Text>
          <Text c="dimmed" mb="md">{t("Tick the runs you want on the training page, then choose Compare.")}</Text>
          <Group justify="center"><ButtonLink to="/training">{t("Go to training")}</ButtonLink></Group>
        </Card>
      </Page>
    );
  }
  return <Runs ids={ids} />;
}

const round = (n: number) => n.toLocaleString(locale, { maximumFractionDigits: 4 });

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
    { label: t("Score"), cell: (j) => (j.status === JobStatus.SUCCEEDED ? score(j.accuracy, isNumber(j)) : <Badge color={statusColor[j.status]} variant="light">{statusWord[j.status]}</Badge>) },
    { label: t("Best pass"), cell: (j) => { const b = bestPass(j); return b ? t("{n1}% at pass {epoch}", { n1: (b.accuracy * 100).toFixed(1), epoch: b.epoch }) : "–"; } },
    ...(anyNumber ? [{ label: t("Usually off by"), cell: (j: Job) => { const e = j.history.at(-1)?.typicalError ?? 0; return isNumber(j) && e > 0 ? round(e) : "–"; } }] : []),
    ...(jobs.some((j) => j.report && j.report.kind !== "number") ? [
      { label: t("Balanced score (macro F1)"), cell: (j: Job) => (j.report && j.report.kind !== "number" ? num(j.report.macroF1, 3) : "–") },
    ] : []),
    ...(jobs.some((j) => j.report?.kind === "ranked") ? [
      { label: t("Right one in its top picks"), cell: (j: Job) => (j.report?.kind === "ranked" ? t("{n1}% in top {k}", { n1: Math.round((j.report.hitAt.at(-1) ?? 0) * 100), k: j.report.k }) : "–") },
    ] : []),
    ...(jobs.some((j) => j.report?.kind === "number") ? [
      { label: t("Big misses count more (RMSE)"), cell: (j: Job) => (j.report?.kind === "number" ? num(j.report.rmse) : "–") },
      { label: t("Explains the answers (R²)"), cell: (j: Job) => (j.report?.kind === "number" ? num(j.report.r2, 3) : "–") },
    ] : []),
    { label: t("Answer time"), cell: (j) => (j.report ? `${num(j.report.millisPerExample, 2)} ms` : "–") },
    { label: t("Model size"), cell: (j) => (j.report ? bytes(j.report.sizeBytes) : "–") },
    { label: t("Time learning"), cell: (j) => (j.history.length > 0 ? took(trainingSeconds(j)) : "–") },
    { label: t("Per pass, on average"), cell: (j) => (j.history.length > 0 ? took(trainingSeconds(j) / j.history.length) : "–") },
    { label: t("Model type"), key: "architecture", cell: (j) => arch(j)?.title ?? j.architecture },
    { label: t("Examples"), key: "dataset", cell: (j) => j.datasetName || t("Deleted") },
    { label: t("Passes over the examples"), key: "epochs", cell: (j) => t("{n} of {total}", { n: j.history.length, total: j.settings?.epochs ?? 0 }) },
    { label: t("Examples per step"), key: "batch_size", cell: (j) => String(j.settings?.batchSize ?? "–") },
    { label: t("Learning rate"), key: "learning_rate", cell: (j) => round(j.settings?.learningRate ?? 0) },
    { label: t("Kept aside for scoring"), key: "hold_out", cell: (j) => `${Math.round((j.settings?.holdOut ?? 0) * 100)}%` },
    { label: t("Shape"), key: "layers", cell: (j) => shapeWord(j.settings?.layers ?? []) },
    { label: t("Trained on"), key: "worker", cell: (j) => (j.worker ? t("Worker {worker}", { worker: j.worker }) : t("This server")) },
    { label: t("Started"), cell: (j) => `${ago(j.createTime)} by ${j.createdBy}` },
  ];
  const best = jobs.find((j) => j.id === data.bestId);

  return (
    <Page
      title={t("Compare runs")}
      crumbs={[{ label: t("Training"), to: "/training" }, { label: t("Compare") }]}
      intro={best ? <>{best.name}{" "}{t("scored best: it")}{" "}{score(best.accuracy, isNumber(best))}.{differs.size > 0 && t(" Highlighted rows are the settings that differ.")}</> : t("None of these runs has finished yet.")}
      actions={<ButtonLink variant="default" to="/training">{t("Back to training")}</ButtonLink>}
    >
      <Card withBorder radius="lg" padding="lg">
        <Title order={3} fz="lg" mb="sm">{allNumber ? t("Close answers") : anyNumber ? t("Right or close answers") : t("Right answers")}{" "}{t("per pass")}</Title>
        <CompareChart jobs={jobs} number={allNumber} />
      </Card>

      <Card withBorder radius="lg" padding="lg">
        <Title order={3} fz="lg" mb="sm">{t("Side by side")}</Title>
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
                      {j.id === data.bestId && <Badge color="teal" variant="light" size="sm">{t("Best")}</Badge>}
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
                      {differ && <Text span size="xs" fw={600} c="var(--mantine-color-text)" ml={6}>{t("· differs")}</Text>}
                    </Table.Td>
                    {jobs.map((j) => <Table.Td key={j.id} fw={differ ? 600 : undefined}>{r.cell(j)}</Table.Td>)}
                  </Table.Tr>
                );
              })}
              <Table.Tr>
                <Table.Td />
                {jobs.map((j) => (
                  <Table.Td key={j.id}>
                    <Button size="xs" variant="default" leftSection={<IconRepeat size={16} />} disabled={!j.datasetName} title={j.datasetName ? undefined : t("Its examples were deleted")} onClick={() => setAgain(j)}>{t("Train again with these settings")}</Button>
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
    <Modal opened={!!job} onClose={onClose} title={t("Train again with these settings")} centered>
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
      notifications.show({ message: t("Training again with the same settings.") });
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
        <Text size="sm" c="dimmed">{t("A new")}{" "}{title}{" "}{t("learns from")}{" "}{job.datasetName}{" "}{t("with")}{" "}{s?.epochs}{" "}{t("passes,")}{" "}{s?.batchSize}{" "}{t("examples per step, a learning rate of")}{" "}{round(s?.learningRate ?? 0)}{" "}{t("and")}{" "}{Math.round((s?.holdOut ?? 0) * 100)}{t("% kept aside")}{s && s.layers.length > 0 ? t(", shaped {layers}", { layers: shapeWord(s.layers) }) : ""}{t(". It reads the examples as they are now.")}</Text>
        <TextInput label={t("Name")} data-autofocus required value={name} onChange={(e) => setName(e.currentTarget.value)} />
        {create.error && <Alert color="red" icon={<IconAlertCircle />}>{message(create.error)}</Alert>}
        <Group justify="flex-end">
          <Button variant="default" onClick={onDone}>{t("Cancel")}</Button>
          <Button type="submit" loading={create.isPending} disabled={!name.trim()}>{t("Start")}</Button>
        </Group>
      </Stack>
    </form>
  );
}
