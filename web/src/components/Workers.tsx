import { useMutation } from "@connectrpc/connect-query";
import { Alert, Badge, Button, Card, Code, CopyButton, Group, Modal, Progress, Select, SimpleGrid, Stack, Switch, Text, TextInput, Title } from "@mantine/core";
import { useDisclosure } from "@mantine/hooks";
import { notifications } from "@mantine/notifications";
import { IconAlertCircle, IconAlertTriangle, IconCheck, IconChevronRight, IconCopy, IconLock, IconLockOpen } from "@tabler/icons-react";
import { useQueryClient, useSuspenseQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";

import { message } from "../api/client";
import { SettingsService } from "../api/gen/mimir/v1/settings_pb";
import { WorkerService, type Worker } from "../api/gen/mimir/v1/worker_pb";
import { allSettingsQueries, allWorkerQueries, cloudGpu, listWorkers, settings } from "../api/queries";
import { ago, bytes, duration, loadColor, share } from "../lib/format";
import { CloudGpu } from "./CloudGpu";
import { StatusDot } from "./Placement";
import { t } from "../i18n";

// plainConsole says workers on other machines can't join this page's
// address, because it isn't https.
const plainConsole = () => location.protocol !== "https:" && !["localhost", "127.0.0.1", "[::1]"].includes(location.hostname);

// Workers is the overview of the machines that run models for this
// console. It refreshes while open so the numbers stay live.
export function Workers() {
  const { data } = useSuspenseQuery({ ...listWorkers, refetchInterval: 5000 });
  const { data: gpu } = useSuspenseQuery({ ...cloudGpu, refetchInterval: 5000 });
  // rented is the worker of the rented GPU, offline until a run
  // waits for it.
  const rented = gpu.cloudGpu?.enabled ? gpu.cloudGpu.workerId : "";
  const gpuProvider = gpu.providers.find((p) => p.id === gpu.cloudGpu?.provider)?.name ?? "";
  const queryClient = useQueryClient();
  const [adding, add] = useDisclosure(false);
  const [name, setName] = useState("");
  const [token, setToken] = useState<string>();
  const create = useMutation(WorkerService.method.createWorker, {
    onSuccess: async (res) => { await queryClient.invalidateQueries({ queryKey: allWorkerQueries }); add.close(); setName(""); setToken(res.token); },
  });
  const ws = data.workers;
  const online = ws.filter((w) => w.online);
  const busy = online.reduce((n, w) => n + w.busy, 0);
  const capacity = online.reduce((n, w) => n + w.capacity, 0);
  const avg = (f: (w: Worker) => number) => (online.length ? online.reduce((n, w) => n + f(w), 0) / online.length : 0);
  const cpu = avg((w) => w.stats?.cpuPercent ?? 0);
  const mem = avg((w) => share(w.stats?.memoryUsedBytes ?? 0n, w.stats?.memoryTotalBytes ?? 0n));
  const stranded = ws.filter((w) => !w.online && w.id !== rented && (w.models > 0 || w.waiting > 0));

  return (
    <Stack>
      <Group justify="space-between" align="flex-start">
        <div>
          <Title order={3} fz="lg">{t("Workers")}</Title>
          <Text c="dimmed" size="sm" maw={560}>{t("The console is this web app. Workers are the machines that run and teach the models. Add one to get its token and the command to run there.")}</Text>
        </div>
        <Button onClick={add.open}>{t("Add a worker")}</Button>
      </Group>

      {plainConsole() ? (
        <Alert color="yellow" icon={<IconLockOpen />} title={t("Workers on other machines can't join yet")}>{t("This console isn't served over https, so a worker elsewhere would send its token unencrypted. Start the console with")}{" "}<Code>-tls-auto</Code>{t(", give it a certificate with")}{" "}<Code>-tls-cert</Code>{" "}{t("and")}{" "}<Code>-tls-key</Code>{t(", or put it behind an https proxy.")}</Alert>
      ) : (
        <Group gap={6} c="dimmed">
          <IconLock size={16} />
          <Text size="sm">{t("Links are encrypted. Each worker proves it's the machine that first joined, and")}{" "}{data.secureJoin ? t("checks this console's fingerprint from its token") : t("checks this console's certificate")}.</Text>
        </Group>
      )}

      {ws.length > 0 && (
        <SimpleGrid cols={{ base: 2, sm: 4 }}>
          <Tile label={t("Online")} value={t("{n} of {total}", { n: online.length, total: ws.length })} hint={online.length === ws.length ? t("All connected") : t("{n1} offline", { n1: ws.length - online.length })} />
          <Tile label={t("Tasks running")} value={t("{n} of {total}", { n: busy, total: capacity })} bar={share(busy, capacity)} />
          <Tile label={t("Average CPU")} value={online.length ? `${Math.round(cpu)}%` : "–"} bar={cpu} />
          <Tile label={t("Average memory")} value={online.length ? `${Math.round(mem)}%` : "–"} bar={mem} />
        </SimpleGrid>
      )}

      {stranded.length > 0 && (
        <Alert color="yellow" icon={<IconAlertTriangle />}>
          {stranded.map((w) => t("{name} is offline{models}{waiting}.", {
            name: w.name,
            models: w.models ? (w.models === 1 ? t(", and 1 model only runs there") : t(", and {n} models only run there", { n: w.models })) : "",
            waiting: w.waiting ? (w.models ? t(" and {runs} for it", { runs: w.waiting === 1 ? t("1 run waits") : t("{n} runs wait", { n: w.waiting }) }) : t(", {runs} for it", { runs: w.waiting === 1 ? t("1 run waits") : t("{n} runs wait", { n: w.waiting }) })) : "",
          })).join(" ")}{" "}{t("Start it again, or move them to another worker.")}</Alert>
      )}

      {ws.length === 0 ? (
        <Card withBorder radius="lg" padding="xl" className="text-center">
          <Text fw={600}>{t("No workers yet")}</Text>
          <Text c="dimmed" size="sm">{t("Without workers this server runs models itself. Add a worker to use a bigger machine, or several.")}</Text>
        </Card>
      ) : (
        <SimpleGrid cols={{ base: 1, sm: 2, lg: 3 }}>
          {ws.map((w) => <WorkerCard key={w.id} w={w} rented={w.id === rented} gpuProvider={gpuProvider} />)}
        </SimpleGrid>
      )}

      <TrainHere />
      <TrainTogether />
      <CloudGpu />

      <Modal opened={adding} onClose={add.close} title={t("Add a worker")} centered>
        <Stack>
          <TextInput label={t("Name")} description={t("Where it runs, for example gpu-box or Mac mini.")} value={name} onChange={(e) => setName(e.currentTarget.value)} maxLength={100} data-autofocus />
          {create.error && <Alert color="red" icon={<IconAlertCircle />}>{message(create.error)}</Alert>}
          <Group justify="flex-end">
            <Button variant="default" onClick={add.close}>{t("Cancel")}</Button>
            <Button loading={create.isPending} disabled={!name.trim()} onClick={() => create.mutate({ name })}>{t("Make a token")}</Button>
          </Group>
        </Stack>
      </Modal>
      <TokenModal token={token} onClose={() => setToken(undefined)} />
    </Stack>
  );
}

function Tile({ label, value, hint, bar }: { label: string; value: string; hint?: string; bar?: number }) {
  return (
    <Card withBorder radius="lg" padding="md">
      <Text size="xs" c="dimmed" tt="uppercase" fw={600} lts={0.4}>{label}</Text>
      <Text fz={26} fw={700} ff="var(--font-display)" lh={1.2} mt={4}>{value}</Text>
      {bar !== undefined ? <Progress mt={8} value={bar} color={loadColor(bar)} size="sm" radius="xl" aria-label={label} /> : <Text size="xs" c="dimmed" mt={6}>{hint}</Text>}
    </Card>
  );
}

// Meter is one labelled bar on a worker card.
export function Meter({ label, pct, detail }: { label: string; pct: number; detail: string }) {
  return (
    <div>
      <Group justify="space-between" gap={4} mb={2}>
        <Text size="xs" fw={600}>{label}</Text>
        <Text size="xs" c="dimmed">{detail}</Text>
      </Group>
      <Progress value={pct} color={loadColor(pct)} size="sm" radius="xl" aria-label={label} />
    </div>
  );
}

function WorkerCard({ w, rented, gpuProvider = "" }: { w: Worker; rented: boolean; gpuProvider?: string }) {
  const s = w.stats;
  const full = w.online && w.busy >= w.capacity;
  // The worker's own memory guard: 1 when it takes no training, 2 when it
  // also loads no more models.
  const short = w.online && (s?.memoryPressure ?? 0) > 0;
  const hot = short || (s && Math.max(s.cpuPercent, share(s.memoryUsedBytes, s.memoryTotalBytes), share(s.diskUsedBytes, s.diskTotalBytes)) >= 90);
  return (
    <Link to="/workers/$id" params={{ id: w.id }} className="block text-inherit no-underline" aria-label={`${w.name}, ${w.online ? t("online") : t("offline")}`}>
    <Card
      withBorder radius="lg" padding="md" h="100%"
      className={`transition-shadow hover:shadow-md ${w.online ? "" : "opacity-70"}`}
      style={hot ? { borderColor: "var(--mantine-color-yellow-5)" } : undefined}
    >
      <Group justify="space-between" wrap="nowrap" mb="sm">
        <Group gap="xs" wrap="nowrap" className="min-w-0">
          <StatusDot online={w.online} busy={full} />
          <Text fw={700} truncate>{w.name}</Text>
        </Group>
        <Group gap={4} wrap="nowrap">
          <Badge variant="light" color={!w.online ? "gray" : full ? "yellow" : "teal"}>{!w.online ? (rented ? t("Asleep") : t("Offline")) : full ? t("Busy") : t("Online")}</Badge>
          <IconChevronRight size={16} color="var(--mantine-color-dimmed)" />
        </Group>
      </Group>
      {w.online && s ? (
        <Stack gap={8}>
          <Meter label="CPU" pct={s.cpuPercent} detail={t("{cpuPercent}% of {cpus} CPUs", { cpuPercent: Math.round(s.cpuPercent), cpus: w.cpus })} />
          <Meter label={t("Memory")} pct={share(s.memoryUsedBytes, s.memoryTotalBytes)} detail={t("{n} of {total}", { n: bytes(s.memoryUsedBytes), total: bytes(s.memoryTotalBytes) })} />
          <Meter label={t("Storage")} pct={share(s.diskUsedBytes, s.diskTotalBytes)} detail={s.diskTotalBytes > 0n ? t("{n1} free", { n1: bytes(s.diskTotalBytes - s.diskUsedBytes) }) : t("unknown")} />
        </Stack>
      ) : (
        <Text size="sm" c="dimmed">{rented ? t("A GPU on {gpuProvider}, started when a training run waits for it.", { gpuProvider }) : w.lastSeen ? t("Last seen {lastSeen}", { lastSeen: ago(w.lastSeen) }) : t("Never connected. Run the command from when you added it.")}</Text>
      )}
      <Group gap="md" mt="sm">
        {w.online && <Text size="xs" c="dimmed">{w.busy}{" "}{t("of")}{" "}{w.capacity}{" "}{t("tasks")}</Text>}
        {w.online && w.gpus > 1 && <Text size="xs" c="dimmed">{t("teaches on")}{" "}{w.gpus}{" "}{t("GPUs at once")}</Text>}
        {w.online && w.trainingOnly && <Text size="xs" c="dimmed">{t("teaches only")}</Text>}
        {short && <Text size="xs" c="yellow.8">{s?.memoryPressure === 2 ? t("memory critical: answering only") : t("memory low: not teaching")}</Text>}
        {w.online && !w.trains && !w.trainingOnly && <Text size="xs" c="dimmed">{t("answers only")}</Text>}
        {w.models > 0 && <Text size="xs" c="dimmed">{w.models === 1 ? t("1 model here") : t("{n} models here", { n: w.models })}</Text>}
        {w.waiting > 0 && <Text size="xs" c="dimmed">{w.waiting}{" "}{t("waiting")}</Text>}
        {w.online && s && <Text size="xs" c="dimmed">{t("up")}{" "}{duration(s.uptimeSeconds)}</Text>}
      </Group>
    </Card>
    </Link>
  );
}

// TrainHere is the switch for training on the console itself. Off, every
// run waits for a worker, so training never shares the machine with the
// API.
function TrainHere() {
  const { data } = useSuspenseQuery(settings);
  const queryClient = useQueryClient();
  const training = data.training;
  const save = useMutation(SettingsService.method.updateTraining, {
    onSuccess: async (res) => {
      await queryClient.invalidateQueries({ queryKey: allSettingsQueries });
      notifications.show({ color: "teal", message: res.training?.trainHere ? t("This console trains runs itself when no worker takes them.") : t("Training runs now wait for a worker; this console trains none itself.") });
    },
  });
  return (
    <Card withBorder radius="lg" padding="lg" className="max-w-xl">
      <Title order={3} fz="md" mb={4}>{t("Training on this console")}</Title>
      {training?.canTrainHere ? (
        <Stack gap="sm">
          <Text size="sm" c="dimmed">{t("On, the console trains runs in its own process when no worker takes them, sharing its CPUs with the API. Off, every run waits for a worker that teaches, so training never slows the API served here.")}</Text>
          <Switch label={t("Train runs on this console when no worker takes them")} checked={training.trainHere} disabled={save.isPending} onChange={(e) => save.mutate({ trainHere: e.currentTarget.checked })} />
          {save.error && <Alert color="red" icon={<IconAlertCircle />}>{message(save.error)}</Alert>}
        </Stack>
      ) : (
        <Text size="sm" c="dimmed">{t("This console runs no models itself (it was started as")}{" "}<Code>-role console</Code>{t("): every training run goes to a worker that teaches, and every prediction to one that answers.")}</Text>
      )}
    </Card>
  );
}

// TrainTogether chooses how many workers one run may train on at once.
// Each learns from its share of the examples and they average what they
// learned every few steps, so a run finishes sooner on several machines.
function TrainTogether() {
  const { data } = useSuspenseQuery(settings);
  const queryClient = useQueryClient();
  const training = data.training;
  const n = Math.max(1, training?.groupWorkers ?? 0);
  const save = useMutation(SettingsService.method.updateTraining, {
    onSuccess: async (res) => {
      await queryClient.invalidateQueries({ queryKey: allSettingsQueries });
      const k = res.training?.groupWorkers ?? 0;
      notifications.show({ color: "teal", message: k > 1 ? t("A run now trains on up to {k} workers together when they're free.", { k }) : t("Each run now trains on one worker.") });
    },
  });
  const choices = [1, 2, 3, 4, 6, 8, 16].map((k) => ({ value: String(k), label: k === 1 ? t("One worker") : t("Up to {k} workers", { k }) }));
  return (
    <Card withBorder radius="lg" padding="lg" className="max-w-xl">
      <Title order={3} fz="md" mb={4}>{t("One run on several workers")}</Title>
      <Stack gap="sm">
        <Text size="sm" c="dimmed">{t("Split a run between workers that teach and are free: each learns from its share of the examples, and every few steps they combine what they learned, so the run finishes sooner. It uses as many as are free, at least two, and otherwise trains on one. Runs asked to train on a particular worker stay there.")}</Text>
        <Select label={t("Train each run on")} data={choices} value={String(n)} allowDeselect={false} disabled={save.isPending} className="max-w-xs"
          onChange={(v) => v && save.mutate({ trainHere: training?.trainHere ?? false, groupWorkers: Number(v) })} />
        {save.error && <Alert color="red" icon={<IconAlertCircle />}>{message(save.error)}</Alert>}
      </Stack>
    </Card>
  );
}

// TokenModal shows a new worker token once, with the command to run.
export function TokenModal({ token, onClose }: { token?: string; onClose: () => void }) {
  const command = `mimir-server -role=worker -server ${location.origin} -token ${token ?? "…"}`;
  return (
    <Modal opened={!!token} onClose={onClose} title={t("Run this on the worker machine")} centered closeOnClickOutside={false} size="lg">
      <Text size="sm" mb="sm">{t("The token is shown once. Install the same mimir-server binary there and run:")}</Text>
      <Group wrap="nowrap" align="flex-start">
        <Code block className="flex-1 break-all">{command}</Code>
        <CopyButton value={command}>
          {({ copied, copy }) => <Button variant="default" onClick={copy} leftSection={copied ? <IconCheck size={16} /> : <IconCopy size={16} />}>{copied ? t("Copied") : t("Copy")}</Button>}
        </CopyButton>
      </Group>
      <Text size="xs" c="dimmed" mt="sm">{t("The worker makes its own key the first time it connects, and from then on only that machine can use this token. If you rebuild the machine, make a new token on the worker's page.")}{token?.includes(".") && t(" The token also carries this console's fingerprint, so the worker checks it reached the right console.")}
        {" "}{t("You can put the token in the MIMIR_WORKER_TOKEN environment variable instead of -token.")}{" "}{t("Add -tasks train to make the worker only teach, or -tasks serve to make it only answer, so training never slows the machines that answer.")}</Text>
      <Group justify="flex-end" mt="lg"><Button onClick={onClose}>{t("I've saved it")}</Button></Group>
    </Modal>
  );
}
