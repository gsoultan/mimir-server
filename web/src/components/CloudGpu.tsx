import { useMutation } from "@connectrpc/connect-query";
import { Alert, Anchor, Badge, Button, Card, Code, Collapse, Group, NumberInput, PasswordInput, Select, SimpleGrid, Stack, Switch, Text, TextInput, Title } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { IconAlertCircle } from "@tabler/icons-react";
import { useQueryClient, useSuspenseQuery } from "@tanstack/react-query";
import { useEffect, useState } from "react";

import { message } from "../api/client";
import { WorkerService, type CloudGpu as Gpu } from "../api/gen/mimir/v1/worker_pb";
import { allWorkerQueries, cloudGpu } from "../api/queries";
import { ago } from "../lib/format";

// GPUS are Modal's GPUs with what an hour of each costs there, from
// modal.com/pricing. The machine's CPU and memory add about $0.24 an hour.
const GPUS = [
  { value: "T4", label: "T4, 16 GB · about $0.59 an hour" },
  { value: "L4", label: "L4, 24 GB · about $0.80 an hour" },
  { value: "A10", label: "A10, 24 GB · about $1.10 an hour" },
  { value: "L40S", label: "L40S, 48 GB · about $1.95 an hour" },
  { value: "A100-40GB", label: "A100, 40 GB · about $2.10 an hour" },
  { value: "A100-80GB", label: "A100, 80 GB · about $2.50 an hour" },
  { value: "H100", label: "H100, 80 GB · about $3.95 an hour" },
  { value: "H200", label: "H200, 141 GB · about $4.54 an hour" },
  { value: "B200", label: "B200, 180 GB · about $6.25 an hour" },
];

const STATES: Record<string, { label: string; color: string }> = {
  off: { label: "Off", color: "gray" },
  asleep: { label: "Asleep", color: "gray" },
  starting: { label: "Starting", color: "blue" },
  running: { label: "Running", color: "teal" },
  failed: { label: "Didn't start", color: "red" },
};

type Form = { enabled: boolean; tokenId: string; tokenSecret: string; gpu: string; consoleUrl: string; idleMinutes: number; maxHours: number; capacity: number; programUrl: string };

const formOf = (g: Gpu | undefined): Form => ({
  enabled: g?.enabled ?? false,
  tokenId: g?.tokenId ?? "",
  tokenSecret: "",
  gpu: g?.gpu || "L4",
  consoleUrl: g?.consoleUrl || location.origin,
  idleMinutes: g?.idleMinutes || 5,
  maxHours: g?.maxHours || 6,
  capacity: g?.capacity || 1,
  programUrl: g?.programUrl ?? "",
});

// Hint explains the row of fields above it, so fields side by side stay
// the same height.
function Hint({ children }: { children: React.ReactNode }) {
  return <Text size="xs" c="dimmed" mt={-4}>{children}</Text>;
}

