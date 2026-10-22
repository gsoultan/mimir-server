import { Code as ErrorCode, ConnectError, createClient } from "@connectrpc/connect";
import { useMutation, useQuery } from "@connectrpc/connect-query";
import { Alert, Badge, Button, Group, NumberInput, PasswordInput, Progress, SegmentedControl, Stack, Switch, Text, TextInput } from "@mantine/core";
import { IconAlertCircle, IconCircleCheck, IconKey } from "@tabler/icons-react";
import { createFileRoute, redirect, useNavigate } from "@tanstack/react-router";
import { useEffect, useState, type ReactNode } from "react";

import { message, transport } from "../api/client";
import { AuthService, SecretKeySource } from "../api/gen/mimir/v1/access_pb";
import { setupState } from "../api/queries";
import { AuthCard } from "../components/AuthCard";

export const Route = createFileRoute("/setup")({
  beforeLoad: async ({ context }) => {
    const { needsSetup } = await context.queryClient.fetchQuery(setupState);
    if (!needsSetup) throw redirect({ to: "/sign-in" });
  },
  component: Setup,
});

const postgresExample = "postgres://mimir:secret@localhost:5432/mimir";

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
  const [databaseUrl, setDatabaseUrl] = useState("");
  const [separateLog, setSeparateLog] = useState(false);
  const [logDatabaseUrl, setLogDatabaseUrl] = useState("");
  const [keyMode, setKeyMode] = useState<"new" | "mine">("new");
  const [secretKey, setSecretKey] = useState("");
  const [auditDays, setAuditDays] = useState<number | null>(null);
  const [deliveryDays, setDeliveryDays] = useState<number | null>(null);
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [finished, setFinished] = useState(false);

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
          <Text size="sm" c="dimmed">mimir keeps people, settings and model details in PostgreSQL. Make an empty database and a role that may create tables in it, then give its URL. It's saved encrypted in the configuration file, which only the server's user can read.</Text>
          <DatabaseField label="Database URL" value={databaseUrl} onChange={(v) => { setDatabaseUrl(v); checkMain.reset(); }} check={() => checkMain.mutate({ setupToken, databaseUrl })} pending={checkMain.isPending} error={checkMain.error} ok={checkMain.data && `${checkMain.data.version}${checkMain.data.hasAccounts ? ". It already has mimir accounts, which are kept." : ", ready."}`} />
          <Switch label="Keep the audit log in a separate database" description="For a log that people who run the main database can't change, or one kept on cheaper storage. Leave off to keep it with everything else." checked={separateLog} onChange={(e) => { setSeparateLog(e.currentTarget.checked); checkLog.reset(); }} />
          {separateLog && (
            <DatabaseField label="Log database URL" value={logDatabaseUrl} onChange={(v) => { setLogDatabaseUrl(v); checkLog.reset(); }} check={() => checkLog.mutate({ setupToken, databaseUrl: logDatabaseUrl })} pending={checkLog.isPending} error={checkLog.error} ok={checkLog.data && `${checkLog.data.version}, ready.`} />
          )}
        </Stack>
      ),
    });
    steps.push({
      label: "Secret key",
      ready: state?.secretKey !== SecretKeySource.NONE || keyMode === "new" || secretKey.trim() !== "",
      body: <KeyStep source={state?.secretKey ?? SecretKeySource.UNSPECIFIED} mode={keyMode} setMode={setKeyMode} value={secretKey} setValue={setSecretKey} />,
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
      databaseUrl,
      logDatabaseUrl: separateLog ? logDatabaseUrl : "",
      secretKey: keyMode === "mine" ? secretKey : "",
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

function DatabaseField({ label, value, onChange, check, pending, error, ok }: {
  label: string; value: string; onChange: (v: string) => void; check: () => void; pending: boolean; error: unknown; ok?: string | false;
}) {
  return (
    <Stack gap={6}>
      <Group align="end" wrap="nowrap">
        <TextInput className="flex-1" label={label} placeholder={postgresExample} required autoComplete="off" spellCheck={false} value={value} onChange={(e) => onChange(e.currentTarget.value)} />
        <Button variant="light" onClick={check} loading={pending} disabled={value.trim() === ""}>Test</Button>
      </Group>
      {error ? <Alert color="red" icon={<IconAlertCircle />} py={6} style={{ overflowWrap: "anywhere" }}>{message(error)}</Alert> : null}
      {ok ? <Text size="sm" c="teal"><IconCircleCheck size={16} className="mr-1 inline align-[-3px]" />{ok}</Text> : null}
    </Stack>
  );
}

function KeyStep({ source, mode, setMode, value, setValue }: {
  source: SecretKeySource; mode: "new" | "mine"; setMode: (m: "new" | "mine") => void; value: string; setValue: (v: string) => void;
}) {
  const intro = <Text size="sm" c="dimmed">The secret key encrypts the secrets the server stores for you: webhook signing secrets, database connection strings for training, alert passwords, and the database URLs in its configuration file. Without it a copy of the database gives none of them away, and without it the server can't read them either.</Text>;
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
      <SegmentedControl value={mode} onChange={(v) => setMode(v as "new" | "mine")} data={[{ value: "new", label: "Make a new key" }, { value: "mine", label: "Use a key I have" }]} />
      {mode === "new"
        ? <Text size="sm">A new key is saved as secret.key in the data folder, readable only by the server's user. <b>Back it up with the database</b>: a restored database can't be read without it.</Text>
        : <PasswordInput label="Secret key" description="32 bytes as base64 or hex, such as the key a backup of this database was made with, or the output of openssl rand -base64 32." required autoComplete="off" value={value} onChange={(e) => setValue(e.currentTarget.value)} />}
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
