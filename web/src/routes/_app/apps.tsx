import { useMutation } from "@connectrpc/connect-query";
import { Alert, Button, Card, Code, CopyButton, Group, Modal, Stack, Tabs, Text, TextInput, Title } from "@mantine/core";
import { useDisclosure } from "@mantine/hooks";
import { notifications } from "@mantine/notifications";
import { IconAlertCircle, IconCheck, IconCopy, IconKey, IconTrash } from "@tabler/icons-react";
import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";

import { message } from "../../api/client";
import { ApiKeyService, type ApiKey } from "../../api/gen/mimir/v1/access_pb";
import { allKeyQueries, listApiKeys, listModels, whoAmI } from "../../api/queries";
import { AccessPicker, describe, type AccessValue } from "../../components/AccessPicker";
import { Page } from "../../components/Page";
import { ago } from "../../lib/format";

export const Route = createFileRoute("/_app/apps")({
  loader: ({ context }) => Promise.all([
    context.queryClient.ensureQueryData(listApiKeys),
    context.queryClient.ensureQueryData(listModels),
  ]),
  component: Apps,
});

function Apps() {
  const { data } = useSuspenseQuery(listApiKeys);
  const { data: models } = useSuspenseQuery(listModels);
  const { data: me } = useSuspenseQuery(whoAmI);
  const { queryClient } = Route.useRouteContext();
  const [creating, create] = useDisclosure();
  const [secret, setSecret] = useState<string>();
  const [revoking, setRevoking] = useState<ApiKey>();
  const names = new Map(models.models.map((m) => [m.id, m.name]));
  const refresh = () => queryClient.invalidateQueries({ queryKey: allKeyQueries });
  const revoke = useMutation(ApiKeyService.method.revokeApiKey, {
    onSuccess: async () => { await refresh(); notifications.show({ message: `${revoking?.name} no longer works.` }); setRevoking(undefined); },
  });
  const example = models.models[0];
  const curl = `curl ${location.origin}/mimir.v1.PredictService/Predict \\
  -H 'Authorization: Bearer mimir_...' \\
  -H 'Content-Type: application/json' \\
  -d '{"modelId": "${example?.id ?? "<model id>"}", "input": {"values": [ /* 784 numbers from 0 to 1 */ ]}}'`;
  const grpc = `grpcurl -plaintext -H 'Authorization: Bearer mimir_...' \\
  -d '{"model_id": "${example?.id ?? "<model id>"}", "input": {"values": [...]}}' \\
  ${location.host} mimir.v1.PredictService/Predict`;

  return (
    <Page title="Connect your apps" intro="Give each app its own key, limited to the models it needs." actions={<Button leftSection={<IconKey size={18} />} onClick={create.open}>Make a key</Button>}>
      <Stack gap="sm">
        <Title order={2} fz="lg">Your API keys</Title>
        {data.apiKeys.length === 0 && (
          <Card withBorder radius="lg" padding="xl" className="text-center">
            <Text fw={600}>No keys yet</Text>
            <Text c="dimmed">Make one and paste it into your app.</Text>
          </Card>
        )}
        {data.apiKeys.map((k) => (
          <Card key={k.id} withBorder radius="lg" padding="md">
            <Group justify="space-between" wrap="nowrap">
              <div className="min-w-0">
                <Text fw={700} truncate>{k.name}</Text>
                <Text size="sm" c="dimmed"><Code>{k.prefix}…</Code> · {describe(k.access, names)}</Text>
                <Text size="xs" c="dimmed">{k.lastUsedTime ? `Last used ${ago(k.lastUsedTime)}` : "Never used"} · made {ago(k.createTime)}</Text>
              </div>
              <Button variant="subtle" color="red" size="sm" leftSection={<IconTrash size={16} />} onClick={() => setRevoking(k)}>Revoke</Button>
            </Group>
          </Card>
        ))}
      </Stack>

      <Card withBorder radius="lg" padding="lg">
        <Title order={2} fz="lg" mb={4}>Ask a model from your code</Title>
        <Text size="sm" c="dimmed" mb="sm">Put your key in the Authorization header. Every model works over HTTP and gRPC.</Text>
        <Tabs defaultValue="http">
          <Tabs.List><Tabs.Tab value="http">HTTP</Tabs.Tab><Tabs.Tab value="grpc">gRPC</Tabs.Tab></Tabs.List>
          <Tabs.Panel value="http" pt="sm"><Code block>{curl}</Code></Tabs.Panel>
          <Tabs.Panel value="grpc" pt="sm"><Code block>{grpc}</Code></Tabs.Panel>
        </Tabs>
      </Card>

      <Card withBorder radius="lg" padding="lg" className="border-dashed">
        <Text fw={600}>Webhooks: coming soon</Text>
        <Text size="sm" c="dimmed">Your apps will get a call when a model changes or training finishes.</Text>
      </Card>

      <KeyModal key={creating ? "open" : "closed"} opened={creating} onClose={create.close} maxRole={me.user?.access?.role ?? 1} userAllModels={me.user?.access?.allModels ?? false} onCreated={async (s) => { await refresh(); setSecret(s); create.close(); }} />

      <Modal opened={!!secret} onClose={() => setSecret(undefined)} title="Your new key" centered closeOnClickOutside={false}>
        <Alert color="yellow" mb="md">Copy it now. For safety, it's shown only this once.</Alert>
        <Group wrap="nowrap" gap="xs">
          <Code block className="flex-1 break-all">{secret}</Code>
          <CopyButton value={secret ?? ""}>
            {({ copied, copy }) => <Button onClick={copy} leftSection={copied ? <IconCheck size={16} /> : <IconCopy size={16} />}>{copied ? "Copied" : "Copy"}</Button>}
          </CopyButton>
        </Group>
        <Group justify="flex-end" mt="lg"><Button onClick={() => setSecret(undefined)}>I've saved it</Button></Group>
      </Modal>

      <Modal opened={!!revoking} onClose={() => setRevoking(undefined)} title={`Revoke ${revoking?.name}?`} centered>
        <Text mb="lg">Any app using this key stops getting answers right away.</Text>
        {revoke.error && <Alert color="red" mb="md" icon={<IconAlertCircle />}>{message(revoke.error)}</Alert>}
        <Group justify="flex-end">
          <Button variant="default" onClick={() => setRevoking(undefined)}>Keep it</Button>
          <Button color="red" loading={revoke.isPending} onClick={() => revoking && revoke.mutate({ id: revoking.id })}>Revoke</Button>
        </Group>
      </Modal>
    </Page>
  );
}

function KeyModal({ opened, onClose, maxRole, userAllModels, onCreated }: { opened: boolean; onClose: () => void; maxRole: number; userAllModels: boolean; onCreated: (secret: string) => Promise<void> }) {
  const [name, setName] = useState("");
  const [access, setAccess] = useState<AccessValue>({ role: 1, allModels: userAllModels, modelIds: [] });
  const create = useMutation(ApiKeyService.method.createApiKey, { onSuccess: (res) => onCreated(res.secret) });
  return (
    <Modal opened={opened} onClose={onClose} title="Make a key" centered size="lg">
      <form onSubmit={(e) => { e.preventDefault(); create.mutate({ name, access }); }}>
        <Stack>
          <TextInput label="What is this key for?" placeholder="e.g. Shop checkout" required maxLength={100} value={name} onChange={(e) => setName(e.currentTarget.value)} data-autofocus />
          <AccessPicker value={access} onChange={setAccess} maxRole={Math.min(maxRole, 2)} subject="this key" />
          {create.error && <Alert color="red" icon={<IconAlertCircle />}>{message(create.error)}</Alert>}
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>Cancel</Button>
            <Button type="submit" loading={create.isPending}>Make key</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}