// CloudGpu sets up renting a GPU on Modal for training: the console starts
// one when a run waits that nothing else takes, and it stops itself once
// it has had nothing to do for a few minutes.
export function CloudGpu() {
  const { data } = useSuspenseQuery({ ...cloudGpu, refetchInterval: 5000 });
  const g = data.cloudGpu;
  const queryClient = useQueryClient();
  const [form, setForm] = useState<Form>(() => formOf(g));
  const [advanced, setAdvanced] = useState(!!g?.programUrl || (g ? !g.canServeProgram : false));
  const saved = g?.workerId ?? "";
  // Start over from what is stored when it changes elsewhere.
  useEffect(() => setForm(formOf(g)), [saved]);
  const set = (p: Partial<Form>) => setForm((f) => ({ ...f, ...p }));
  const save = useMutation(WorkerService.method.updateCloudGpu, {
    onSuccess: async (res) => {
      await queryClient.invalidateQueries({ queryKey: allWorkerQueries });
      setForm(formOf(res.cloudGpu));
      notifications.show({ color: "teal", message: res.cloudGpu?.enabled ? `Training runs that wait will start a ${res.cloudGpu.gpu} on Modal.` : "Renting a GPU is off." });
    },
  });
  const stop = useMutation(WorkerService.method.stopCloudGpu, {
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: allWorkerQueries });
      notifications.show({ color: "teal", message: "The GPU on Modal is stopped. Runs on it went back in the queue." });
    },
  });
  const st = STATES[g?.state ?? "off"] ?? { label: "Off", color: "gray" };
  const live = g?.state === "starting" || g?.state === "running";

  return (
    <Card withBorder radius="lg" padding="lg" className="max-w-2xl">
      <Group justify="space-between" align="flex-start" mb={4}>
        <Title order={3} fz="md">Rent a GPU for training</Title>
        <Badge variant="light" color={st.color}>{st.label}</Badge>
      </Group>
      <Stack gap="sm">
        <Text size="sm" c="dimmed">
          When a training run waits that nothing here takes, the console starts a machine with a GPU on <Anchor href="https://modal.com" target="_blank" rel="noreferrer">Modal</Anchor>, trains there, and stops it once it has had nothing to do for a few minutes. Modal bills by the second while it runs. The models it trains are answered by your other workers, which get the new weights with the next question.
        </Text>
        {g?.state === "asleep" && <Text size="sm">Nothing is waiting for it. It starts when a run waits.</Text>}
        {g?.state === "starting" && <Text size="sm">Starting on Modal{g.stateTime ? `, ${ago(g.stateTime)}` : ""}. The first start downloads the CUDA image and takes a few minutes; later ones take under a minute.</Text>}
        {g?.state === "running" && <Text size="sm">Running and training. It shows as <b>Modal GPU</b> under Workers above.</Text>}
        {g?.state === "failed" && <Alert color="red" icon={<IconAlertCircle />}>{g.error}</Alert>}
        {live && (
          <Group>
            <Button variant="default" color="red" size="xs" loading={stop.isPending} onClick={() => stop.mutate({})}>Stop it now</Button>
            {stop.error && <Text size="sm" c="red">{message(stop.error)}</Text>}
          </Group>
        )}

        <Switch label="Start a GPU on Modal when a training run waits" checked={form.enabled} onChange={(e) => set({ enabled: e.currentTarget.checked })} />
        <Collapse expanded={form.enabled}>
          <Stack gap="sm">
            <SimpleGrid cols={{ base: 1, sm: 2 }} spacing="md" verticalSpacing="sm">
              <TextInput label="Modal token id" placeholder="ak-…" value={form.tokenId} onChange={(e) => set({ tokenId: e.currentTarget.value })} />
              <PasswordInput label="Token secret" placeholder={g?.hasTokenSecret ? "Saved; leave empty to keep" : "as-…"} value={form.tokenSecret} onChange={(e) => set({ tokenSecret: e.currentTarget.value })} />
            </SimpleGrid>
            <Hint>From Settings → API tokens on modal.com. The secret is stored encrypted and never shown again. Leave both empty to use <Code>MODAL_TOKEN_ID</Code> and <Code>MODAL_TOKEN_SECRET</Code> from the console's environment.</Hint>
            <SimpleGrid cols={{ base: 1, sm: 2 }} spacing="md" verticalSpacing="sm">
              <Select label="GPU" data={GPUS} value={form.gpu} onChange={(v) => set({ gpu: v ?? "L4" })} allowDeselect={false} />
              <TextInput label="This console's address" placeholder="https://mimir.example.com" value={form.consoleUrl} onChange={(e) => set({ consoleUrl: e.currentTarget.value })} />
            </SimpleGrid>
            <Hint>Prices are Modal's, per hour the machine runs; its CPU and memory add about $0.24. A T4 or L4 suits table, text and small photo models. The address is where the GPU machine reaches this console over https, from the internet.</Hint>
            <SimpleGrid cols={{ base: 1, sm: 3 }} spacing="md" verticalSpacing="sm">
              <NumberInput label="Stop after idle" suffix=" min" min={1} max={120} value={form.idleMinutes} onChange={(v) => set({ idleMinutes: Number(v) || 5 })} />
              <NumberInput label="Longest per machine" suffix=" h" min={1} max={24} value={form.maxHours} onChange={(v) => set({ maxHours: Number(v) || 6 })} />
              <NumberInput label="Runs at once" min={1} max={16} value={form.capacity} onChange={(v) => set({ capacity: Number(v) || 1 })} />
            </SimpleGrid>
            <Hint>It waits this long for more runs before stopping. A run still going when a machine reaches its limit resumes on a new one. Runs at once share the one GPU.</Hint>
            <Anchor component="button" type="button" size="sm" style={{ alignSelf: "flex-start" }} onClick={() => setAdvanced((a) => !a)}>{advanced ? "Hide" : "Where the GPU gets mimir-server"}</Anchor>
            <Collapse expanded={advanced}>
              <TextInput label="Download address" placeholder="https://…/mimir-server" value={form.programUrl} onChange={(e) => set({ programUrl: e.currentTarget.value })}
                description={g?.canServeProgram
                  ? "Empty: the machine downloads this console's own program from it, so both run the same version. Or give an https address of mimir-server for linux/amd64."
                  : "This console doesn't run on linux/amd64, so it can't hand the machine its program. Give an https address of mimir-server for linux/amd64, the same version as the console."} />
            </Collapse>
          </Stack>
        </Collapse>
        {save.error && <Alert color="red" icon={<IconAlertCircle />}>{message(save.error)}</Alert>}
        <Group justify="flex-end">
          <Button loading={save.isPending} disabled={!form.enabled && !g?.enabled} onClick={() => save.mutate(form)}>Save</Button>
        </Group>
        <Text size="xs" c="dimmed">
          A run goes to it when nothing else takes it. To send a run there anyway, pick <b>Modal GPU</b> under <b>Train on</b> when you start it. Turn off <b>Training on this console</b> above, or runs placed on <i>Automatic</i> train here instead.
        </Text>
      </Stack>
    </Card>
  );
}
