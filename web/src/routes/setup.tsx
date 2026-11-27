import { Code as ErrorCode, ConnectError, createClient } from "@connectrpc/connect";
import { useMutation, useQuery } from "@connectrpc/connect-query";
import { ActionIcon, Alert, Badge, Button, Checkbox, Code, CopyButton, Divider, Grid, Group, NumberInput, PasswordInput, Progress, SegmentedControl, Select, SimpleGrid, Stack, Text, TextInput, Tooltip } from "@mantine/core";
import { IconAlertCircle, IconCheck, IconCircleCheck, IconCopy, IconKey, IconRefresh } from "@tabler/icons-react";
import { createFileRoute, redirect, useNavigate } from "@tanstack/react-router";
import { useEffect, useState, type ReactNode } from "react";

import { message, transport } from "../api/client";
import { AuthService, SecretKeySource } from "../api/gen/mimir/v1/access_pb";
import { setupState } from "../api/queries";
import { AuthCard } from "../components/AuthCard";
import { defaultPostgres, newSecretKey, type Postgres, postgresReady, postgresUrl } from "../lib/postgres";
import { t } from "../i18n";

export const Route = createFileRoute("/setup")({
  beforeLoad: async ({ context }) => {
    const { needsSetup } = await context.queryClient.fetchQuery(setupState);
    if (!needsSetup) throw redirect({ to: "/sign-in" });
  },
  component: Setup,
});

