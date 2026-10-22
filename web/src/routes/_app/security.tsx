import { timestampDate } from "@bufbuild/protobuf/wkt";
import { useMutation } from "@connectrpc/connect-query";
import { Alert, Anchor, Badge, Button, Card, Code, CopyButton, Group, Modal, PasswordInput, PinInput, SimpleGrid, Stack, Text, TextInput, Title } from "@mantine/core";
import { useDisclosure } from "@mantine/hooks";
import { notifications } from "@mantine/notifications";
import { IconAlertCircle, IconCheck, IconDeviceLaptop, IconDeviceMobile, IconShieldCheck, IconShieldOff } from "@tabler/icons-react";
import { useQueryClient, useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import QRCode from "qrcode";
import { useEffect, useState } from "react";

import { message } from "../../api/client";
import { AuthService, type Session } from "../../api/gen/mimir/v1/access_pb";
import { allAuthQueries, listSessions, whoAmI } from "../../api/queries";
import { Page } from "../../components/Page";
import { ago } from "../../lib/format";

export const Route = createFileRoute("/_app/security")({
  loader: ({ context }) => context.queryClient.ensureQueryData(listSessions),
  component: Security,
});

function Security() {
  const { data: me } = useSuspenseQuery(whoAmI);
  const [setting, setup] = useDisclosure();
  const [turningOff, off] = useDisclosure();
  const on = me.user?.twoFactor ?? false;
  return (
    <Page title="Security" intro="Keep your account yours: a second step at sign-in, and a look at where you're signed in.">
      {me.mustSetUpTwoFactor && (
        <Alert color="yellow" icon={<IconAlertCircle />} title="Two-step verification is required here" mb="md">
          An admin asks everyone with your role to use it. Set it up below; the rest of mimir unlocks right after.
        </Alert>
      )}
      <Stack gap="md">
        <Card withBorder radius="lg" padding="lg">
          <Group justify="space-between" align="flex-start" wrap="nowrap">
            <div>
              <Group gap="xs">
                <Title order={3} fz="lg">Two-step verification</Title>
                {on ? <Badge color="teal" variant="light" leftSection={<IconShieldCheck size={12} />}>On</Badge> : <Badge color="gray" variant="light" leftSection={<IconShieldOff size={12} />}>Off</Badge>}
              </Group>
              <Text c="dimmed" size="sm" mt={4}>
                {on
                  ? "Signing in takes your password and a six-digit code from your authenticator app. A stolen password alone can't get in."
                  : "Add a six-digit code from an authenticator app (Google Authenticator, Authy, 1Password, Microsoft Authenticator) to every sign-in."}
              </Text>
            </div>
            {on ? (
              <Button variant="default" onClick={off.open}>Turn off</Button>
            ) : (
              <Button onClick={setup.open}>Turn on</Button>
            )}
          </Group>
        </Card>
        <Sessions currentId={me.sessionId} />
      </Stack>
      <SetupModal key={setting ? "open" : "closed"} opened={setting} onClose={setup.close} />
      <TurnOffModal opened={turningOff} onClose={off.close} />
    </Page>
  );
}

// Setting up: password, scan, code, then the recovery codes once.
function SetupModal({ opened, onClose }: { opened: boolean; onClose: () => void }) {
  const queryClient = useQueryClient();
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [qr, setQr] = useState("");
  const start = useMutation(AuthService.method.startTwoFactor);
  const enable = useMutation(AuthService.method.enableTwoFactor, {
    onSuccess: () => queryClient.invalidateQueries({ queryKey: allAuthQueries }),
  });
  const url = start.data?.otpauthUrl;
  useEffect(() => {
    if (!url) return;
    QRCode.toDataURL(url, { margin: 1, width: 220 }).then(setQr).catch(() => setQr(""));
  }, [url]);
  const codes = enable.data?.recoveryCodes;
  const finish = () => { onClose(); notifications.show({ color: "teal", message: "Two-step verification is on. Other devices were signed out." }); };

  return (
    <Modal opened={opened} onClose={codes ? finish : onClose} title={codes ? "Save your recovery codes" : "Turn on two-step verification"} centered size="md" closeOnClickOutside={!codes}>
      {codes ? (
        <Stack>
          <Alert color="yellow" icon={<IconAlertCircle />}>If you lose your phone, one of these codes gets you in. Each works once. Keep them somewhere safe; they won't be shown again.</Alert>
          <SimpleGrid cols={2} spacing="xs">
            {codes.map((c) => <Code key={c} fz="md" p="xs" ta="center">{c}</Code>)}
          </SimpleGrid>
          <Group justify="space-between">
            <CopyButton value={codes.join("\n")}>
              {({ copied, copy }) => <Button variant="default" leftSection={copied ? <IconCheck size={16} /> : undefined} onClick={copy}>{copied ? "Copied" : "Copy all"}</Button>}
            </CopyButton>
            <Button onClick={finish}>I saved them</Button>
          </Group>
        </Stack>
      ) : !start.data ? (
        <form onSubmit={(e) => { e.preventDefault(); start.mutate({ password }); }}>
          <Stack>
            <Text size="sm" c="dimmed">First, confirm it's you.</Text>
            <PasswordInput label="Your password" autoComplete="current-password" required value={password} onChange={(e) => setPassword(e.currentTarget.value)} data-autofocus />
            {start.error && <Alert color="red" icon={<IconAlertCircle />}>{message(start.error)}</Alert>}
            <Button type="submit" loading={start.isPending}>Continue</Button>
          </Stack>
        </form>
      ) : (
        <form onSubmit={(e) => { e.preventDefault(); enable.mutate({ code }); }}>
          <Stack>
            <Text size="sm"><b>1.</b> Open your authenticator app and scan this code.</Text>
            <div className="flex justify-center">
              {qr ? <img src={qr} alt="QR code for your authenticator app" width={220} height={220} className="rounded-md" /> : <div style={{ width: 220, height: 220 }} />}
            </div>
            <Text size="xs" c="dimmed" ta="center">Can't scan? Type this key instead: <Code>{start.data.secret}</Code></Text>
            <Text size="sm"><b>2.</b> Type the six digits the app shows.</Text>
            <PinInput length={6} type="number" oneTimeCode autoFocus value={code} onChange={setCode} aria-label="Six-digit code" className="justify-center" />
            {enable.error && <Alert color="red" icon={<IconAlertCircle />}>{message(enable.error)}</Alert>}
            <Button type="submit" loading={enable.isPending} disabled={code.length < 6}>Turn on</Button>
          </Stack>
        </form>
      )}
    </Modal>
  );
}

// Turning off takes the password and a current code, or a recovery code
// for someone who lost their phone, so a stolen cookie and password alone
// can't do it.
function TurnOffModal({ opened, onClose }: { opened: boolean; onClose: () => void }) {
  const queryClient = useQueryClient();
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [recovery, setRecovery] = useState(false);
  const disable = useMutation(AuthService.method.disableTwoFactor, {
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: allAuthQueries });
      setPassword(""); setCode(""); setRecovery(false); onClose();
      notifications.show({ message: "Two-step verification is off." });
    },
  });
  return (
    <Modal opened={opened} onClose={onClose} title="Turn off two-step verification?" centered>
      <form onSubmit={(e) => { e.preventDefault(); disable.mutate({ password, code }); }}>
        <Stack>
          <Text size="sm" c="dimmed">Your password alone will sign you in again. Your recovery codes stop working.</Text>
          <PasswordInput label="Your password" autoComplete="current-password" required value={password} onChange={(e) => setPassword(e.currentTarget.value)} data-autofocus />
          {recovery ? (
            <TextInput label="Recovery code" placeholder="xxxx-xxxx-xxxx" autoComplete="one-time-code" required value={code} onChange={(e) => setCode(e.currentTarget.value)} />
          ) : (
            <Stack gap={4}>
              <Text size="sm" fw={500}>Code from your authenticator app</Text>
              <PinInput length={6} type="number" oneTimeCode value={code} onChange={setCode} aria-label="Six-digit code" />
            </Stack>
          )}
          <Anchor component="button" type="button" size="sm" ta="left" onClick={() => { setRecovery(!recovery); setCode(""); }}>
            {recovery ? "Use the app instead" : "Lost your phone? Use a recovery code"}
          </Anchor>
          {disable.error && <Alert color="red" icon={<IconAlertCircle />}>{message(disable.error)}</Alert>}
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>Keep it on</Button>
            <Button color="red" type="submit" loading={disable.isPending} disabled={!recovery && code.length < 6}>Turn off</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}

