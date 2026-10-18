import { useMutation } from "@connectrpc/connect-query";
import { Alert, Card, Group, Select, Text } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { IconAlertCircle, IconAlertTriangle } from "@tabler/icons-react";
import { useQuery, useQueryClient } from "@tanstack/react-query";

import { message } from "../api/client";
import { ModelService, type Model } from "../api/gen/mimir/v1/model_pb";
import type { Worker } from "../api/gen/mimir/v1/worker_pb";
import { allModelQueries, allWorkerQueries, listWorkerChoices } from "../api/queries";

// A small light for whether a worker is up.
export function StatusDot({ online, busy, size = 10 }: { online: boolean; busy?: boolean; size?: number }) {
  const color = !online ? "var(--mantine-color-gray-5)" : busy ? "var(--mantine-color-yellow-6)" : "var(--mantine-color-teal-6)";
  return (
    <span className="relative inline-flex shrink-0" style={{ width: size, height: size }}>
      {online && <span className="absolute inline-flex h-full w-full animate-ping rounded-full opacity-40" style={{ background: color }} />}
      <span className="relative inline-flex rounded-full" style={{ width: size, height: size, background: color }} />
    </span>
  );
}

// useWorkerChoices lists the workers work can be placed on. It's empty
// for people who can't place work, and while it loads.
export function useWorkerChoices(enabled = true) {
  const { data } = useQuery({ ...listWorkerChoices, enabled, refetchInterval: 10000, retry: false });
  return data?.workers ?? [];
}

// WorkerSelect picks a worker, or "" for any.
export function WorkerSelect({ workers, value, onChange, label, description, forTraining, disabled }: {
  workers: Worker[]; value: string; onChange: (id: string) => void; label: string; description: string; forTraining?: boolean; disabled?: boolean;
}) {
  const data = [
    { value: "", label: "Automatic: any worker with room" },
    ...workers.map((w) => ({
      value: w.id,
      label: w.name,
      disabled: forTraining && w.online && !w.trains,
    })),
  ];
  const byId = new Map(workers.map((w) => [w.id, w]));
  return (
    <Select
      label={label}
      description={description}
      data={data}
      value={value}
      onChange={(v) => onChange(v ?? "")}
      allowDeselect={false}
      disabled={disabled}
      renderOption={({ option }) => {
        const w = byId.get(option.value);
        if (!w) return <Text size="sm">{option.label}</Text>;
        return (
          <Group gap="xs" wrap="nowrap">
            <StatusDot online={w.online} busy={w.online && w.busy >= w.capacity} size={8} />
            <Text size="sm">{w.name}</Text>
            <Text size="xs" c="dimmed">
              {!w.online ? "offline" : forTraining && !w.trains ? "can't teach" : w.busy >= w.capacity ? "busy" : `${w.busy} of ${w.capacity} in use`}
            </Text>
          </Group>
        );
      }}
    />
  );
}

// RunsOn is the model page's card for where a model answers.
export function RunsOn({ model, canWrite }: { model: Model; canWrite: boolean }) {
  const queryClient = useQueryClient();
  const workers = useWorkerChoices(canWrite);
  const set = useMutation(ModelService.method.setModelWorker, {
    onSuccess: async (res) => {
      await queryClient.invalidateQueries({ queryKey: allModelQueries });
      await queryClient.invalidateQueries({ queryKey: allWorkerQueries });
      const w = workers.find((x) => x.id === res.model?.workerId);
      notifications.show({ color: "teal", message: w ? `${model.name} now runs on ${w.name}.` : `${model.name} now runs wherever there's room.` });
    },
  });
  if (!canWrite || workers.length === 0) return null;
  const chosen = workers.find((w) => w.id === model.workerId);
  return (
    <Card withBorder radius="lg" padding="lg">
      <Text fw={700} mb={4}>Where it runs</Text>
      <Text size="sm" c="dimmed" mb="sm">Keep this model on one machine, for example the one with a GPU, or let any worker answer.</Text>
      <WorkerSelect
        workers={workers}
        value={model.workerId}
        onChange={(workerId) => set.mutate({ id: model.id, workerId })}
        label="Runs on"
        description={chosen ? "Only this worker answers. If it's offline, apps get a clear error until it's back." : "Questions go to the least busy worker that can run it."}
        disabled={set.isPending}
      />
      {chosen && !chosen.online && (
        <Alert mt="sm" color="yellow" icon={<IconAlertTriangle />}>{chosen.name} is offline, so this model can't answer right now.</Alert>
      )}
      {set.error && <Alert mt="sm" color="red" icon={<IconAlertCircle />}>{message(set.error)}</Alert>}
    </Card>
  );
}
