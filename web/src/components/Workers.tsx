import { useMutation } from "@connectrpc/connect-query";
import { Alert, Badge, Button, Card, Code, CopyButton, Group, Modal, Progress, SimpleGrid, Stack, Text, TextInput, Title } from "@mantine/core";
import { useDisclosure } from "@mantine/hooks";
import { IconAlertCircle, IconAlertTriangle, IconCheck, IconChevronRight, IconCopy, IconLock, IconLockOpen } from "@tabler/icons-react";
import { useQueryClient, useSuspenseQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";

import { message } from "../api/client";
import { WorkerService, type Worker } from "../api/gen/mimir/v1/worker_pb";
import { allWorkerQueries, listWorkers } from "../api/queries";
import { ago, bytes, duration, loadColor, share } from "../lib/format";
import { StatusDot } from "./Placement";

// plainConsole says workers on other machines can't join this page's
// address, because it isn't https.
const plainConsole = () => location.protocol !== "https:" && !["localhost", "127.0.0.1", "[::1]"].includes(location.hostname);

// Workers is the overview of the machines that run models for this
// console. It refreshes while open so the numbers stay live.
export function Workers() {
  const { data } = useSuspenseQuery({ ...listWorkers, refetchInterval: 5000 });
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
  const stranded = ws.filter((w) => !w.online && (w.models > 0 || w.waiting > 0));

  return (
    <Stack>
      <Group justify="space-between" align="flex-start">
        <div>
          <Title order={3} fz="lg">Workers</Title>
          <Text c="dimmed" size="sm" maw={560}>The console is this web app. Workers are the machines that run and teach the models. Add one to get its token and the command to run there.</Text>
        </div>
        <Button onClick={add.open}>Add a worker</Button>
      </Group>

      {plainConsole() ? (
        <Alert color="yellow" icon={<IconLockOpen />} title="Workers on other machines can't join yet">
          This console isn't served over https, so a worker elsewhere would send its token unencrypted. Start the console with <Code>-tls-auto</Code>, give it a certificate with <Code>-tls-cert</Code> and <Code>-tls-key</Code>, or put it behind an https proxy.
        </Alert>
      ) : (
        <Group gap={6} c="dimmed">
          <IconLock size={16} />
          <Text size="sm">Links are encrypted. Each worker proves it's the machine that first joined, and {data.secureJoin ? "checks this console's fingerprint from its token" : "checks this console's certificate"}.</Text>
        </Group>
      )}

      {ws.length > 0 && (
        <SimpleGrid cols={{ base: 2, sm: 4 }}>
          <Tile label="Online" value={`${online.length} of ${ws.length}`} hint={online.length === ws.length ? "All connected" : `${ws.length - online.length} offline`} />
          <Tile label="Tasks running" value={`${busy} of ${capacity}`} bar={share(busy, capacity)} />
          <Tile label="Average CPU" value={online.length ? `${Math.round(cpu)}%` : "–"} bar={cpu} />
          <Tile label="Average memory" value={online.length ? `${Math.round(mem)}%` : "–"} bar={mem} />
        </SimpleGrid>
      )}

      {stranded.length > 0 && (
        <Alert color="yellow" icon={<IconAlertTriangle />}>
          {stranded.map((w) => `${w.name} is offline${w.models ? `, and ${w.models} model${w.models === 1 ? " only runs" : "s only run"} there` : ""}${w.waiting ? `${w.models ? " and" : ","} ${w.waiting} run${w.waiting === 1 ? " waits" : "s wait"} for it` : ""}.`).join(" ")} Start it again, or move them to another worker.
        </Alert>
      )}

      {ws.length === 0 ? (
        <Card withBorder radius="lg" padding="xl" className="text-center">
          <Text fw={600}>No workers yet</Text>
          <Text c="dimmed" size="sm">Without workers this server runs models itself. Add a worker to use a bigger machine, or several.</Text>
        </Card>
      ) : (
        <SimpleGrid cols={{ base: 1, sm: 2, lg: 3 }}>
          {ws.map((w) => <WorkerCard key={w.id} w={w} />)}
        </SimpleGrid>
      )}

      <Modal opened={adding} onClose={add.close} title="Add a worker" centered>
        <Stack>
          <TextInput label="Name" description="Where it runs, for example gpu-box or Mac mini." value={name} onChange={(e) => setName(e.currentTarget.value)} maxLength={100} data-autofocus />
          {create.error && <Alert color="red" icon={<IconAlertCircle />}>{message(create.error)}</Alert>}
          <Group justify="flex-end">
            <Button variant="default" onClick={add.close}>Cancel</Button>
            <Button loading={create.isPending} disabled={!name.trim()} onClick={() => create.mutate({ name })}>Make a token</Button>
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

function WorkerCard({ w }: { w: Worker }) {
  const s = w.stats;
  const full = w.online && w.busy >= w.capacity;
  const hot = s && Math.max(s.cpuPercent, share(s.memoryUsedBytes, s.memoryTotalBytes), share(s.diskUsedBytes, s.diskTotalBytes)) >= 90;
  return (
    <Link to="/workers/$id" params={{ id: w.id }} className="block text-inherit no-underline" aria-label={`${w.name}, ${w.online ? "online" : "offline"}`}>
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
          <Badge variant="light" color={!w.online ? "gray" : full ? "yellow" : "teal"}>{!w.online ? "Offline" : full ? "Busy" : "Online"}</Badge>
          <IconChevronRight size={16} color="var(--mantine-color-dimmed)" />
        </Group>
      </Group>
      {w.online && s ? (
        <Stack gap={8}>
          <Meter label="CPU" pct={s.cpuPercent} detail={`${Math.round(s.cpuPercent)}% of ${w.cpus} CPUs`} />
          <Meter label="Memory" pct={share(s.memoryUsedBytes, s.memoryTotalBytes)} detail={`${bytes(s.memoryUsedBytes)} of ${bytes(s.memoryTotalBytes)}`} />
          <Meter label="Storage" pct={share(s.diskUsedBytes, s.diskTotalBytes)} detail={s.diskTotalBytes > 0n ? `${bytes(s.diskTotalBytes - s.diskUsedBytes)} free` : "unknown"} />
        </Stack>
      ) : (
        <Text size="sm" c="dimmed">{w.lastSeen ? `Last seen ${ago(w.lastSeen)}` : "Never connected. Run the command from when you added it."}</Text>
      )}
      <Group gap="md" mt="sm">
        {w.online && <Text size="xs" c="dimmed">{w.busy} of {w.capacity} tasks</Text>}
        {w.models > 0 && <Text size="xs" c="dimmed">{w.models} model{w.models === 1 ? "" : "s"} here</Text>}
        {w.waiting > 0 && <Text size="xs" c="dimmed">{w.waiting} waiting</Text>}
        {w.online && s && <Text size="xs" c="dimmed">up {duration(s.uptimeSeconds)}</Text>}
      </Group>
    </Card>
    </Link>
  );
}

// TokenModal shows a new worker token once, with the command to run.
export function TokenModal({ token, onClose }: { token?: string; onClose: () => void }) {
  const command = `mimir-server -role=worker -server ${location.origin} -token ${token ?? "…"}`;
  return (
    <Modal opened={!!token} onClose={onClose} title="Run this on the worker machine" centered closeOnClickOutside={false} size="lg">
      <Text size="sm" mb="sm">The token is shown once. Install the same mimir-server binary there and run:</Text>
      <Group wrap="nowrap" align="flex-start">
        <Code block className="flex-1 break-all">{command}</Code>
        <CopyButton value={command}>
          {({ copied, copy }) => <Button variant="default" onClick={copy} leftSection={copied ? <IconCheck size={16} /> : <IconCopy size={16} />}>{copied ? "Copied" : "Copy"}</Button>}
        </CopyButton>
      </Group>
      <Text size="xs" c="dimmed" mt="sm">
        The worker makes its own key the first time it connects, and from then on only that machine can use this token. If you rebuild the machine, make a new token on the worker's page.
        {token?.includes(".") && " The token also carries this console's fingerprint, so the worker checks it reached the right console."}
        {" "}You can put the token in the MIMIR_WORKER_TOKEN environment variable instead of -token.
      </Text>
      <Group justify="flex-end" mt="lg"><Button onClick={onClose}>I've saved it</Button></Group>
    </Modal>
  );
}