function Sessions({ currentId }: { currentId: string }) {
  const { data } = useSuspenseQuery(listSessions);
  const queryClient = useQueryClient();
  const refresh = () => queryClient.invalidateQueries({ queryKey: allAuthQueries });
  const revoke = useMutation(AuthService.method.revokeSessions, {
    onSuccess: async (res) => { await refresh(); notifications.show({ message: res.revoked === 1 ? "Signed out on that device." : `Signed out on ${res.revoked} devices.` }); },
  });
  const others = data.sessions.filter((s) => s.id !== currentId);
  return (
    <Card withBorder radius="lg" padding="lg">
      <Group justify="space-between" align="flex-start" mb="sm">
        <div>
          <Title order={3} fz="lg">Where you're signed in</Title>
          <Text c="dimmed" size="sm" mt={4}>Each browser gets its own sign-in. Sign out of any you don't recognise.</Text>
        </div>
        {others.length > 0 && <Button variant="default" loading={revoke.isPending} onClick={() => revoke.mutate({})}>Sign out everywhere else</Button>}
      </Group>
      {revoke.error && <Alert color="red" icon={<IconAlertCircle />} mb="sm">{message(revoke.error)}</Alert>}
      <Stack gap="xs">
        {data.sessions.map((s) => <SessionRow key={s.id} session={s} current={s.id === currentId} onRevoke={() => revoke.mutate({ id: s.id })} />)}
      </Stack>
    </Card>
  );
}

function SessionRow({ session: s, current, onRevoke }: { session: Session; current: boolean; onRevoke: () => void }) {
  return (
    <Group justify="space-between" wrap="nowrap" className="rounded-md border border-gray-200 px-3 py-2">
      <Group gap="sm" wrap="nowrap" className="min-w-0">
        {/iphone|ipad|android/i.test(s.device) ? <IconDeviceMobile size={22} stroke={1.6} className="shrink-0 text-gray-500" /> : <IconDeviceLaptop size={22} stroke={1.6} className="shrink-0 text-gray-500" />}
        <div className="min-w-0">
          <Group gap="xs">
            <Text fw={600} size="sm">{s.device}</Text>
            {current && <Badge size="xs" variant="light" color="teal">This device</Badge>}
          </Group>
          <Text size="xs" c="dimmed">
            {s.ip && `${s.ip} · `}
            {s.lastSeenTime && `Active ${ago(s.lastSeenTime)}`}
            {s.createTime && ` · Signed in ${timestampDate(s.createTime).toLocaleDateString()}`}
          </Text>
        </div>
      </Group>
      {!current && <Button size="xs" variant="subtle" color="red" onClick={onRevoke}>Sign out</Button>}
    </Group>
  );
}