// Setup walks through first-run setup. A server started without a database
// asks for it, a separate log database, the secret key, how long history
// is kept and the first owner; one that already has its database asks for
// the last two only. Every step needs the one-time token from the log.
function Setup() {
  const navigate = useNavigate();
  const { queryClient } = Route.useRouteContext();
  const { data: state } = useQuery(AuthService.method.getSetupState, {});
  const needsConfig = state?.needsConfig ?? false;

  const [step, setStep] = useState(0);
  const [setupToken, setSetupToken] = useState("");
  const [db, setDb] = useState<Postgres>(defaultPostgres);
  const [separateLog, setSeparateLog] = useState(false);
  const [logDb, setLogDb] = useState<Postgres | null>(null);
  const [keyMode, setKeyMode] = useState<"new" | "mine">("new");
  const [newKey, setNewKey] = useState(newSecretKey);
  const [keySaved, setKeySaved] = useState(false);
  const [secretKey, setSecretKey] = useState("");
  const [auditDays, setAuditDays] = useState<number | null>(null);
  const [deliveryDays, setDeliveryDays] = useState<number | null>(null);
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [finished, setFinished] = useState(false);

  // The log database starts as a copy of the main one's server and role.
  const log = logDb ?? { ...db, database: `${db.database}_log` };
  const audit = auditDays ?? state?.retention?.auditDays ?? 365;
  const delivery = deliveryDays ?? state?.retention?.deliveryDays ?? 30;

  const checkMain = useMutation(AuthService.method.checkDatabase);
  const checkLog = useMutation(AuthService.method.checkDatabase);
  const hasAccounts = checkMain.data?.hasAccounts ?? false;
  const setup = useMutation(AuthService.method.setup, {
    onSuccess: async (res) => {
      if (res.signInNext) { setFinished(true); return; }
      queryClient.clear();
      await navigate({ to: "/" });
    },
  });

  if (finished) return <Finished hasAccounts={hasAccounts} />;

  type Step = { label: string; body: ReactNode; ready: boolean };
  const steps: Step[] = [{
    label: t("Token"),
    ready: setupToken.trim() !== "",
    body: (
      <TextInput label={t("Setup token")} description={t("The server prints it in its log when it starts without a configuration or accounts (or it's $MIMIR_SETUP_TOKEN). Anyone who can reach this page could otherwise claim the server.")} required autoComplete="off" spellCheck={false} value={setupToken} onChange={(e) => setSetupToken(e.currentTarget.value)} data-autofocus />
    ),
  }];
  if (needsConfig) {
    steps.push({
      label: t("Database"),
      ready: checkMain.isSuccess && (!separateLog || checkLog.isSuccess),
      body: (
        <Stack>
          <Text size="sm" c="dimmed">{t("mimir keeps people, settings and model details in PostgreSQL. Make an empty database and a user that may create tables in it, then fill in where it is. Passwords are saved encrypted, in a file only the server's user can read.")}</Text>
          <DatabaseFields value={db} onChange={(v) => { setDb(v); checkMain.reset(); }} check={() => checkMain.mutate({ setupToken, databaseUrl: postgresUrl(db) })} pending={checkMain.isPending} error={checkMain.error} ok={checkMain.data && t("Connected to {version}{n2}", { version: checkMain.data.version, n2: checkMain.data.hasAccounts ? t(". It already has mimir accounts, which are kept.") : "." })} />
          <Divider my={4} />
          <div>
            <Text fw={600}>{t("Audit log database")}</Text>
            <Text size="sm" c="dimmed" mb="xs">{t("The audit log records who signed in and what they changed. Keep it in the database above, or in one of its own: one that people who run the main database can't change, or on cheaper storage.")}</Text>
            <SegmentedControl fullWidth value={separateLog ? "separate" : "same"} onChange={(v) => { setSeparateLog(v === "separate"); checkLog.reset(); }} data={[{ value: "same", label: t("Same database") }, { value: "separate", label: t("A separate database") }]} />
          </div>
          {separateLog && (
            <DatabaseFields value={log} onChange={(v) => { setLogDb(v); checkLog.reset(); }} check={() => checkLog.mutate({ setupToken, databaseUrl: postgresUrl(log) })} pending={checkLog.isPending} error={checkLog.error} ok={checkLog.data && t("Connected to {version}.", { version: checkLog.data.version })} />
          )}
        </Stack>
      ),
    });
    steps.push({
      label: t("Secret key"),
      ready: state?.secretKey !== SecretKeySource.NONE || (keyMode === "new" ? keySaved : secretKey.trim() !== ""),
      body: <KeyStep source={state?.secretKey ?? SecretKeySource.UNSPECIFIED} mode={keyMode} setMode={setKeyMode} newKey={newKey} regenerate={() => { setNewKey(newSecretKey()); setKeySaved(false); }} saved={keySaved} setSaved={setKeySaved} value={secretKey} setValue={setSecretKey} />,
    });
  }
  steps.push({
    label: t("History"),
    ready: audit >= 0 && audit <= 3650 && delivery >= 1 && delivery <= 3650,
    body: (
      <Stack>
        <Text size="sm" c="dimmed">{t("Old history is trimmed every hour. You can change this later under Settings.")}</Text>
        <NumberInput label={t("Keep audit events for (days)")} description={t("Who signed in, changed settings, made keys and models. 0 keeps them forever.")} min={0} max={3650} value={audit} onChange={(v) => setAuditDays(typeof v === "number" ? v : null)} />
        <NumberInput label={t("Keep webhook delivery history for (days)")} min={1} max={3650} value={delivery} onChange={(v) => setDeliveryDays(typeof v === "number" ? v : null)} />
      </Stack>
    ),
  });
  if (!hasAccounts) {
    steps.push({
      label: t("Owner"),
      ready: name.trim() !== "" && email.trim() !== "" && password.length >= 10,
      body: (
        <Stack>
          <Text size="sm" c="dimmed">{t("You'll be the server's owner: you can do everything, including adding people.")}</Text>
          <TextInput label={t("Your name")} required maxLength={100} value={name} onChange={(e) => setName(e.currentTarget.value)} />
          <TextInput label={t("Email")} type="email" autoComplete="username" required value={email} onChange={(e) => setEmail(e.currentTarget.value)} />
          <PasswordInput label={t("Password")} description={t("At least 10 characters.")} autoComplete="new-password" required minLength={10} value={password} onChange={(e) => setPassword(e.currentTarget.value)} />
        </Stack>
      ),
    });
  }

  const last = step === steps.length - 1;
  const current = steps[Math.min(step, steps.length - 1)]!;
  const finish = () => setup.mutate({
    setupToken, name, email, password,
    retention: { auditDays: audit, deliveryDays: delivery },
    ...(needsConfig && {
      databaseUrl: postgresUrl(db),
      logDatabaseUrl: separateLog ? postgresUrl(log) : "",
      secretKey: state?.secretKey !== SecretKeySource.NONE ? "" : keyMode === "mine" ? secretKey : newKey,
    }),
  });

  return (
    <AuthCard wide title={t("Welcome to mimir")} intro={needsConfig ? t("This server isn't set up yet. A few steps and it's yours.") : t("This server has no accounts yet. Make yours; you'll be its owner.")}>
      <form onSubmit={(e) => { e.preventDefault(); if (!current.ready) return; if (last) finish(); else setStep(step + 1); }}>
        <Stack>
          <div>
            <Text size="sm" c="dimmed">{t("Step")}{" "}{step + 1}{" "}{t("of")}{" "}{steps.length}: <Text span fw={600} c="var(--mantine-color-text)">{current.label}</Text></Text>
            <Progress mt={6} size="sm" value={((step + 1) / steps.length) * 100} />
          </div>
          {current.body}
          {setup.error && <Alert color="red" icon={<IconAlertCircle />} style={{ overflowWrap: "anywhere" }}>{message(setup.error)}</Alert>}
          <Group justify="space-between">
            <Button variant="default" disabled={step === 0} onClick={() => setStep(step - 1)}>{t("Back")}</Button>
            <Button type="submit" disabled={!current.ready} loading={setup.isPending}>{last ? (needsConfig ? t("Finish setup") : t("Create my account")) : t("Next")}</Button>
          </Group>
        </Stack>
      </form>
    </AuthCard>
  );
}

