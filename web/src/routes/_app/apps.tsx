import { useMutation } from "@connectrpc/connect-query";
import { ActionIcon, Alert, Button, Card, Code, CopyButton, Group, Modal, Stack, Table, Tabs, Text, TextInput, Title, Tooltip } from "@mantine/core";
import { useDisclosure } from "@mantine/hooks";
import { notifications } from "@mantine/notifications";
import { IconAlertCircle, IconCheck, IconCopy, IconKey, IconTrash } from "@tabler/icons-react";
import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import { z } from "zod";

import { message } from "../../api/client";
import { ApiKeyService, type ApiKey } from "../../api/gen/mimir/v1/access_pb";
import type { Model } from "../../api/gen/mimir/v1/model_pb";
import { allKeyQueries, listApiKeys, listModels, listWebhooks, whoAmI } from "../../api/queries";
import { AccessPicker, describe, type AccessValue } from "../../components/AccessPicker";
import { Page } from "../../components/Page";
import { StatusPill } from "../../components/ui";
import { CodeBlock } from "../../components/CodeBlock";
import { Usage } from "../../components/Usage";
import { Webhooks } from "../../components/Webhooks";
import { ago } from "../../lib/format";
import { t } from "../../i18n";

const tabNames = ["keys", "openai", "usage", "webhooks"] as const;
type Tab = (typeof tabNames)[number];

export const Route = createFileRoute("/_app/apps")({
  validateSearch: z.object({ tab: z.enum(tabNames).optional() }),
  loader: async ({ context }) => {
    const me = await context.queryClient.ensureQueryData(whoAmI);
    await Promise.all([
      context.queryClient.ensureQueryData(listApiKeys),
      context.queryClient.ensureQueryData(listModels),
      me.permissions.includes("webhooks.manage") ? context.queryClient.ensureQueryData(listWebhooks) : null,
    ]);
  },
  component: Apps,
});

