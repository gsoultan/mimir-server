import { timestampDate } from "@bufbuild/protobuf/wkt";
import { useMutation } from "@connectrpc/connect-query";
import { Alert, Badge, Button, Card, Group, NumberInput, PasswordInput, Radio, SimpleGrid, Stack, Switch, Table, Tabs, TagsInput, Text, TextInput, Title } from "@mantine/core";
import { useMediaQuery } from "@mantine/hooks";
import { notifications } from "@mantine/notifications";
import { IconAlertCircle, IconDownload, IconUsers } from "@tabler/icons-react";
import { useQueryClient, useSuspenseInfiniteQuery, useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import { z } from "zod";

import { message } from "../../api/client";
import { SettingsService, TwoFactorPolicy } from "../../api/gen/mimir/v1/settings_pb";
import { allSettingsQueries, auditLog, databaseInfo, listTeams, settings, signInProviders, whoAmI } from "../../api/queries";
import { AnchorLink, ButtonLink } from "../../components/links";
import { Page } from "../../components/Page";
import { SignInProviders } from "../../components/SignInProviders";
import { Teams } from "../../components/Teams";
import { Limits, Usage } from "../../components/Usage";
import { Workers } from "../../components/Workers";
import { ago, bytes } from "../../lib/format";
import { t } from "../../i18n";

export const Route = createFileRoute("/_app/settings")({
  validateSearch: z.object({ tab: z.enum(["audit", "teams", "database", "alerts", "retention", "security", "signin", "workers", "limits", "usage"]).optional() }),
  loader: async ({ context }) => {
    const me = await context.queryClient.ensureQueryData(whoAmI);
    // Admins run their team; the server-wide tabs are the owners'.
    const owner = me.permissions.includes("settings.manage");
    return Promise.all([
      context.queryClient.ensureInfiniteQueryData(auditLog),
      owner ? context.queryClient.ensureQueryData(settings) : null,
      owner ? context.queryClient.ensureQueryData(databaseInfo) : null,
      owner ? context.queryClient.ensureQueryData(listTeams) : null,
      owner ? context.queryClient.ensureQueryData(signInProviders) : null,
    ]);
  },
  component: Settings,
});

function Settings() {
  const { tab } = Route.useSearch();
  const navigate = Route.useNavigate();
  const { data: me } = useSuspenseQuery(whoAmI);
  const owner = me.permissions.includes("settings.manage");
  const wide = useMediaQuery("(min-width: 62em)", true, { getInitialValueInEffect: false });
  const shown = owner || tab === "audit" || tab === "usage" ? tab ?? "audit" : "audit";
  return (
    <Page title={t("Settings")}
      actions={me.permissions.includes("users.read") && <ButtonLink variant="default" leftSection={<IconUsers size={18} />} to="/users">{t("People")}</ButtonLink>}
      intro={owner ? t("How this server stores data and keeps you informed.") : t("What's happening in {n1}.", { n1: me.user?.teamName ?? t("your team") })}>
      <Tabs value={shown} onChange={(t) => navigate({ search: { tab: (t ?? "audit") as "audit" }, replace: true })}
        orientation={wide ? "vertical" : "horizontal"} variant={wide ? "pills" : "default"} classNames={wide ? { list: "settings-list", tab: "side-link", panel: "settings-panel" } : undefined}>
        <Tabs.List>
          <Tabs.Tab value="audit">{t("Audit log")}</Tabs.Tab>
          {owner && <>
            <Tabs.Tab value="teams">{t("Teams")}</Tabs.Tab>
            <Tabs.Tab value="database">{t("Database")}</Tabs.Tab>
            <Tabs.Tab value="alerts">{t("Alerts")}</Tabs.Tab>
            <Tabs.Tab value="retention">{t("Log retention")}</Tabs.Tab>
            <Tabs.Tab value="security">{t("Security")}</Tabs.Tab>
            <Tabs.Tab value="signin">{t("Sign-in")}</Tabs.Tab>
            <Tabs.Tab value="workers">{t("Workers")}</Tabs.Tab>
            <Tabs.Tab value="limits">{t("Limits")}</Tabs.Tab>
          </>}
          <Tabs.Tab value="usage">{t("Usage")}</Tabs.Tab>
        </Tabs.List>
        <Tabs.Panel value="audit" pt={wide ? 0 : "md"}><AuditLog showTeam={owner} /></Tabs.Panel>
        {owner && <>
          <Tabs.Panel value="teams" pt={wide ? 0 : "md"}><Teams /></Tabs.Panel>
          <Tabs.Panel value="database" pt={wide ? 0 : "md"}><Database /></Tabs.Panel>
          <Tabs.Panel value="alerts" pt={wide ? 0 : "md"}><Alerts /></Tabs.Panel>
          <Tabs.Panel value="retention" pt={wide ? 0 : "md"}><Retention /></Tabs.Panel>
          <Tabs.Panel value="security" pt={wide ? 0 : "md"}><SecurityPolicy /></Tabs.Panel>
          <Tabs.Panel value="signin" pt={wide ? 0 : "md"}><SignInProviders /></Tabs.Panel>
          <Tabs.Panel value="workers" pt={wide ? 0 : "md"}><Workers /></Tabs.Panel>
          <Tabs.Panel value="limits" pt={wide ? 0 : "md"}><Limits /></Tabs.Panel>
        </>}
        <Tabs.Panel value="usage" pt={wide ? 0 : "md"}><Usage /></Tabs.Panel>
      </Tabs>
    </Page>
  );
}

function Database() {
  const { data } = useSuspenseQuery(databaseInfo);
  const upToDate = data.schemaVersion === data.latestSchemaVersion;
  const facts: [string, string][] = [
    [t("Engine"), "PostgreSQL"],
    [t("Server"), data.path],
    ...(data.logPath ? [[t("Audit log"), data.logPath] as [string, string]] : []),
    [t("Size"), bytes(data.sizeBytes)],
    [t("Models"), String(data.models)],
    [t("People"), String(data.users)],
    [t("Audit events"), String(data.auditEvents)],
    [t("Webhook deliveries"), String(data.deliveries)],
    [t("Last cleanup"), data.lastTrimTime ? t("{lastTrimTime}, {lastTrimRows} old rows removed", { lastTrimTime: ago(data.lastTrimTime), lastTrimRows: data.lastTrimRows }) : t("Not yet; it runs every hour")],
  ];
  return (
    <SimpleGrid cols={{ base: 1, md: 2 }}>
      <Card withBorder radius="lg" padding="lg">
        <Group justify="space-between" mb="sm">
          <Title order={2} fz="lg">{t("This server's database")}</Title>
          <Badge variant="light" color={upToDate ? "teal" : "yellow"}>{upToDate ? t("Up to date (v{schemaVersion})", { schemaVersion: data.schemaVersion }) : t("Update pending (v{schemaVersion} → v{latestSchemaVersion})", { schemaVersion: data.schemaVersion, latestSchemaVersion: data.latestSchemaVersion })}</Badge>
        </Group>
        <Table verticalSpacing="xs">
          <Table.Tbody>
            {facts.map(([k, v]) => <Table.Tr key={k}><Table.Td c="dimmed" w={160}>{k}</Table.Td><Table.Td className="break-all">{v}</Table.Td></Table.Tr>)}
          </Table.Tbody>
        </Table>
        <Text size="sm" c="dimmed" mt="sm">{t("Database updates run on their own each time the server starts, so there's nothing to click here.")}</Text>
      </Card>
      <Stack>
        <Card withBorder radius="lg" padding="lg">
          <Title order={2} fz="lg">{t("Back it up")}</Title>
          <Text size="sm" c="dimmed" mb="md">{t("Download a copy of the database, made with pg_dump in its custom format: people, keys, settings and model details. Restore it with pg_restore. The server needs pg_dump installed for this; without it, run pg_dump yourself. Model files live in the data folder and aren't included")}{data.logPath ? t(", and neither is the separate audit log database; back that up with pg_dump") : ""}.</Text>
          <Button component="a" href="/api/backup" download leftSection={<IconDownload size={18} />}>{t("Download a backup")}</Button>
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
    onSuccess: async () => { await queryClient.invalidateQueries({ queryKey: allSettingsQueries }); notifications.show({ color: "teal", message: t("Alert settings saved.") }); },
  });
  const test = useMutation(SettingsService.method.sendTestAlert, {
    onSuccess: () => notifications.show({ color: "teal", message: t("Test alert sent. Check your inbox or chat.") }),
    onError: (err) => notifications.show({ color: "red", message: message(err), autoClose: 8000 }),
  });
  return (
    <form onSubmit={(e) => { e.preventDefault(); save.mutate({ email, telegram }); }}>
      <Stack>
        <Text c="dimmed">{t("You get an alert when a webhook keeps failing. More alerts come with training.")}</Text>
        <SimpleGrid cols={{ base: 1, md: 2 }}>
          <Card withBorder radius="lg" padding="lg">
            <Group justify="space-between" mb="sm">
              <Title order={2} fz="lg">{t("Email")}</Title>
              <Switch label={t("On")} checked={email.enabled} onChange={(e) => setEmail({ ...email, enabled: e.currentTarget.checked })} />
            </Group>
            <Stack gap="sm">
              <Group grow align="flex-start">
                <TextInput label={t("Mail server")} placeholder="smtp.example.com" value={email.host} onChange={(e) => setEmail({ ...email, host: e.currentTarget.value })} />
                <NumberInput label={t("Port")} min={1} max={65535} value={email.port} onChange={(v) => setEmail({ ...email, port: Number(v) || 587 })} />
              </Group>
              <TextInput label={t("Username")} autoComplete="off" value={email.username} onChange={(e) => setEmail({ ...email, username: e.currentTarget.value })} />
              <PasswordInput label={t("Password")} autoComplete="new-password" placeholder={data.email?.passwordSet ? t("Saved; leave empty to keep it") : ""} value={email.password} onChange={(e) => setEmail({ ...email, password: e.currentTarget.value })} />
              <TextInput label={t("From")} type="email" placeholder="mimir@example.com" value={email.from} onChange={(e) => setEmail({ ...email, from: e.currentTarget.value })} />
              <TagsInput label={t("Send to")} description={t("Press Enter after each address.")} placeholder="you@example.com" value={email.to} onChange={(to) => setEmail({ ...email, to })} />
              <Button variant="default" className="self-start" loading={test.isPending && test.variables?.channel === "email"} onClick={() => test.mutate({ channel: "email" })} disabled={!data.email?.enabled}>{t("Send a test email")}</Button>
            </Stack>
          </Card>
          <Card withBorder radius="lg" padding="lg">
            <Group justify="space-between" mb="sm">
              <Title order={2} fz="lg">{t("Telegram")}</Title>
              <Switch label={t("On")} checked={telegram.enabled} onChange={(e) => setTelegram({ ...telegram, enabled: e.currentTarget.checked })} />
            </Group>
            <Stack gap="sm">
              <Text size="sm" c="dimmed">{t("Make a bot with @BotFather, start a chat with it, then put its token and your chat id here.")}</Text>
              <PasswordInput label={t("Bot token")} autoComplete="off" placeholder={data.telegram?.tokenSet ? t("Saved; leave empty to keep it") : "123456:ABC..."} value={telegram.botToken} onChange={(e) => setTelegram({ ...telegram, botToken: e.currentTarget.value })} />
              <TextInput label={t("Chat id")} placeholder="e.g. 123456789" value={telegram.chatId} onChange={(e) => setTelegram({ ...telegram, chatId: e.currentTarget.value })} />
              <Button variant="default" className="self-start" loading={test.isPending && test.variables?.channel === "telegram"} onClick={() => test.mutate({ channel: "telegram" })} disabled={!data.telegram?.enabled}>{t("Send a test message")}</Button>
            </Stack>
          </Card>
        </SimpleGrid>
        {save.error && <Alert color="red" icon={<IconAlertCircle />}>{message(save.error)}</Alert>}
        <Group><Button type="submit" loading={save.isPending}>{t("Save alert settings")}</Button><Text size="sm" c="dimmed">{t("Save first, then send a test.")}</Text></Group>
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
    onSuccess: async () => { await queryClient.invalidateQueries({ queryKey: allSettingsQueries }); notifications.show({ color: "teal", message: t("Retention saved.") }); },
  });
  return (
    <Card withBorder radius="lg" padding="lg" className="max-w-xl">
      <form onSubmit={(e) => { e.preventDefault(); save.mutate({ retention: { auditDays: audit, deliveryDays: delivery } }); }}>
        <Stack>
          <Text c="dimmed">{t("Old history is removed once an hour. Shorter keeps the database small; longer keeps more to look back on.")}</Text>
          <NumberInput label={t("Keep audit events for")} suffix=" days" description={t("0 keeps them forever.")} min={0} max={3650} value={audit} onChange={(v) => setAudit(Number(v) || 0)} />
          <NumberInput label={t("Keep webhook delivery history for")} suffix=" days" min={1} max={3650} value={delivery} onChange={(v) => setDelivery(Number(v) || 1)} />
          {save.error && <Alert color="red" icon={<IconAlertCircle />}>{message(save.error)}</Alert>}
          <Button type="submit" loading={save.isPending} className="self-start">{t("Save")}</Button>
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
    onSuccess: async () => { await queryClient.invalidateQueries({ queryKey: allSettingsQueries }); notifications.show({ color: "teal", message: t("Security settings saved.") }); },
  });
  return (
    <Card withBorder radius="lg" padding="lg" className="max-w-xl">
      <form onSubmit={(e) => { e.preventDefault(); save.mutate({ security: { twoFactor: Number(policy) as TwoFactorPolicy } }); }}>
        <Stack>
          <Radio.Group label={t("Who must use two-step verification")} description={t("People without it set up can only do that until it's on. API keys keep working.")} value={policy} onChange={setPolicy}>
            <Stack gap="xs" mt="xs">
              <Radio value={String(TwoFactorPolicy.OPTIONAL)} label={t("Nobody; each person decides")} />
              <Radio value={String(TwoFactorPolicy.ADMINS)} label={t("Admins and owners")} />
              <Radio value={String(TwoFactorPolicy.EVERYONE)} label={t("Everyone")} />
            </Stack>
          </Radio.Group>
          {!me.user?.twoFactor && policy !== String(TwoFactorPolicy.OPTIONAL) && (
            <Alert color="yellow" icon={<IconAlertCircle />}>{t("Turn it on for your own account first, under")}{" "}<AnchorLink to="/security">{t("Security")}</AnchorLink>.</Alert>
          )}
          {save.error && <Alert color="red" icon={<IconAlertCircle />}>{message(save.error)}</Alert>}
          <Button type="submit" loading={save.isPending} className="self-start">{t("Save")}</Button>
        </Stack>
      </form>
    </Card>
  );
}