function DatabaseFields({ value, onChange, check, pending, error, ok }: {
  value: Postgres; onChange: (v: Postgres) => void; check: () => void; pending: boolean; error: unknown; ok?: string | false;
}) {
  const set = <K extends keyof Postgres>(k: K, v: Postgres[K]) => onChange({ ...value, [k]: v });
  return (
    <Stack gap="sm">
      <Grid gap="sm">
        <Grid.Col span={{ base: 12, xs: 8 }}><TextInput label={t("Host")} placeholder="localhost" required autoComplete="off" spellCheck={false} value={value.host} onChange={(e) => set("host", e.currentTarget.value)} /></Grid.Col>
        <Grid.Col span={{ base: 12, xs: 4 }}><NumberInput label={t("Port")} required min={1} max={65535} hideControls value={value.port} onChange={(v) => set("port", typeof v === "number" ? v : 0)} /></Grid.Col>
      </Grid>
      <SimpleGrid cols={{ base: 1, xs: 2 }} spacing="sm">
        <TextInput label={t("Username")} required autoComplete="off" spellCheck={false} value={value.user} onChange={(e) => set("user", e.currentTarget.value)} />
        <PasswordInput label={t("Password")} autoComplete="new-password" value={value.password} onChange={(e) => set("password", e.currentTarget.value)} />
      </SimpleGrid>
      <SimpleGrid cols={{ base: 1, xs: 2 }} spacing="sm">
        <TextInput label={t("Database name")} required autoComplete="off" spellCheck={false} value={value.database} onChange={(e) => set("database", e.currentTarget.value)} />
        <Select label={t("Encryption (SSL)")} allowDeselect={false} value={value.ssl} onChange={(v) => v && set("ssl", v as Postgres["ssl"])} data={[
          { value: "prefer", label: t("Use it if the server has it") },
          { value: "require", label: t("Always (a server elsewhere)") },
          { value: "disable", label: t("Off (same machine)") },
        ]} />
      </SimpleGrid>
      <Group>
        <Button variant="light" onClick={check} loading={pending} disabled={!postgresReady(value)}>{t("Test connection")}</Button>
      </Group>
      {error ? <Alert color="red" icon={<IconAlertCircle />} py={6} style={{ overflowWrap: "anywhere" }}>{message(error)}</Alert> : null}
      {ok ? <Text size="sm" c="teal"><IconCircleCheck size={16} className="mr-1 inline align-[-3px]" />{ok}</Text> : null}
    </Stack>
  );
}

