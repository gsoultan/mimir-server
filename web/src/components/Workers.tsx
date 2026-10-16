import { useMutation } from "@connectrpc/connect-query";
import { Alert, Badge, Button, Card, Code, CopyButton, Group, Modal, Stack, Text, TextInput, Title } from "@mantine/core";
import { useDisclosure } from "@mantine/hooks";
import { notifications } from "@mantine/notifications";
import { IconAlertCircle, IconCheck, IconCopy } from "@tabler/icons-react";
import { useQueryClient, useSuspenseQuery } from "@tanstack/react-query";
import { useState } from "react";

import { message } from "../api/client";
import { WorkerService, type Worker } from "../api/gen/mimir/v1/worker_pb";
import { allWorkerQueries, listArchitectures, listWorkers } from "../api/queries";
import { ago, bytes } from "../lib/format";

// Workers lists the machines that run models for this server and makes
// tokens for new ones. The list refreshes while open so online shows live.
export function Workers() {
  const { data } = useSuspenseQuery({ ...listWorkers, refetchInterval: 5000 });
  const { data: archs } = useSuspenseQuery(listArchitectures);
  const queryClient = useQueryClient();
  const refresh = () => queryClient.invalidateQueries({ queryKey: allWorkerQueries });
  const [adding, add] = useDisclosure(false);
  const [name, setName] = useState("");
  const [token, setToken] = useState<string>();
  const create = useMutation(WorkerService.method.createWorker, {
    onSuccess: async (res) => { await refresh(); add.close(); setName(""); setToken(res.token); },
  });
  const remove = useMutation(WorkerService.method.deleteWorker, {
    onSuccess: async () => { await refresh(); notifications.show({ message: "Worker removed. Its token no longer works." }); },
  });
  const title = (a: string) => archs.architectures.find((x) => x.name === a)?.title ?? a;
  const command = `mimir-server -role=worker -server ${location.origin} -token ${token ?? "…"}`;

  return (
    <Stack>
      <Group justify="space-between" align="flex-start">
        <div>
          <Title order={3} fz="lg">Workers</Title>
          <Text c="dimmed" size="sm">Other machines that run and teach models for this server. Add one to get a token and the command to run there.</Text>
        </div>
        <Button onClick={add.open}>Add a worker</Button>
      </Group>
      {data.workers.length === 0 ? (
        <Card withBorder radius="lg" padding="xl" className="text-center">
          <Text fw={600}>No workers yet</Text>
          <Text c="dimmed" size="sm">Without workers this server runs models itself. Add a worker to use a bigger machine, or several.</Text>
        </Card>
      ) : data.workers.map((w) => <WorkerCard key={w.id} w={w} title={title} onDelete={() => remove.mutate({ id: w.id })} deleting={remove.isPending} />)}
      {remove.error && <Alert color="red" icon={<IconAlertCircle />}>{message(remove.error)}</Alert>}

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

      <Modal opened={!!token} onClose={() => setToken(undefined)} title="Run this on the worker machine" centered closeOnClickOutside={false} size="lg">
        <Text size="sm" mb="sm">The token is shown once. Install the same mimir-server binary there and run:</Text>
        <Group wrap="nowrap" align="flex-start">
          <Code block className="flex-1 break-all">{command}</Code>
          <CopyButton value={command}>
            {({ copied, copy }) => <Button variant="default" onClick={copy} leftSection={copied ? <IconCheck size={16} /> : <IconCopy size={16} />}>{copied ? "Copied" : "Copy"}</Button>}
          </CopyButton>
        </Group>
        <Text size="xs" c="dimmed" mt="sm">You can also put the token in the MIMIR_WORKER_TOKEN environment variable instead of -token. The worker appears here as online within a few seconds.</Text>
        <Group justify="flex-end" mt="lg"><Button onClick={() => setToken(undefined)}>I've saved it</Button></Group>
      </Modal>
    </Stack>
  );
}

function WorkerCard({ w, title, onDelete, deleting }: { w: Worker; title: (a: string) => string; onDelete: () => void; deleting: boolean }) {
  return (
    <Card withBorder radius="lg" padding="md">
      <Group justify="space-between" wrap="nowrap" align="flex-start">
        <div className="min-w-0">
          <Group gap="xs">
            <Text fw={700}>{w.name}</Text>
            <Badge color={w.online ? "teal" : "gray"} variant="light">{w.online ? "Online" : "Offline"}</Badge>
            {w.online && w.busy > 0 && <Badge color="blue" variant="light">{w.busy} running</Badge>}
          </Group>
          {w.online ? (
            <Text size="sm" c="dimmed">
              {w.device}{w.cpus > 0 && ` · ${w.cpus} CPUs`}{w.memoryBytes > 0n && ` · ${bytes(w.memoryBytes)}`} · up to {w.capacity} at once{w.version && ` · ${w.version}`}{w.address && ` · from ${w.address}`}
            </Text>
          ) : (
            <Text size="sm" c="dimmed">{w.lastSeen ? `Last seen ${ago(w.lastSeen)}` : "Never connected"} · added by {w.createdBy} {ago(w.createTime)}</Text>
          )}
          {w.online && (
            <Text size="sm" mt={4}>
              {w.trains ? "Runs and teaches" : "Runs"}: {w.architectures.map(title).join(", ")}
            </Text>
          )}
        </div>
        <Button size="xs" variant="subtle" color="red" onClick={onDelete} loading={deleting}>Remove</Button>
      </Group>
    </Card>
  );
}