// Plain words for each audit action.
const actions: Record<string, string> = {
  "server.setup": t("set up the server"),
  "user.signin": t("signed in"),
  "user.signin_failed": t("failed to sign in"),
  "user.signin_linked": t("tied their account to"),
  "settings.signin_add": t("added the way to sign in"),
  "settings.signin_change": t("changed the way to sign in"),
  "settings.signin_remove": t("removed the way to sign in"),
  "user.password_changed": t("changed their password"),
  "user.password_reset": t("reset the password of"),
  "user.create": "added",
  "user.update": "changed",
  "user.delete": "removed",
  "apikey.create": t("made the API key"),
  "apikey.revoke": t("revoked an API key"),
  "model.create": t("added the model"),
  "model.update": t("changed the model"),
  "model.delete": t("deleted the model"),
  "webhook.create": t("added the webhook"),
  "webhook.update": t("changed the webhook"),
  "webhook.delete": t("removed the webhook"),
  "settings.alerts": t("changed alert settings"),
  "settings.retention": t("changed log retention"),
  "settings.security": t("changed security settings"),
  "dataset.create": t("added the examples"),
  "dataset.delete": t("removed the examples"),
  "dataset.refresh": t("fetched the examples again"),
  "settings.limits": t("changed the limits"),
  "team.create": t("added the team"),
  "team.rename": t("renamed the team"),
  "team.delete": t("removed the team"),
  "user.move": t("moved to another team"),
  "training.repeat": t("changed how often it keeps learning"),
  "training.create": t("started teaching"),
  "training.cancel": t("stopped teaching"),
  "training.resume": t("resumed teaching"),
  "training.delete": t("deleted the training run"),
  "training.succeeded": t("finished teaching"),
  "training.failed": t("failed to teach"),
  "scoring.created": t("started scoring"),
  "scoring.run": t("scored again"),
  "scoring.cancelled": t("stopped scoring"),
  "scoring.repeat": t("changed how often it scores"),
  "scoring.deleted": t("deleted the scoring"),
  "scoring.finished": t("finished scoring"),
  "scoring.failed": t("failed to score"),
  "collection.create": t("made the collection"),
  "collection.update": t("changed the collection"),
  "collection.delete": t("deleted the collection"),
  "document.add": t("added the document"),
  "document.delete": t("deleted the document"),
  "document.failed": t("couldn't read the document"),
  "worker.create": t("added the worker"),
  "worker.delete": t("removed the worker"),
  "worker.join": t("first connected as"),
  "worker.refused": t("was refused as"),
  "worker.address": t("connected from a new address as"),
  "worker.token": t("made a new token for the worker"),
  "model.worker": t("moved the model"),
  "user.code_failed": t("entered a wrong code for"),
  "user.two_factor_on": "turned on two-step verification",
  "user.two_factor_off": "turned off two-step verification",
  "user.two_factor_reset": "reset two-step verification for",
  "user.sessions_revoked": t("signed out other devices"),
};