function KeyStep({ source, mode, setMode, newKey, regenerate, saved, setSaved, value, setValue }: {
  source: SecretKeySource; mode: "new" | "mine"; setMode: (m: "new" | "mine") => void;
  newKey: string; regenerate: () => void; saved: boolean; setSaved: (b: boolean) => void;
  value: string; setValue: (v: string) => void;
}) {
  const intro = <Text size="sm" c="dimmed">{t("The secret key encrypts the secrets the server stores for you: webhook signing secrets, database connection strings for training, alert passwords, and the database password in its configuration file. A copy of the database gives none of them away without it, and the server can't read them without it either.")}</Text>;
  if (source === SecretKeySource.ENVIRONMENT || source === SecretKeySource.FILE) {
    return (
      <Stack>
        {intro}
        <Alert color="teal" icon={<IconKey />}>
          {source === SecretKeySource.ENVIRONMENT ? t("The server already has its key from $MIMIR_SECRET_KEY or -secret-key-file.") : t("The server already has a key file in its data folder, and keeps using it.")}
        </Alert>
      </Stack>
    );
  }
  return (
    <Stack>
      {intro}
      <SegmentedControl value={mode} onChange={(v) => setMode(v as "new" | "mine")} data={[{ value: "new", label: t("Generate a new key") }, { value: "mine", label: t("Use a key I have") }]} />
      {mode === "new" ? (
        <Stack gap="sm">
          <div>
            <Text size="sm" fw={500} mb={4}>{t("Your new key")}</Text>
            <Group gap="xs" wrap="nowrap">
              <Code block className="flex-1" style={{ overflowWrap: "anywhere", whiteSpace: "normal" }}>{newKey}</Code>
              <CopyButton value={newKey}>
                {({ copied, copy }) => (
                  <Tooltip label={copied ? t("Copied") : t("Copy")}>
                    <ActionIcon variant="light" size="lg" onClick={copy} aria-label={t("Copy the key")}>{copied ? <IconCheck size={18} /> : <IconCopy size={18} />}</ActionIcon>
                  </Tooltip>
                )}
              </CopyButton>
              <Tooltip label={t("Generate another")}>
                <ActionIcon variant="default" size="lg" onClick={regenerate} aria-label={t("Generate another key")}><IconRefresh size={18} /></ActionIcon>
              </Tooltip>
            </Group>
          </div>
          <Text size="sm">{t("The server saves it as secret.key in its data folder.")}{" "}<b>{t("Keep a copy somewhere safe, such as a password manager")}</b>{t(": a restored database backup can't be read without it.")}</Text>
          <Checkbox label={t("I've saved a copy of this key")} checked={saved} onChange={(e) => setSaved(e.currentTarget.checked)} />
        </Stack>
      ) : (
        <PasswordInput label={t("Secret key")} description={t("The key a backup of this database was made with: 32 bytes as base64 or hex.")} required autoComplete="off" value={value} onChange={(e) => setValue(e.currentTarget.value)} />
      )}
    </Stack>
  );
}

// Finished waits for the full server, which starts once setup is written,
// before sending the owner to sign in.
function Finished({ hasAccounts }: { hasAccounts: boolean }) {
  const navigate = useNavigate();
  const { queryClient } = Route.useRouteContext();
  const [ready, setReady] = useState(false);
  useEffect(() => {
    const client = createClient(AuthService, transport);
    let stop = false;
    const poll = async () => {
      while (!stop) {
        try {
          await client.whoAmI({});
          break;
        } catch (err) {
          if (ConnectError.from(err).code !== ErrorCode.Unavailable) break;
        }
        await new Promise((r) => setTimeout(r, 1000));
      }
      if (!stop) setReady(true);
    };
    void poll();
    return () => { stop = true; };
  }, []);
  return (
    <AuthCard title={t("All set")} intro={hasAccounts ? t("The server is configured and keeps the accounts its database already had.") : t("The server is configured and your owner account is ready.")}>
      <Stack>
        <Group gap="xs">
          <Badge color={ready ? "teal" : "gray"} variant="light">{ready ? t("Server ready") : t("Server starting…")}</Badge>
        </Group>
        <Text size="sm" c="dimmed">{t("Setup is now closed: this page won't open again. Back up the data folder's secret.key with the database.")}</Text>
        <Button fullWidth disabled={!ready} onClick={() => { queryClient.clear(); void navigate({ to: "/sign-in" }); }}>{t("Sign in")}</Button>
      </Stack>
    </AuthCard>
  );
}
