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
    label: "Token",
    ready: setupToken.trim() !== "",
    body: (
      <TextInput label="Setup token" description="The server prints it in its log when it starts without a configuration or accounts (or it's $MIMIR_SETUP_TOKEN). Anyone who can reach this page could otherwise claim the server." required autoComplete="off" spellCheck={false} value={setupToken} onChange={(e) => setSetupToken(e.currentTarget.value)} data-autofocus />
    ),
  }];
  if (needsConfig) {
    steps.push({
      label: "Database",
      ready: checkMain.isSuccess && (!separateLog || checkLog.isSuccess),
      body: (
        <Stack>
          <Text size="sm" c="dimmed">mimir keeps people, settings and model details in PostgreSQL. Make an empty database and a user that may create tables in it, then fill in where it is. Passwords are saved encrypted, in a file only the server's user can read.</Text>
          <DatabaseFields value={db} onChange={(v) => { setDb(v); checkMain.reset(); }} check={() => checkMain.mutate({ setupToken, databaseUrl: postgresUrl(db) })} pending={checkMain.isPending} error={checkMain.error} ok={checkMain.data && `Connected to ${checkMain.data.version}${checkMain.data.hasAccounts ? ". It already has mimir accounts, which are kept." : "."}`} />
          <Divider my={4} />
          <div>
            <Text fw={600}>Audit log database</Text>
            <Text size="sm" c="dimmed" mb="xs">The audit log records who signed in and what they changed. Keep it in the database above, or in one of its own: one that people who run the main database can't change, or on cheaper storage.</Text>
            <SegmentedControl fullWidth value={separateLog ? "separate" : "same"} onChange={(v) => { setSeparateLog(v === "separate"); checkLog.reset(); }} data={[{ value: "same", label: "Same database" }, { value: "separate", label: "A separate database" }]} />
          </div>
          {separateLog && (
            <DatabaseFields value={log} onChange={(v) => { setLogDb(v); checkLog.reset(); }} check={() => checkLog.mutate({ setupToken, databaseUrl: postgresUrl(log) })} pending={checkLog.isPending} error={checkLog.error} ok={checkLog.data && `Connected to ${checkLog.data.version}.`} />
          )}
        </Stack>
      ),
    });
    steps.push({
      label: "Secret key",
      ready: state?.secretKey !== SecretKeySource.NONE || (keyMode === "new" ? keySaved : secretKey.trim() !== ""),
      body: <KeyStep source={state?.secretKey ?? SecretKeySource.UNSPECIFIED} mode={keyMode} setMode={setKeyMode} newKey={newKey} regenerate={() => { setNewKey(newSecretKey()); setKeySaved(false); }} saved={keySaved} setSaved={setKeySaved} value={secretKey} setValue={setSecretKey} />,
    });
  }
  steps.push({
    label: "History",
    ready: audit >= 0 && audit <= 3650 && delivery >= 1 && delivery <= 3650,
    body: (
      <Stack>
        <Text size="sm" c="dimmed">Old history is trimmed every hour. You can change this later under Settings.</Text>
        <NumberInput label="Keep audit events for (days)" description="Who signed in, changed settings, made keys and models. 0 keeps them forever." min={0} max={3650} value={audit} onChange={(v) => setAuditDays(typeof v === "number" ? v : null)} />
        <NumberInput label="Keep webhook delivery history for (days)" min={1} max={3650} value={delivery} onChange={(v) => setDeliveryDays(typeof v === "number" ? v : null)} />
      </Stack>
    ),
  });
  if (!hasAccounts) {
    steps.push({
      label: "Owner",
      ready: name.trim() !== "" && email.trim() !== "" && password.length >= 10,
      body: (
        <Stack>
          <Text size="sm" c="dimmed">You'll be the server's owner: you can do everything, including adding people.</Text>
          <TextInput label="Your name" required maxLength={100} value={name} onChange={(e) => setName(e.currentTarget.value)} />
          <TextInput label="Email" type="email" autoComplete="username" required value={email} onChange={(e) => setEmail(e.currentTarget.value)} />
          <PasswordInput label="Password" description="At least 10 characters." autoComplete="new-password" required minLength={10} value={password} onChange={(e) => setPassword(e.currentTarget.value)} />
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
    <AuthCard wide title="Welcome to mimir" intro={needsConfig ? "This server isn't set up yet. A few steps and it's yours." : "This server has no accounts yet. Make yours; you'll be its owner."}>
      <form onSubmit={(e) => { e.preventDefault(); if (!current.ready) return; if (last) finish(); else setStep(step + 1); }}>
        <Stack>
          <div>
            <Text size="sm" c="dimmed">Step {step + 1} of {steps.length}: <Text span fw={600} c="var(--mantine-color-text)">{current.label}</Text></Text>
            <Progress mt={6} size="sm" value={((step + 1) / steps.length) * 100} />
          </div>
          {current.body}
          {setup.error && <Alert color="red" icon={<IconAlertCircle />} style={{ overflowWrap: "anywhere" }}>{message(setup.error)}</Alert>}
          <Group justify="space-between">
            <Button variant="default" disabled={step === 0} onClick={() => setStep(step - 1)}>Back</Button>
            <Button type="submit" disabled={!current.ready} loading={setup.isPending}>{last ? (needsConfig ? "Finish setup" : "Create my account") : "Next"}</Button>
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
        <Grid.Col span={{ base: 12, xs: 8 }}><TextInput label="Host" placeholder="localhost" required autoComplete="off" spellCheck={false} value={value.host} onChange={(e) => set("host", e.currentTarget.value)} /></Grid.Col>
        <Grid.Col span={{ base: 12, xs: 4 }}><NumberInput label="Port" required min={1} max={65535} hideControls value={value.port} onChange={(v) => set("port", typeof v === "number" ? v : 0)} /></Grid.Col>
      </Grid>
      <SimpleGrid cols={{ base: 1, xs: 2 }} spacing="sm">
        <TextInput label="Username" required autoComplete="off" spellCheck={false} value={value.user} onChange={(e) => set("user", e.currentTarget.value)} />
        <PasswordInput label="Password" autoComplete="new-password" value={value.password} onChange={(e) => set("password", e.currentTarget.value)} />
      </SimpleGrid>
      <SimpleGrid cols={{ base: 1, xs: 2 }} spacing="sm">
        <TextInput label="Database name" required autoComplete="off" spellCheck={false} value={value.database} onChange={(e) => set("database", e.currentTarget.value)} />
        <Select label="Encryption (SSL)" allowDeselect={false} value={value.ssl} onChange={(v) => v && set("ssl", v as Postgres["ssl"])} data={[
          { value: "prefer", label: "Use it if the server has it" },
          { value: "require", label: "Always (a server elsewhere)" },
          { value: "disable", label: "Off (same machine)" },
        ]} />
      </SimpleGrid>
      <Group>
        <Button variant="light" onClick={check} loading={pending} disabled={!postgresReady(value)}>Test connection</Button>
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
  const intro = <Text size="sm" c="dimmed">The secret key encrypts the secrets the server stores for you: webhook signing secrets, database connection strings for training, alert passwords, and the database password in its configuration file. A copy of the database gives none of them away without it, and the server can't read them without it either.</Text>;
  if (source === SecretKeySource.ENVIRONMENT || source === SecretKeySource.FILE) {
    return (
      <Stack>
        {intro}
        <Alert color="teal" icon={<IconKey />}>
          {source === SecretKeySource.ENVIRONMENT ? "The server already has its key from $MIMIR_SECRET_KEY or -secret-key-file." : "The server already has a key file in its data folder, and keeps using it."}
        </Alert>
      </Stack>
    );
  }
  return (
    <Stack>
      {intro}
      <SegmentedControl value={mode} onChange={(v) => setMode(v as "new" | "mine")} data={[{ value: "new", label: "Generate a new key" }, { value: "mine", label: "Use a key I have" }]} />
      {mode === "new" ? (
        <Stack gap="sm">
          <div>
            <Text size="sm" fw={500} mb={4}>Your new key</Text>
            <Group gap="xs" wrap="nowrap">
              <Code block className="flex-1" style={{ overflowWrap: "anywhere", whiteSpace: "normal" }}>{newKey}</Code>
              <CopyButton value={newKey}>
                {({ copied, copy }) => (
                  <Tooltip label={copied ? "Copied" : "Copy"}>
                    <ActionIcon variant="light" size="lg" onClick={copy} aria-label="Copy the key">{copied ? <IconCheck size={18} /> : <IconCopy size={18} />}</ActionIcon>
                  </Tooltip>
                )}
              </CopyButton>
              <Tooltip label="Generate another">
                <ActionIcon variant="default" size="lg" onClick={regenerate} aria-label="Generate another key"><IconRefresh size={18} /></ActionIcon>
              </Tooltip>
            </Group>
          </div>
          <Text size="sm">The server saves it as secret.key in its data folder. <b>Keep a copy somewhere safe, such as a password manager</b>: a restored database backup can't be read without it.</Text>
          <Checkbox label="I've saved a copy of this key" checked={saved} onChange={(e) => setSaved(e.currentTarget.checked)} />
        </Stack>
      ) : (
        <PasswordInput label="Secret key" description="The key a backup of this database was made with: 32 bytes as base64 or hex." required autoComplete="off" value={value} onChange={(e) => setValue(e.currentTarget.value)} />
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
    <AuthCard title="All set" intro={hasAccounts ? "The server is configured and keeps the accounts its database already had." : "The server is configured and your owner account is ready."}>
      <Stack>
        <Group gap="xs">
          <Badge color={ready ? "teal" : "gray"} variant="light">{ready ? "Server ready" : "Server starting…"}</Badge>
        </Group>
        <Text size="sm" c="dimmed">Setup is now closed: this page won't open again. Back up the data folder's secret.key with the database.</Text>
        <Button fullWidth disabled={!ready} onClick={() => { queryClient.clear(); void navigate({ to: "/sign-in" }); }}>Sign in</Button>
      </Stack>
    </AuthCard>
  );
}
