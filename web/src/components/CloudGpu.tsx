import { useMutation } from "@connectrpc/connect-query";
import { Alert, Anchor, Autocomplete, Badge, Button, Card, Code, Collapse, Group, NumberInput, PasswordInput, Select, SimpleGrid, Stack, Switch, Text, TextInput, Title } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { IconAlertCircle } from "@tabler/icons-react";
import { useQueryClient, useSuspenseQuery } from "@tanstack/react-query";
import { Children, useEffect, useState } from "react";

import { message } from "../api/client";
import { WorkerService, type CloudGpu as Gpu, type CloudGpuProvider } from "../api/gen/mimir/v1/worker_pb";
import { allWorkerQueries, cloudGpu } from "../api/queries";
import { ago } from "../lib/format";

// HELP says, for each provider, where its credentials come from and what
// its prices mean. The GPUs and prices themselves come from the server.
const HELP: Record<string, { site: string; keys: React.ReactNode; prices: string }> = {
  modal: {
    site: "https://modal.com",
    keys: <>From Settings → API tokens on modal.com. Leave both empty to use <Code>MODAL_TOKEN_ID</Code> and <Code>MODAL_TOKEN_SECRET</Code> from the console's environment.</>,
    prices: "Prices are Modal's, per hour the machine runs; its CPU and memory add about $0.24.",
  },
  aws: {
    site: "https://aws.amazon.com/ec2/",
    keys: <>An access key of an IAM user allowed to run, describe and terminate EC2 instances. Leave both empty to use the console's own AWS credentials. A new account must first ask AWS to raise its quota for G instances.</>,
    prices: "On-demand prices in us-east-1, per hour; other regions differ a little, and the 100 GB disk adds about a cent.",
  },
  gcp: {
    site: "https://cloud.google.com/compute",
    keys: <>Paste the JSON key of a service account with the Compute Instance Admin role, from IAM → Service accounts. The machine starts in that account's project, in a zone that has the GPU.</>,
    prices: "On-demand prices in us-central1, per hour, with the machine around the GPU.",
  },
  runpod: {
    site: "https://www.runpod.io",
    keys: <>From Settings → API Keys on runpod.io.</>,
    prices: "RunPod's Secure Cloud prices, per hour the pod runs.",
  },
  lambda: {
    site: "https://lambda.ai",
    keys: <>From API keys in the Lambda Cloud dashboard. Lambda puts an SSH key on every machine; name one you added there. The console never uses it.</>,
    prices: "Lambda's prices, per hour the machine runs. Lambda often runs out of a GPU in a region; try another then.",
  },
  vast: {
    site: "https://vast.ai",
    keys: <>From Account → Keys on vast.ai. It rents the cheapest verified machine with that GPU when one is needed.</>,
    prices: "Vast.ai is a market: each start takes the cheapest offer at the time, usually near these prices.",
  },
};

const STATES: Record<string, { label: string; color: string }> = {
  off: { label: "Off", color: "gray" },
  asleep: { label: "Asleep", color: "gray" },
  starting: { label: "Starting", color: "blue" },
  running: { label: "Running", color: "teal" },
  failed: { label: "Didn't start", color: "red" },
};

type Form = { enabled: boolean; provider: string; tokenId: string; tokenSecret: string; gpu: string; region: string; sshKey: string; consoleUrl: string; idleMinutes: number; maxHours: number; capacity: number; programUrl: string };