function Apps() {
  const { data } = useSuspenseQuery(listApiKeys);
  const { data: models } = useSuspenseQuery(listModels);
  const { data: me } = useSuspenseQuery(whoAmI);
  const { queryClient } = Route.useRouteContext();
  const { tab } = Route.useSearch();
  const navigate = Route.useNavigate();
  const [creating, create] = useDisclosure();
  const [secret, setSecret] = useState<string>();
  const [revoking, setRevoking] = useState<ApiKey>();
  const names = new Map(models.models.map((m) => [m.id, m.name]));
  const refresh = () => queryClient.invalidateQueries({ queryKey: allKeyQueries });
  const revoke = useMutation(ApiKeyService.method.revokeApiKey, {
    onSuccess: async () => { await refresh(); notifications.show({ message: t("{name} no longer works.", { name: revoking?.name }) }); setRevoking(undefined); },
  });
  const example = models.models[0];
  const curl = `curl ${location.origin}/mimir.v1.PredictService/Predict \\
  -H 'Authorization: Bearer mimir_...' \\
  -H 'Content-Type: application/json' \\
  -d '{"modelId": "${example?.id ?? t("<model id>")}", "input": {"values": [ /* 784 numbers from 0 to 1 */ ]}}'`;
  const grpc = `grpcurl -plaintext -H 'Authorization: Bearer mimir_...' \\
  -d '{"model_id": "${example?.id ?? t("<model id>")}", "input": {"values": [...]}}' \\
  ${location.host} mimir.v1.PredictService/Predict`;

  const tabs = [["keys", t("Keys")], ["openai", t("Use it like OpenAI")], ["usage", t("Usage")], ...(me.permissions.includes("webhooks.manage") ? [["webhooks", t("Webhooks")]] : [])] as const;
  const shown = tabs.some(([t]) => t === tab) ? tab : "keys";

  return (
    <Page title={t("Connect your apps")} intro={t("Apps ask your models questions with a key. Give each app its own, limited to the models it needs.")} actions={data.apiKeys.length > 0 && <Button leftSection={<IconKey size={18} />} onClick={create.open}>{t("Make a key")}</Button>}>
      <Tabs value={shown} onChange={(t) => void navigate({ search: { tab: (t ?? "keys") as Tab }, replace: true })}>
        <Tabs.List>{tabs.map(([v, l]) => <Tabs.Tab key={v} value={v}>{l}</Tabs.Tab>)}</Tabs.List>

        <Tabs.Panel value="keys" pt="lg">
          <Stack gap="lg">
            {data.apiKeys.length === 0 ? (
              <Card withBorder padding="xl" className="text-center">
                <Text fw={650}>{t("No keys yet")}</Text>
                <Text c="dimmed" mb="md">{t("Make one and paste it into your app.")}</Text>
                <Group justify="center"><Button leftSection={<IconKey size={18} />} onClick={create.open}>{t("Make a key")}</Button></Group>
              </Card>
            ) : (
              <Card withBorder padding={0}>
                <Table highlightOnHover>
                  <Table.Thead>
                    <Table.Tr>
                      <Table.Th>{t("Key")}</Table.Th>
                      <Table.Th visibleFrom="sm">{t("Can use")}</Table.Th>
                      <Table.Th visibleFrom="md">{t("Last used")}</Table.Th>
                      <Table.Th>{t("Status")}</Table.Th>
                      <Table.Th w={48} />
                    </Table.Tr>
                  </Table.Thead>
                  <Table.Tbody>
                    {data.apiKeys.map((k) => {
                      const recent = !!k.lastUsedTime && Date.now() - Number(k.lastUsedTime.seconds) * 1000 < 30 * 86_400_000;
                      return (
                        <Table.Tr key={k.id}>
                          <Table.Td>
                            <Text fw={650} truncate>{k.name}</Text>
                            <Text size="xs" c="dimmed" ff="monospace">{k.prefix}…</Text>
                          </Table.Td>
                          <Table.Td visibleFrom="sm" c="dimmed">{describe(k.access, names)}</Table.Td>
                          <Table.Td visibleFrom="md" c="dimmed">{k.lastUsedTime ? ago(k.lastUsedTime) : t("Never")}</Table.Td>
                          <Table.Td><StatusPill tone={recent ? "ok" : "idle"}>{recent ? t("Working") : k.lastUsedTime ? t("Not used lately") : t("Not used yet")}</StatusPill></Table.Td>
                          <Table.Td>
                            <Tooltip label={t("Revoke")}>
                              <ActionIcon variant="subtle" color="red" aria-label={t("Revoke {name}", { name: k.name })} onClick={() => setRevoking(k)}><IconTrash size={16} /></ActionIcon>
                            </Tooltip>
                          </Table.Td>
                        </Table.Tr>
                      );
                    })}
                  </Table.Tbody>
                </Table>
              </Card>
            )}

            <Card withBorder padding="lg">
              <Title order={2} mb={4}>{t("Ask a model from your code")}</Title>
              <Text size="sm" c="dimmed" mb="sm">{t("Put your key in the Authorization header. Every model works over HTTP and gRPC.")}</Text>
              <Tabs defaultValue="http">
                <Tabs.List><Tabs.Tab value="http">{t("HTTP")}</Tabs.Tab><Tabs.Tab value="grpc">{t("gRPC")}</Tabs.Tab></Tabs.List>
                <Tabs.Panel value="http" pt="sm"><CodeBlock code={curl} /></Tabs.Panel>
                <Tabs.Panel value="grpc" pt="sm"><CodeBlock code={grpc} /></Tabs.Panel>
              </Tabs>
            </Card>
          </Stack>
        </Tabs.Panel>

        <Tabs.Panel value="openai" pt="lg"><OpenAICard models={models.models.filter((m) => m.language)} /></Tabs.Panel>
        <Tabs.Panel value="usage" pt="lg"><Usage userId={me.user?.id ?? ""} /></Tabs.Panel>
        {me.permissions.includes("webhooks.manage") && <Tabs.Panel value="webhooks" pt="lg"><Webhooks /></Tabs.Panel>}
      </Tabs>

      <KeyModal key={creating ? "open" : "closed"} opened={creating} onClose={create.close} maxRole={me.user?.access?.role ?? 1} userAllModels={me.user?.access?.allModels ?? false} onCreated={async (s) => { await refresh(); setSecret(s); create.close(); }} />

      <Modal opened={!!secret} onClose={() => setSecret(undefined)} title={t("Your new key")} centered closeOnClickOutside={false}>
        <Alert color="yellow" mb="md">{t("Copy it now. For safety, it's shown only this once.")}</Alert>
        <Group wrap="nowrap" gap="xs">
          <Code block className="flex-1 break-all">{secret}</Code>
          <CopyButton value={secret ?? ""}>
            {({ copied, copy }) => <Button onClick={copy} leftSection={copied ? <IconCheck size={16} /> : <IconCopy size={16} />}>{copied ? t("Copied") : t("Copy")}</Button>}
          </CopyButton>
        </Group>
        <Group justify="flex-end" mt="lg"><Button onClick={() => setSecret(undefined)}>{t("I've saved it")}</Button></Group>
      </Modal>

      <Modal opened={!!revoking} onClose={() => setRevoking(undefined)} title={t("Revoke {name}?", { name: revoking?.name })} centered>
        <Text mb="lg">{t("Any app using this key stops getting answers right away.")}</Text>
        {revoke.error && <Alert color="red" mb="md" icon={<IconAlertCircle />}>{message(revoke.error)}</Alert>}
        <Group justify="flex-end">
          <Button variant="default" onClick={() => setRevoking(undefined)}>{t("Keep it")}</Button>
          <Button color="red" loading={revoke.isPending} onClick={() => revoking && revoke.mutate({ id: revoking.id })}>{t("Revoke")}</Button>
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
    <Modal opened={opened} onClose={onClose} title={t("Make a key")} centered size="lg">
      <form onSubmit={(e) => { e.preventDefault(); create.mutate({ name, access }); }}>
        <Stack>
          <TextInput label={t("What is this key for?")} placeholder={t("e.g. Shop checkout")} required maxLength={100} value={name} onChange={(e) => setName(e.currentTarget.value)} data-autofocus />
          <AccessPicker value={access} onChange={setAccess} maxRole={Math.min(maxRole, 2)} subject={t("this key")} />
          {create.error && <Alert color="red" icon={<IconAlertCircle />}>{message(create.error)}</Alert>}
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>{t("Cancel")}</Button>
            <Button type="submit" loading={create.isPending}>{t("Make key")}</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}

// OpenAICard shows how apps made for OpenAI reach the language models.
function OpenAICard({ models }: { models: Model[] }) {
  const base = `${location.origin}/v1`;
  const name = models[0]?.language?.apiName ?? t("<model name>");
  const curl = `curl ${base}/chat/completions \\
  -H 'Authorization: Bearer mimir_...' \\
  -H 'Content-Type: application/json' \\
  -d '{
    "model": "${name}",
    "stream": true,
    "messages": [{"role": "user", "content": "Hello!"}]
  }'`;
  const python = `from openai import OpenAI

client = OpenAI(base_url="${base}", api_key="mimir_...")

for part in client.chat.completions.create(
        model="${name}", stream=True,
        messages=[{"role": "user", "content": "Hello!"}]):
    print(part.choices[0].delta.content or "", end="")`;
  const js = `import OpenAI from "openai";

const client = new OpenAI({ baseURL: "${base}", apiKey: "mimir_..." });

const stream = await client.chat.completions.create({
  model: "${name}",
  stream: true,
  messages: [{ role: "user", content: "Hello!" }],
});
for await (const part of stream) process.stdout.write(part.choices[0]?.delta?.content ?? "");`;
  return (
    <Card withBorder radius="lg" padding="lg">
      <Title order={2} fz="lg" mb={4}>{t("Use it like OpenAI")}</Title>
      <Text size="sm" c="dimmed" mb="md">{t("Apps and tools built for OpenAI can talk to your language models. Give them this address and a mimir key instead of an OpenAI one. Nothing leaves your server.")}</Text>
      <Stack gap="md">
        <div>
          <Text size="sm" fw={500} mb={4}>{t("Address (base URL)")}</Text>
          <Group gap="xs" wrap="nowrap">
            <Code block className="flex-1 break-all">{base}</Code>
            <CopyButton value={base}>
              {({ copied, copy }) => <Button variant="default" onClick={copy} leftSection={copied ? <IconCheck size={16} /> : <IconCopy size={16} />}>{copied ? t("Copied") : t("Copy")}</Button>}
            </CopyButton>
          </Group>
        </div>
        <div>
          <Text size="sm" fw={500} mb={4}>{t("Model names")}</Text>
          {models.length === 0 ? (
            <Text size="sm" c="dimmed">{t("No language models yet. Add one from a GGUF file under Models, and its name shows up here.")}</Text>
          ) : (
            <Table.ScrollContainer minWidth={360}>
              <Table withTableBorder verticalSpacing="xs">
                <Table.Thead><Table.Tr><Table.Th>{t("Model")}</Table.Th><Table.Th>{t("Name to send")}</Table.Th></Table.Tr></Table.Thead>
                <Table.Tbody>
                  {models.map((m) => (
                    <Table.Tr key={m.id}><Table.Td>{m.name}</Table.Td><Table.Td><Code>{m.language?.apiName}</Code></Table.Td></Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            </Table.ScrollContainer>
          )}
          <Text size="xs" c="dimmed" mt={4}>{t("A key only sees the models it's allowed to use. The name follows the model's name, so renaming a model changes it.")}</Text>
        </div>
        <Tabs defaultValue="curl">
          <Tabs.List>
            <Tabs.Tab value="curl">{t("curl")}</Tabs.Tab>
            <Tabs.Tab value="python">{t("Python")}</Tabs.Tab>
            <Tabs.Tab value="js">{t("JavaScript")}</Tabs.Tab>
          </Tabs.List>
          <Tabs.Panel value="curl" pt="sm"><Code block>{curl}</Code></Tabs.Panel>
          <Tabs.Panel value="python" pt="sm"><Code block>{python}</Code></Tabs.Panel>
          <Tabs.Panel value="js" pt="sm"><Code block>{js}</Code></Tabs.Panel>
        </Tabs>
        <Text size="sm" c="dimmed">{t("Works with")}{" "}<Code>/v1/chat/completions</Code>, <Code>/v1/completions</Code>{" "}{t("and")}{" "}<Code>/v1/models</Code>{t(". Add")}{" "}<Code>"stream": true</Code>{" "}{t("to get the answer word by word as it's written.")}</Text>
      </Stack>
    </Card>
  );
}