const when = new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" });

function AuditLog({ showTeam }: { showTeam: boolean }) {
  const { data, hasNextPage, fetchNextPage, isFetchingNextPage } = useSuspenseInfiniteQuery(auditLog);
  const events = data.pages.flatMap((p) => p.events);

  if (events.length === 0) return <Text c="dimmed">{t("Nothing has happened yet.")}</Text>;
  return (
    <Stack>
      <Table.ScrollContainer minWidth={560}>
        <Table verticalSpacing="sm">
          <Table.Thead><Table.Tr><Table.Th>{t("When")}</Table.Th>{showTeam && <Table.Th>{t("Team")}</Table.Th>}<Table.Th>{t("Who")}</Table.Th><Table.Th>{t("What")}</Table.Th></Table.Tr></Table.Thead>
          <Table.Tbody>
            {events.map((e) => (
              <Table.Tr key={e.id}>
                <Table.Td className="whitespace-nowrap">{e.time ? when.format(timestampDate(e.time)) : ""}</Table.Td>
                {showTeam && <Table.Td>{e.teamName || <Text span c="dimmed">{t("None")}</Text>}</Table.Td>}
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
      {hasNextPage && <Group><Button variant="default" loading={isFetchingNextPage} onClick={() => fetchNextPage()}>{t("Show older")}</Button></Group>}
    </Stack>
  );
}