const formOf = (g: Gpu | undefined): Form => ({
  enabled: g?.enabled ?? false,
  provider: g?.provider || "modal",
  tokenId: g?.tokenId ?? "",
  tokenSecret: "",
  gpu: g?.gpu || "L4",
  region: g?.region ?? "",
  sshKey: g?.sshKey ?? "",
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

const SECRET_LABEL: Record<string, string> = {
  modal: "Token secret",
  aws: "Secret access key",
  gcp: "Service account key (JSON)",
};

// Fields lays fields out two to a row; an odd one out at the end takes the
// whole row, so the form stays even whichever provider asks for what.
function Fields({ children }: { children: React.ReactNode }) {
  const items = Children.toArray(children).filter(Boolean);
  return (
    <SimpleGrid cols={{ base: 1, sm: 2 }} spacing="md" verticalSpacing="sm">
      {items.map((c, i) => (items.length % 2 === 1 && i === items.length - 1 ? <div key={i} style={{ gridColumn: "1 / -1" }}>{c}</div> : c))}
    </SimpleGrid>
  );
}

// CloudGpu sets up renting a GPU for training: the console starts one on
// the chosen provider when a run waits that nothing else takes, and it
// stops once it has had nothing to do for a few minutes.
export function CloudGpu() {
  const { data } = useSuspenseQuery({ ...cloudGpu, refetchInterval: 5000 });
  const g = data.cloudGpu;
  const providers = data.providers;
  const queryClient = useQueryClient();
  const [form, setForm] = useState<Form>(() => formOf(g));
  const [advanced, setAdvanced] = useState(!!g?.programUrl || (g ? !g.canServeProgram : false));
  const saved = g?.workerId ?? "";
  // Start over from what is stored when it changes elsewhere.
  useEffect(() => setForm(formOf(g)), [saved]);
  const set = (p: Partial<Form>) => setForm((f) => ({ ...f, ...p }));
  const provider: CloudGpuProvider | undefined = providers.find((p) => p.id === form.provider);
  const name = provider?.name ?? "the provider";
  const savedName = providers.find((p) => p.id === g?.provider)?.name ?? "the provider";
  const help = HELP[form.provider] ?? HELP.modal!;
  // A secret stored for this provider is kept when the field is empty.
  const hasSecret = !!g?.hasTokenSecret && g.provider === form.provider;
  const pick = (id: string | null) => {
    const p = providers.find((x) => x.id === id);
    if (!p) return;
    const same = p.id === g?.provider;
    set({ provider: p.id, gpu: same ? g!.gpu : p.defaultGpu, region: same ? g!.region : p.defaultRegion, tokenId: same ? g!.tokenId : "", tokenSecret: "" });
  };
  const save = useMutation(WorkerService.method.updateCloudGpu, {
    onSuccess: async (res) => {
      await queryClient.invalidateQueries({ queryKey: allWorkerQueries });
      setForm(formOf(res.cloudGpu));
      const on = providers.find((p) => p.id === res.cloudGpu?.provider);
      notifications.show({ color: "teal", message: res.cloudGpu?.enabled ? `Training runs that wait will start a ${on?.gpus.find((x) => x.id === res.cloudGpu?.gpu)?.label.split(" ·")[0] ?? res.cloudGpu.gpu} on ${on?.name}.` : "Renting a GPU is off." });
    },
  });
  const stop = useMutation(WorkerService.method.stopCloudGpu, {
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: allWorkerQueries });
      notifications.show({ color: "teal", message: `The GPU on ${savedName} is stopped. Runs on it went back in the queue.` });
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
          When a training run waits that nothing here takes, the console starts a machine with a GPU on <Anchor href={help.site} target="_blank" rel="noreferrer">{name}</Anchor>, trains there, and removes it once it has had nothing to do for a few minutes, so you pay only while it trains. The models it trains are answered by your other workers, which get the new weights with the next question.
        </Text>
        {g?.state === "asleep" && <Text size="sm">Nothing is waiting for it. It starts when a run waits.</Text>}
        {g?.state === "starting" && <Text size="sm">Starting on {savedName}{g.stateTime ? `, ${ago(g.stateTime)}` : ""}. A machine takes from one to a few minutes to boot and join.</Text>}
        {g?.state === "running" && <Text size="sm">Running and training. It shows as <b>Rented GPU</b> under Workers above.</Text>}
        {g?.state === "failed" && <Alert color="red" icon={<IconAlertCircle />}>{g.error}</Alert>}
        {live && (
          <Group>
            <Button variant="default" color="red" size="xs" loading={stop.isPending} onClick={() => stop.mutate({})}>Stop it now</Button>
            {stop.error && <Text size="sm" c="red">{message(stop.error)}</Text>}
          </Group>
        )}

        <Switch label="Start a GPU when a training run waits" checked={form.enabled} onChange={(e) => set({ enabled: e.currentTarget.checked })} />
        <Collapse expanded={form.enabled}>
          <Stack gap="sm">
            <SimpleGrid cols={{ base: 1, sm: 2 }} spacing="md" verticalSpacing="sm">
              <Select label="Rent from" data={providers.map((p) => ({ value: p.id, label: p.name }))} value={form.provider} onChange={pick} allowDeselect={false} />
              <Select label="GPU" data={(provider?.gpus ?? []).map((x) => ({ value: x.id, label: x.label }))} value={form.gpu} onChange={(v) => v && set({ gpu: v })} allowDeselect={false} />
            </SimpleGrid>
            <Hint>{help.prices} A T4, L4 or A10 suits table, text and small photo models; a machine with 4 or 8 GPUs trains a big run on all of them at once.</Hint>
            <Fields>
              {(form.provider === "modal" || form.provider === "aws") && (
                <TextInput label={form.provider === "aws" ? "Access key id" : "Modal token id"} placeholder={form.provider === "aws" ? "AKIA…" : "ak-…"} value={form.tokenId} onChange={(e) => set({ tokenId: e.currentTarget.value })} />
              )}
              <PasswordInput label={SECRET_LABEL[form.provider] ?? "API key"} placeholder={hasSecret ? "Saved; leave empty to keep" : form.provider === "modal" ? "as-…" : form.provider === "gcp" ? "{ \"type\": \"service_account\", … }" : ""} value={form.tokenSecret} onChange={(e) => set({ tokenSecret: e.currentTarget.value })} />
              {form.provider === "lambda" && <TextInput label="SSH key name" placeholder="As named in Lambda Cloud" value={form.sshKey} onChange={(e) => set({ sshKey: e.currentTarget.value })} />}
              {(provider?.regions.length ?? 0) > 0 && (
                <Autocomplete label={form.provider === "gcp" ? "Zone" : "Region"} data={provider!.regions} value={form.region} onChange={(v) => set({ region: v })} />
              )}
              <TextInput label="This console's address" placeholder="https://mimir.example.com" value={form.consoleUrl} onChange={(e) => set({ consoleUrl: e.currentTarget.value })} />
            </Fields>
            <Hint>{help.keys} Secrets are stored encrypted and never shown again. The address is where the GPU machine reaches this console over https, from the internet.</Hint>
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
          A run goes to it when nothing else takes it. To send a run there anyway, pick <b>Rented GPU</b> under <b>Train on</b> when you start it. Turn off <b>Training on this console</b> above, or runs placed on <i>Automatic</i> train here instead.
        </Text>
      </Stack>
    </Card>
  );
}
