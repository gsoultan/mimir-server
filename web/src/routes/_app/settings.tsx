import { timestampDate } from "@bufbuild/protobuf/wkt";
import { useMutation } from "@connectrpc/connect-query";
import { Alert, Badge, Button, Card, Group, NumberInput, PasswordInput, Radio, SimpleGrid, Stack, Switch, Table, Tabs, TagsInput, Text, TextInput, Title } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { IconAlertCircle, IconDownload } from "@tabler/icons-react";
import { useQueryClient, useSuspenseInfiniteQuery, useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import { z } from "zod";

import { message } from "../../api/client";
import { SettingsService, TwoFactorPolicy } from "../../api/gen/mimir/v1/settings_pb";
import { allSettingsQueries, auditLog, databaseInfo, settings, whoAmI } from "../../api/queries";
import { AnchorLink } from "../../components/links";
import { Page } from "../../components/Page";
import { Workers } from "../../components/Workers";
import { ago, bytes } from "../../lib/format";

export const Route = createFileRoute("/_app/settings")({
  validateSearch: z.object({ tab: z.enum(["audit", "database", "alerts", "retention", "security", "workers"]).optional() }),
  loader: ({ context }) => Promise.all([
    context.queryClient.ensureInfiniteQueryData(auditLog),
    context.queryClient.ensureQueryData(settings),
    context.queryClient.ensureQueryData(databaseInfo),
  ]),
  component: Settings,
});

function Settings() {
  const { tab } = Route.useSearch();
  const navigate = Route.useNavigate();
  return (
    <Page title="Settings" intro="How this server stores data and keeps you informed.">
      <Tabs value={tab ?? "audit"} onChange={(t) => navigate({ search: { tab: (t ?? "audit") as "audit" }, replace: true })}>
        <Tabs.List>
          <Tabs.Tab value="audit">Audit log</Tabs.Tab>
          <Tabs.Tab value="database">Database</Tabs.Tab>
          <Tabs.Tab value="alerts">Alerts</Tabs.Tab>
          <Tabs.Tab value="retention">Log retention</Tabs.Tab>
          <Tabs.Tab value="security">Security</Tabs.Tab>
          <Tabs.Tab value="workers">Workers</Tabs.Tab>
        </Tabs.List>
        <Tabs.Panel value="audit" pt="md"><AuditLog /></Tabs.Panel>
        <Tabs.Panel value="database" pt="md"><Database /></Tabs.Panel>
        <Tabs.Panel value="alerts" pt="md"><Alerts /></Tabs.Panel>
        <Tabs.Panel value="retention" pt="md"><Retention /></Tabs.Panel>
        <Tabs.Panel value="security" pt="md"><SecurityPolicy /></Tabs.Panel>
        <Tabs.Panel value="workers" pt="md"><Workers /></Tabs.Panel>
      </Tabs>
    </Page>
  );
}

function Database() {
  const { data } = useSuspenseQuery(databaseInfo);
  const upToDate = data.schemaVersion === data.latestSchemaVersion;
  const facts: [string, string][] = [
    ["Engine", "SQLite, built in"],
    ["File", data.path],
    ["Size", bytes(data.sizeBytes)],
    ["Models", String(data.models)],
    ["People", String(data.users)],
    ["Audit events", String(data.auditEvents)],
    ["Webhook deliveries", String(data.deliveries)],
    ["Last cleanup", data.lastTrimTime ? `${ago(data.lastTrimTime)}, ${data.lastTrimRows} old rows removed` : "Not yet; it runs every hour"],
  ];
  return (
    <SimpleGrid cols={{ base: 1, md: 2 }}>
      <Card withBorder radius="lg" padding="lg">
        <Group justify="space-between" mb="sm">
          <Title order={2} fz="lg">This server's database</Title>
          <Badge variant="light" color={upToDate ? "teal" : "yellow"}>{upToDate ? `Up to date (v${data.schemaVersion})` : `Update pending (v${data.schemaVersion} → v${data.latestSchemaVersion})`}</Badge>
        </Group>
        <Table verticalSpacing="xs">
          <Table.Tbody>
            {facts.map(([k, v]) => <Table.Tr key={k}><Table.Td c="dimmed" w={160}>{k}</Table.Td><Table.Td className="break-all">{v}</Table.Td></Table.Tr>)}
          </Table.Tbody>
        </Table>
        <Text size="sm" c="dimmed" mt="sm">Database updates run on their own each time the server starts, so there's nothing to click here.</Text>
      </Card>
      <Stack>
        <Card withBorder radius="lg" padding="lg">
          <Title order={2} fz="lg">Back it up</Title>
          <Text size="sm" c="dimmed" mb="md">Download a copy of the database: people, keys, settings and model details. Model files live next to it in the data folder and aren't included.</Text>
          <Button component="a" href="/api/backup" download leftSection={<IconDownload size={18} />}>Download a backup</Button>
        </Card>
        <Card withBorder radius="lg" padding="lg" className="border-dashed">
          <Badge variant="light" mb="xs">Coming soon</Badge>
          <Text fw={600}>Move to PostgreSQL</Text>
          <Text size="sm" c="dimmed">For a server with many people and workers.</Text>
        </Card>
      </Stack>
    </SimpleGrid>
  );
}

function Alerts() {
  const { data } = useSuspenseQuery(settings);
  const queryClient = useQueryClient();
  const [email, setEmail] = useState({ enabled: data.email?.enabled ?? false, host: data.email?.host ?? "", port: data.email?.port || 587, username: data.email?.username ?? "", password: "", from: data.email?.from ?? "", to: data.email?.to ?? [] });
  const [telegram, setTelegram] = useState({ enabled: data.telegram?.enabled ?? false, botToken: "", chatId: data.telegram?.chatId ?? "" });
  const save = useMutation(SettingsService.method.updateAlerts, {
    onSuccess: async () => { await queryClient.invalidateQueries({ queryKey: allSettingsQueries }); notifications.show({ color: "teal", message: "Alert settings saved." }); },
  });
  const test = useMutation(SettingsService.method.sendTestAlert, {
    onSuccess: () => notifications.show({ color: "teal", message: "Test alert sent. Check your inbox or chat." }),
    onError: (err) => notifications.show({ color: "red", message: message(err), autoClose: 8000 }),
  });
  return (
    <form onSubmit={(e) => { e.preventDefault(); save.mutate({ email, telegram }); }}>
      <Stack>
        <Text c="dimmed">You get an alert when a webhook keeps failing. More alerts come with training.</Text>
        <SimpleGrid cols={{ base: 1, md: 2 }}>
          <Card withBorder radius="lg" padding="lg">
            <Group justify="space-between" mb="sm">
              <Title order={2} fz="lg">Email</Title>
              <Switch label="On" checked={email.enabled} onChange={(e) => setEmail({ ...email, enabled: e.currentTarget.checked })} />
            </Group>
            <Stack gap="sm">
              <Group grow align="flex-start">
                <TextInput label="Mail server" placeholder="smtp.example.com" value={email.host} onChange={(e) => setEmail({ ...email, host: e.currentTarget.value })} />
                <NumberInput label="Port" min={1} max={65535} value={email.port} onChange={(v) => setEmail({ ...email, port: Number(v) || 587 })} />
              </Group>
              <TextInput label="Username" autoComplete="off" value={email.username} onChange={(e) => setEmail({ ...email, username: e.currentTarget.value })} />
              <PasswordInput label="Password" autoComplete="new-password" placeholder={data.email?.passwordSet ? "Saved; leave empty to keep it" : ""} value={email.password} onChange={(e) => setEmail({ ...email, password: e.currentTarget.value })} />
              <TextInput label="From" type="email" placeholder="mimir@example.com" value={email.from} onChange={(e) => setEmail({ ...email, from: e.currentTarget.value })} />
              <TagsInput label="Send to" description="Press Enter after each address." placeholder="you@example.com" value={email.to} onChange={(to) => setEmail({ ...email, to })} />
              <Button variant="default" className="self-start" loading={test.isPending && test.variables?.channel === "email"} onClick={() => test.mutate({ channel: "email" })} disabled={!data.email?.enabled}>Send a test email</Button>
            </Stack>
          </Card>
          <Card withBorder radius="lg" padding="lg">
            <Group justify="space-between" mb="sm">
              <Title order={2} fz="lg">Telegram</Title>
              <Switch label="On" checked={telegram.enabled} onChange={(e) => setTelegram({ ...telegram, enabled: e.currentTarget.checked })} />
            </Group>
            <Stack gap="sm">
              <Text size="sm" c="dimmed">Make a bot with @BotFather, start a chat with it, then put its token and your chat id here.</Text>
              <PasswordInput label="Bot token" autoComplete="off" placeholder={data.telegram?.tokenSet ? "Saved; leave empty to keep it" : "123456:ABC..."} value={telegram.botToken} onChange={(e) => setTelegram({ ...telegram, botToken: e.currentTarget.value })} />
              <TextInput label="Chat id" placeholder="e.g. 123456789" value={telegram.chatId} onChange={(e) => setTelegram({ ...telegram, chatId: e.currentTarget.value })} />
              <Button variant="default" className="self-start" loading={test.isPending && test.variables?.channel === "telegram"} onClick={() => test.mutate({ channel: "telegram" })} disabled={!data.telegram?.enabled}>Send a test message</Button>
            </Stack>
          </Card>
        </SimpleGrid>
        {save.error && <Alert color="red" icon={<IconAlertCircle />}>{message(save.error)}</Alert>}
        <Group><Button type="submit" loading={save.isPending}>Save alert settings</Button><Text size="sm" c="dimmed">Save first, then send a test.</Text></Group>
      </Stack>
    </form>
  );
}

function Retention() {
  const { data } = useSuspenseQuery(settings);
  const queryClient = useQueryClient();
  const [audit, setAudit] = useState(data.retention?.auditDays ?? 365);
  const [delivery, setDelivery] = useState(data.retention?.deliveryDays ?? 30);
  const save = useMutation(SettingsService.method.updateRetention, {
    onSuccess: async () => { await queryClient.invalidateQueries({ queryKey: allSettingsQueries }); notifications.show({ color: "teal", message: "Retention saved." }); },
  });
  return (
    <Card withBorder radius="lg" padding="lg" className="max-w-xl">
      <form onSubmit={(e) => { e.preventDefault(); save.mutate({ retention: { auditDays: audit, deliveryDays: delivery } }); }}>
        <Stack>
          <Text c="dimmed">Old history is removed once an hour. Shorter keeps the database small; longer keeps more to look back on.</Text>
          <NumberInput label="Keep audit events for" suffix=" days" description="0 keeps them forever." min={0} max={3650} value={audit} onChange={(v) => setAudit(Number(v) || 0)} />
          <NumberInput label="Keep webhook delivery history for" suffix=" days" min={1} max={3650} value={delivery} onChange={(v) => setDelivery(Number(v) || 1)} />
          {save.error && <Alert color="red" icon={<IconAlertCircle />}>{message(save.error)}</Alert>}
          <Button type="submit" loading={save.isPending} className="self-start">Save</Button>
        </Stack>
      </form>
    </Card>
  );
}

function SecurityPolicy() {
  const { data } = useSuspenseQuery(settings);
  const { data: me } = useSuspenseQuery(whoAmI);
  const queryClient = useQueryClient();
  const [policy, setPolicy] = useState(String(data.security?.twoFactor || TwoFactorPolicy.OPTIONAL));
  const save = useMutation(SettingsService.method.updateSecurity, {
    onSuccess: async () => { await queryClient.invalidateQueries({ queryKey: allSettingsQueries }); notifications.show({ color: "teal", message: "Security settings saved." }); },
  });
  return (
    <Card withBorder radius="lg" padding="lg" className="max-w-xl">
      <form onSubmit={(e) => { e.preventDefault(); save.mutate({ security: { twoFactor: Number(policy) as TwoFactorPolicy } }); }}>
        <Stack>
          <Radio.Group label="Who must use two-step verification" description="People without it set up can only do that until it's on. API keys keep working." value={policy} onChange={setPolicy}>
            <Stack gap="xs" mt="xs">
              <Radio value={String(TwoFactorPolicy.OPTIONAL)} label="Nobody; each person decides" />
              <Radio value={String(TwoFactorPolicy.ADMINS)} label="Admins and owners" />
              <Radio value={String(TwoFactorPolicy.EVERYONE)} label="Everyone" />
            </Stack>
          </Radio.Group>
          {!me.user?.twoFactor && policy !== String(TwoFactorPolicy.OPTIONAL) && (
            <Alert color="yellow" icon={<IconAlertCircle />}>Turn it on for your own account first, under <AnchorLink to="/security">Security</AnchorLink>.</Alert>
          )}
          {save.error && <Alert color="red" icon={<IconAlertCircle />}>{message(save.error)}</Alert>}
          <Button type="submit" loading={save.isPending} className="self-start">Save</Button>
        </Stack>
      </form>
    </Card>
  );
}

// Plain words for each audit action.
const actions: Record<string, string> = {
  "server.setup": "set up the server",
  "user.signin": "signed in",
  "user.signin_failed": "failed to sign in",
  "user.password_changed": "changed their password",
  "user.password_reset": "reset the password of",
  "user.create": "added",
  "user.update": "changed",
  "user.delete": "removed",
  "apikey.create": "made the API key",
  "apikey.revoke": "revoked an API key",
  "model.create": "added the model",
  "model.update": "changed the model",
  "model.delete": "deleted the model",
  "webhook.create": "added the webhook",
  "webhook.update": "changed the webhook",
  "webhook.delete": "removed the webhook",
  "settings.alerts": "changed alert settings",
  "settings.retention": "changed log retention",
  "settings.security": "changed security settings",
  "dataset.create": "added the examples",
  "dataset.delete": "removed the examples",
  "dataset.refresh": "fetched the examples again",
  "training.repeat": "changed how often it keeps learning",
  "training.create": "started teaching",
  "training.cancel": "stopped teaching",
  "training.resume": "resumed teaching",
  "training.delete": "deleted the training run",
  "training.succeeded": "finished teaching",
  "training.failed": "failed to teach",
  "worker.create": "added the worker",
  "worker.delete": "removed the worker",
  "user.code_failed": "entered a wrong code for",
  "user.two_factor_on": "turned on two-step verification",
  "user.two_factor_off": "turned off two-step verification",
  "user.two_factor_reset": "reset two-step verification for",
  "user.sessions_revoked": "signed out other devices",
};

const when = new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" });

function AuditLog() {
  const { data, hasNextPage, fetchNextPage, isFetchingNextPage } = useSuspenseInfiniteQuery(auditLog);
  const events = data.pages.flatMap((p) => p.events);

  if (events.length === 0) return <Text c="dimmed">Nothing has happened yet.</Text>;
  return (
    <Stack>
      <Table.ScrollContainer minWidth={560}>
        <Table verticalSpacing="sm">
          <Table.Thead><Table.Tr><Table.Th>When</Table.Th><Table.Th>Who</Table.Th><Table.Th>What</Table.Th></Table.Tr></Table.Thead>
          <Table.Tbody>
            {events.map((e) => (
              <Table.Tr key={e.id}>
                <Table.Td className="whitespace-nowrap">{e.time ? when.format(timestampDate(e.time)) : ""}</Table.Td>
                <Table.Td>{e.actor}</Table.Td>
                <Table.Td>
                  {actions[e.action] ?? e.action} {e.target !== e.actor && <Text span fw={600}>{e.target}</Text>}
                  {e.detail && <Text span c="dimmed"> ({e.detail})</Text>}
                </Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      </Table.ScrollContainer>
      {hasNextPage && <Group><Button variant="default" loading={isFetchingNextPage} onClick={() => fetchNextPage()}>Show older</Button></Group>}
    </Stack>
  );
}
