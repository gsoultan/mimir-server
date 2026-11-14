import { timestampDate } from "@bufbuild/protobuf/wkt";
import { useMutation } from "@connectrpc/connect-query";
import { Alert, Badge, Button, Card, Code, Group, Modal, RingProgress, SegmentedControl, Select, SimpleGrid, Stack, Table, Text, Title, Tooltip } from "@mantine/core";
import { useDisclosure } from "@mantine/hooks";
import { notifications } from "@mantine/notifications";
import { IconAlertCircle, IconArrowLeft, IconKey, IconLock, IconLockOpen, IconTrash } from "@tabler/icons-react";
import { keepPreviousData, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { z } from "zod";

import { message } from "../../../api/client";
import { WorkerService, type GetWorkerResponse, type Worker } from "../../../api/gen/mimir/v1/worker_pb";
import { allModelQueries, allWorkerQueries, getWorker, listArchitectures } from "../../../api/queries";
import { AreaChart } from "../../../components/AreaChart";
import { AnchorLink } from "../../../components/links";
import { Page } from "../../../components/Page";
import { StatusPill } from "../../../components/ui";
import { TokenModal } from "../../../components/Workers";
import { ago, bytes, duration, loadColor, share } from "../../../lib/format";
import { t } from "../../../i18n";

const ranges = [
  { value: "1", label: t("Last hour") },
  { value: "24", label: t("Last day") },
  { value: "168", label: t("Last week") },
];

export const Route = createFileRoute("/_app/workers/$id")({
  validateSearch: z.object({ hours: z.enum(["1", "24", "168"]).optional() }),
  loaderDeps: ({ search }) => ({ hours: Number(search.hours ?? "1") }),
  loader: ({ context, params, deps }) => Promise.all([
    context.queryClient.ensureQueryData(getWorker(params.id, deps.hours)),
    context.queryClient.ensureQueryData(listArchitectures),
  ]),
  errorComponent: ({ error }) => (
    <Alert color="red" icon={<IconAlertCircle />} className="mx-auto max-w-5xl">
      {message(error)} <Link to="/settings" search={{ tab: "workers" }}>{t("Back to workers")}</Link>
    </Alert>
  ),
  component: WorkerPage,
});

function WorkerPage() {
  const { id } = Route.useParams();
  const { hours: h } = Route.useSearch();
  const hours = Number(h ?? "1");
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { data } = useQuery({ ...getWorker(id, hours), refetchInterval: 5000, placeholderData: keepPreviousData });
  const { data: archs } = useQuery(listArchitectures);
  const [confirming, confirm] = useDisclosure();
  const [replacing, replace] = useDisclosure();
  const [token, setToken] = useState<string>();
  const refresh = () => queryClient.invalidateQueries({ queryKey: allWorkerQueries });
  const remove = useMutation(WorkerService.method.deleteWorker, {
    onSuccess: async () => {
      await navigate({ to: "/settings", search: { tab: "workers" } });
      await refresh();
      await queryClient.invalidateQueries({ queryKey: allModelQueries });
      notifications.show({ message: t("Worker removed. Its token no longer works, and its models run wherever there's room.") });
    },
  });
  const newToken = useMutation(WorkerService.method.replaceWorkerToken, {
    onSuccess: async (res) => { replace.close(); setToken(res.token); await refresh(); },
  });
  const setTasks = useMutation(WorkerService.method.setWorkerTasks, {
    onSuccess: async (res) => {
      await refresh();
      await queryClient.invalidateQueries({ queryKey: allModelQueries });
      const w = res.worker;
      notifications.show({ color: "teal", message: w?.tasks === "train" ? t("{name} now only teaches. Models that ran only there run wherever there's room.", { name: w.name }) : w?.tasks === "serve" ? t("{name} now only answers. Runs that waited for it go to any worker that teaches.", { name: w.name }) : w?.tasks === "all" ? t("{name} now runs and teaches.", { name: w.name }) : t("{name} takes what it was started with.", { name: w?.name }) });
    },
  });
  if (!data?.worker) return null;
  const w = data.worker;
  const s = w.stats;
  const title = (a: string) => archs?.architectures.find((x) => x.name === a)?.title ?? a;
  const full = w.online && w.busy >= w.capacity;

  return (
    <Page
      title={w.name}
      crumbs={[{ label: t("Settings"), to: "/settings" }, { label: t("Workers") }, { label: w.name }]}
      badge={<StatusPill tone={!w.online ? "idle" : full ? "busy" : "ok"}>{!w.online ? t("Offline") : full ? t("Busy") : t("Online")}</StatusPill>}
      intro={w.online
        ? `${t("Connected")}${w.connectTime ? t(" for {n1}", { n1: duration((Date.now() - timestampDate(w.connectTime).getTime()) / 1000) }) : ""} · ${w.device}${w.gpus > 1 ? t(" · {gpus} GPUs, each run on all of them", { gpus: w.gpus }) : ""}${w.version ? t(" · version {version}", { version: w.version }) : ""}`
        : w.lastSeen ? t("Offline since {lastSeen}", { lastSeen: ago(w.lastSeen) }) : t("Hasn't connected yet")}
      actions={
        <Group>
          <Button variant="subtle" color="gray" leftSection={<IconArrowLeft size={16} />} onClick={() => navigate({ to: "/settings", search: { tab: "workers" } })}>{t("All workers")}</Button>
          <Button variant="default" leftSection={<IconKey size={16} />} onClick={replace.open}>{t("New token")}</Button>
          <Button variant="subtle" color="red" leftSection={<IconTrash size={16} />} onClick={confirm.open}>{t("Remove")}</Button>
        </Group>
      }
    >
      {!w.online && (w.models > 0 || w.waiting > 0) && (
        <Alert color="yellow" icon={<IconAlertCircle />}>{t("While it's offline, {what}. Start it again, or move them to another worker.", {
          what: [
            w.models > 0 ? (w.models === 1 ? t("1 model can't answer") : t("{n} models can't answer", { n: w.models })) : "",
            w.waiting > 0 ? (w.waiting === 1 ? t("1 run waits") : t("{n} runs wait", { n: w.waiting })) : "",
          ].filter(Boolean).join(t(" and ")),
        })}</Alert>
      )}

      <SimpleGrid cols={{ base: 2, sm: 4 }}>
        <Gauge label="CPU" pct={s?.cpuPercent ?? 0} off={!w.online} detail={w.online ? t("{cpus} CPUs{n2}", { cpus: w.cpus, n2: s && s.loadAverage > 0 ? t(" · load {n1}", { n1: s.loadAverage.toFixed(1) }) : "" }) : t("offline")} />
        <Gauge label={t("Memory")} pct={share(s?.memoryUsedBytes ?? 0n, s?.memoryTotalBytes ?? 0n)} off={!w.online} detail={s && w.online ? t("{n} of {total}", { n: bytes(s.memoryUsedBytes), total: bytes(s.memoryTotalBytes) }) : t("offline")} />
        <Gauge label={t("Storage")} pct={share(s?.diskUsedBytes ?? 0n, s?.diskTotalBytes ?? 0n)} off={!w.online || !s?.diskTotalBytes} detail={s && w.online && s.diskTotalBytes > 0n ? t("{n1} free of {diskTotalBytes}", { n1: bytes(s.diskTotalBytes - s.diskUsedBytes), diskTotalBytes: bytes(s.diskTotalBytes) }) : w.online ? t("not reported") : t("offline")} />
        <Gauge label={t("Tasks")} pct={share(w.busy, w.capacity)} off={!w.online} center={w.online ? `${w.busy}/${w.capacity}` : undefined} detail={w.online ? t("{busy} running, room for {capacity}", { busy: w.busy, capacity: w.capacity }) : t("offline")} />
      </SimpleGrid>

      <History data={data} hours={hours} onHours={(v) => navigate({ to: ".", search: { hours: v as "1" }, replace: true })} />

      <SimpleGrid cols={{ base: 1, md: 2 }}>
        <Card withBorder padding="lg">
          <Title order={2} mb="sm">{t("Running now")}</Title>
          {data.tasks.length === 0 ? <Text size="sm" c="dimmed">{w.online ? t("Nothing right now. It's ready for work.") : t("Nothing, it's offline.")}</Text> : (
            <Stack gap="xs">
              {data.tasks.map((task) => (
                <Group key={task.kind + task.jobId + task.modelId} justify="space-between" wrap="nowrap">
                  <Text size="sm">
                    {task.kind === "train"
                      ? <>{t("Teaching")}{" "}<AnchorLink to="/training/$id" params={{ id: task.jobId }} fw={600}>{task.jobName || t("a run")}</AnchorLink></>
                      : <>{t("Answering")}{" "}<AnchorLink to="/models/$id" params={{ id: task.modelId }} fw={600}>{task.modelName || t("a model")}</AnchorLink>{task.count > 1 && ` ×${task.count}`}</>}
                  </Text>
                  <Text size="xs" c="dimmed">{task.startTime && t("for {n1}", { n1: duration((Date.now() - timestampDate(task.startTime).getTime()) / 1000) })}</Text>
                </Group>
              ))}
            </Stack>
          )}
          {w.online && w.architectures.length > 0 && (
            <Text size="xs" c="dimmed" mt="md">{w.trainingOnly ? t("Teaches only") : w.trains ? t("Runs and teaches") : t("Runs only")}: {w.architectures.map(title).join(", ")}</Text>
          )}
        </Card>

        <Card withBorder padding="lg">
          <Title order={2} mb="sm">{t("What it takes")}</Title>
          <Text size="sm" c="dimmed" mb="sm">{t("Keep training off the machines that answer: a worker that only teaches gets no predictions, and one that only answers gets no training runs. The change applies at once; work already on it finishes.")}</Text>
          <Select
            label={t("Work for this worker")}
            value={w.tasks}
            onChange={(v) => setTasks.mutate({ id: w.id, tasks: v ?? "" })}
            disabled={setTasks.isPending}
            allowDeselect={false}
            data={[
              { value: "", label: t("As started (its -tasks flag)") },
              { value: "all", label: t("Runs and teaches") },
              { value: "train", label: t("Teaches only") },
              { value: "serve", label: t("Answers only") },
            ]}
          />
          {w.online && w.tasks === "train" && !w.trains && <Alert color="yellow" icon={<IconAlertCircle />} mt="sm">{t("This worker can't teach, so set to teach only it does nothing. Pick another choice.")}</Alert>}
          {setTasks.error && <Alert color="red" icon={<IconAlertCircle />} mt="sm">{message(setTasks.error)}</Alert>}
        </Card>

        <Card withBorder padding="lg">
          <Title order={2} mb="sm">{t("Placed here")}</Title>
          {data.models.length === 0 && data.runs.length === 0 && (
            <Text size="sm" c="dimmed">{t("No models or runs are set to use only this worker. It takes work for any model set to Automatic.")}</Text>
          )}
          {data.models.length > 0 && (
            <>
              <Text size="xs" c="dimmed" tt="uppercase" fw={600} mb={4}>{t("Models that run only here")}</Text>
              <Group gap="xs" mb="sm">
                {data.models.map((m) => <Link key={m.id} to="/models/$id" params={{ id: m.id }}><Badge variant="light" className="cursor-pointer normal-case">{m.name}</Badge></Link>)}
              </Group>
            </>
          )}
          {data.runs.length > 0 && (
            <>
              <Text size="xs" c="dimmed" tt="uppercase" fw={600} mb={4}>{t("Training runs")}</Text>
              <Stack gap={4}>
                {data.runs.map((r) => (
                  <Group key={r.id} justify="space-between" wrap="nowrap">
                    <AnchorLink to="/training/$id" params={{ id: r.id }} size="sm" truncate>{r.name}</AnchorLink>
                    <Group gap="xs" wrap="nowrap">
                      <Badge size="sm" variant="light" color={runColor[r.status] ?? "gray"}>{runWord[r.status] ?? r.status}</Badge>
                      <Text size="xs" c="dimmed" className="whitespace-nowrap">{ago(r.time)}</Text>
                    </Group>
                  </Group>
                ))}
              </Stack>
            </>
          )}
        </Card>
      </SimpleGrid>

      <Connection w={w} data={data} />

      <Modal opened={replacing} onClose={replace.close} title={t("New token for {name}?", { name: w.name })} centered>
        <Text mb="sm">{t("The old token stops working at once and")}{" "}{w.name}{" "}{t("disconnects. The console also forgets the key it joined with, so the machine you run the new token on becomes")}{" "}{w.name}.</Text>
        <Text mb="lg" size="sm" c="dimmed">{t("Do this if the token may have leaked, or the machine was rebuilt.")}</Text>
        {newToken.error && <Alert color="red" mb="md" icon={<IconAlertCircle />}>{message(newToken.error)}</Alert>}
        <Group justify="flex-end">
          <Button variant="default" onClick={replace.close}>{t("Cancel")}</Button>
          <Button loading={newToken.isPending} onClick={() => newToken.mutate({ id })}>{t("Make a new token")}</Button>
        </Group>
      </Modal>
      <Modal opened={confirming} onClose={confirm.close} title={t("Remove {name}?", { name: w.name })} centered>
        <Text mb="lg">{t("Its token stops working and it disconnects. Models and runs placed on it go back to Automatic.")}</Text>
        {remove.error && <Alert color="red" mb="md" icon={<IconAlertCircle />}>{message(remove.error)}</Alert>}
        <Group justify="flex-end">
          <Button variant="default" onClick={confirm.close}>{t("Keep it")}</Button>
          <Button color="red" loading={remove.isPending} onClick={() => remove.mutate({ id })}>{t("Remove")}</Button>
        </Group>
      </Modal>
      <TokenModal token={token} onClose={() => setToken(undefined)} />
    </Page>
  );
}

const runWord: Record<string, string> = { queued: t("Waiting"), running: t("Learning"), succeeded: t("Done"), failed: t("Failed"), cancelled: t("Stopped") };
const runColor: Record<string, string> = { queued: "gray", running: "blue", succeeded: "teal", failed: "red", cancelled: "gray" };

function Gauge({ label, pct, detail, off, center }: { label: string; pct: number; detail: string; off?: boolean; center?: string }) {
  const color = off ? "gray" : loadColor(pct);
  return (
    <Card withBorder padding="md">
      <div className="flex flex-col items-center gap-2 text-center sm:flex-row sm:gap-3 sm:text-left">
        <RingProgress
          size={84} thickness={9} roundCaps className="shrink-0"
          sections={off ? [] : [{ value: Math.min(pct, 100), color }]}
          label={<Text ta="center" fw={700} size="sm">{off ? "–" : center ?? `${Math.round(pct)}%`}</Text>}
          aria-label={`${label} ${Math.round(pct)}%`}
        />
        <div className="min-w-0">
          <Text size="xs" c="dimmed" tt="uppercase" fw={600} lts={0.4}>{label}</Text>
          <Text size="sm" fw={500} lh={1.3}>{detail}</Text>
        </div>
      </div>
    </Card>
  );
}

function History({ data, hours, onHours }: { data: GetWorkerResponse; hours: number; onHours: (v: string) => void }) {
  const at = (sample: (typeof data.samples)[number]) => (sample.time ? timestampDate(sample.time).getTime() : 0);
  const span = hours * 3600_000;
  const series = {
    cpu: data.samples.map((x) => ({ t: at(x), v: x.cpuPercent })),
    mem: data.samples.map((x) => ({ t: at(x), v: share(x.memoryUsedBytes, x.memoryTotalBytes) })),
    disk: data.samples.map((x) => ({ t: at(x), v: share(x.diskUsedBytes, x.diskTotalBytes) })),
    busy: data.samples.map((x) => ({ t: at(x), v: x.busy })),
  };
  const pct = (v: number) => `${Math.round(v)}%`;
  return (
    <Card withBorder padding="lg">
      <Group justify="space-between" mb="md">
        <div>
          <Title order={2}>{t("Over time")}</Title>
          <Text size="xs" c="dimmed">{t("One reading a minute, kept for a week. Hover a chart to read it.")}</Text>
        </div>
        <SegmentedControl size="xs" value={String(hours)} onChange={onHours} data={ranges} />
      </Group>
      <SimpleGrid cols={{ base: 1, md: 2 }} spacing="lg">
        <Chart title="CPU"><AreaChart points={series.cpu} max={100} span={span} color="var(--mantine-color-teal-6)" format={pct} label={t("CPU use over time")} /></Chart>
        <Chart title={t("Memory")}><AreaChart points={series.mem} max={100} span={span} color="var(--mantine-color-blue-6)" format={pct} label={t("Memory use over time")} /></Chart>
        <Chart title={t("Storage")}><AreaChart points={series.disk} max={100} span={span} color="var(--mantine-color-violet-6)" format={pct} label={t("Storage use over time")} /></Chart>
        <Chart title={t("Tasks running")}><AreaChart points={series.busy} span={span} color="var(--mantine-color-orange-6)" format={(v) => String(Math.round(v * 10) / 10)} label={t("Tasks running over time")} /></Chart>
      </SimpleGrid>
    </Card>
  );
}

function Chart({ title, children }: { title: string; children: React.ReactNode }) {
  return <div><Text size="sm" fw={600} mb={4}>{title}</Text>{children}</div>;
}

const eventWord: Record<string, string> = {
  "worker.join": t("Joined for the first time"),
  "worker.refused": t("Refused a connection"),
  "worker.address": t("Connected from a new address"),
  "worker.token": t("New token made"),
  "worker.create": t("Added"),
};

const security: Record<string, { label: string; good: boolean; hint: string }> = {
  tls: { label: t("Encrypted"), good: true, hint: t("The console serves TLS itself.") },
  proxy: { label: t("Encrypted by your proxy"), good: true, hint: t("Your https proxy encrypts the link to this console.") },
  local: { label: t("Same machine"), good: true, hint: t("The worker runs on the console's own machine, so nothing crosses the network.") },
  plain: { label: t("Not encrypted"), good: false, hint: t("Allowed with -allow-plain-workers. Use only on a network you trust.") },
};

function Connection({ w, data }: { w: Worker; data: GetWorkerResponse }) {
  const sec = security[w.security];
  return (
    <Card withBorder padding="lg">
      <Title order={2} mb="sm">{t("Connection")}</Title>
      <SimpleGrid cols={{ base: 1, md: 2 }} spacing="xl">
        <Table withRowBorders={false} verticalSpacing={6} horizontalSpacing={0}>
          <Table.Tbody>
            <Row label={t("Link")}>
              {sec ? (
                <Tooltip label={sec.hint} multiline w={260}>
                  <Badge variant="light" color={sec.good ? "teal" : "red"} leftSection={sec.good ? <IconLock size={12} /> : <IconLockOpen size={12} />}>
                    {sec.label}{w.tlsVersion && ` · ${w.tlsVersion}`}
                  </Badge>
                </Tooltip>
              ) : <Text size="sm" c="dimmed">{t("Not connected")}</Text>}
            </Row>
            <Row label={t("Its key")}>
              {w.keyFingerprint
                ? <Tooltip label={t("Only the machine holding this key can use the token. It proves it on every connection.")} multiline w={260}><Code>{w.keyFingerprint}</Code></Tooltip>
                : <Text size="sm" c="dimmed">{t("Not joined yet")}</Text>}
            </Row>
            <Row label={t("Connects from")}><Text size="sm">{w.address || "–"}</Text></Row>
            {w.connectTime && <Row label={t("Connected")}><Text size="sm">{ago(w.connectTime)}</Text></Row>}
            <Row label={t("Added")}><Text size="sm">{t("by")}{" "}{w.createdBy} {ago(w.createTime)}</Text></Row>
          </Table.Tbody>
        </Table>
        <div>
          <Text size="xs" c="dimmed" tt="uppercase" fw={600} mb={6}>{t("Recent security events")}</Text>
          {data.events.length === 0 ? <Text size="sm" c="dimmed">{t("None yet.")}</Text> : (
            <Stack gap={6}>
              {data.events.map((e) => (
                <div key={e.id}>
                  <Group gap="xs" wrap="nowrap">
                    <span className={`inline-block h-2 w-2 shrink-0 rounded-full ${e.name === "worker.refused" ? "bg-[var(--mantine-color-red-6)]" : "bg-[var(--mantine-color-gray-5)]"}`} />
                    <Text size="sm" fw={500}>{eventWord[e.name] ?? e.name}</Text>
                    <Text size="xs" c="dimmed" className="whitespace-nowrap">{ago(e.time)}</Text>
                  </Group>
                  {e.status && <Text size="xs" c="dimmed" ml={16}>{e.status}</Text>}
                </div>
              ))}
            </Stack>
          )}
        </div>
      </SimpleGrid>
    </Card>
  );
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <Table.Tr>
      <Table.Td w={130}><Text size="sm" c="dimmed">{label}</Text></Table.Td>
      <Table.Td>{children}</Table.Td>
    </Table.Tr>
  );
}
