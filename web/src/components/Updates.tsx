import { create } from "@bufbuild/protobuf";
import { createClient } from "@connectrpc/connect";
import { useMutation } from "@connectrpc/connect-query";
import { Alert, Anchor, Button, Card, Group, Loader, Modal, Stack, Switch, Text, ThemeIcon, Title } from "@mantine/core";
import { useDisclosure } from "@mantine/hooks";
import { notifications } from "@mantine/notifications";
import { IconAlertCircle, IconCircleCheck, IconDownload, IconExternalLink, IconRefresh, IconSparkles } from "@tabler/icons-react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useEffect, useState } from "react";

import { message, transport } from "../api/client";
import { GetUpdatesResponseSchema, SettingsService, UpdateBlock, UpdateState, type Updates as UpdatesInfo } from "../api/gen/mimir/v1/settings_pb";
import { getUpdates } from "../api/queries";
import { ago } from "../lib/format";
import { t } from "../i18n";

// Updates is the About page's card for new versions: what the last check
// found, a button to check now and one to install, and the switch for
// automatic updates. Owners only.
export function Updates() {
  const queryClient = useQueryClient();
  const { data, error } = useQuery({
    ...getUpdates,
    // While it downloads, follow along.
    refetchInterval: (q) => (q.state.data?.updates?.state === UpdateState.INSTALLING ? 1500 : false),
  });
  const [installed, setInstalled] = useState<UpdatesInfo | undefined>(restarting);
  const set = (u?: UpdatesInfo) => queryClient.setQueryData(getUpdates.queryKey, create(GetUpdatesResponseSchema, { updates: u }));
  const check = useMutation(SettingsService.method.checkForUpdates, { onSuccess: (res) => set(res.updates) });
  const auto = useMutation(SettingsService.method.updateAutoUpdates, {
    onSuccess: (res) => {
      set(res.updates);
      notifications.show({ color: "teal", message: res.updates?.auto ? t("Automatic updates are on.") : t("Automatic updates are off.") });
    },
  });
  const [asking, ask] = useDisclosure();
  const install = useMutation(SettingsService.method.installUpdate, {
    onSuccess: (res) => {
      ask.close();
      set(res.updates);
      // The restart can come and go between two looks at the state, so
      // watch the version from now on.
      if (res.updates) waitForRestart(res.updates.currentVersion);
    },
  });

  const u = data?.updates;
  // Once the download is checked the server restarts; remember what it
  // was installing, since the server may not answer for a while and this
  // card may be drawn again meanwhile.
  useEffect(() => {
    if (u?.state === UpdateState.RESTARTING) {
      restarting = u;
      setInstalled(u);
      waitForRestart(u.currentVersion);
    } else if (u?.state === UpdateState.IDLE && u.installError) {
      stopWaiting();
    }
  }, [u]);

  if (installed) return <Restarting from={installed.currentVersion} to={installed.latestVersion} />;
  if (!u) {
    return (
      <Card withBorder radius="lg" padding="lg">
        <Title order={2} fz={18}>{t("Updates")}</Title>
        {error ? <Text c="red" size="sm" mt={8}>{message(error)}</Text> : <Loader size="sm" mt={8} />}
      </Card>
    );
  }
  if (u.blocked === UpdateBlock.TURNED_OFF) {
    return (
      <Card withBorder radius="lg" padding="lg">
        <Title order={2} fz={18}>{t("Updates")}</Title>
        <Text c="dimmed" size="sm" mt={8}>{t("Updates are turned off on this server, so it never looks for new versions. Whoever runs it upgrades it the way it was installed.")}</Text>
      </Card>
    );
  }

  const checking = u.state === UpdateState.CHECKING || check.isPending;
  const installing = u.state === UpdateState.INSTALLING;
  const canInstall = u.available && u.blocked === UpdateBlock.UNSPECIFIED;
  return (
    <Card withBorder radius="lg" padding="lg">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div className="min-w-0 flex-1">
          <Title order={2} fz={18}>{t("Updates")}</Title>
          <Status u={u} installing={installing} />
        </div>
        <Group gap="sm">
          <Button variant="default" leftSection={<IconRefresh size={18} />} loading={checking} disabled={installing} onClick={() => check.mutate({})}>{t("Check now")}</Button>
          {canInstall && <Button leftSection={<IconDownload size={18} />} loading={installing} onClick={ask.open}>{t("Update now")}</Button>}
        </Group>
      </div>

      {u.checkError && <Alert color="red" icon={<IconAlertCircle />} mt="md">{t("Couldn't check for a new version.")}{" "}{u.checkError}</Alert>}
      {u.installError && !installing && <Alert color="red" icon={<IconAlertCircle />} mt="md">{t("The last update didn't finish, so the server kept the version it has.")}{" "}{u.installError}</Alert>}
      {check.error && <Alert color="red" icon={<IconAlertCircle />} mt="md">{message(check.error)}</Alert>}

      <Stack gap={4} mt="md" pt="md" style={{ borderTop: "1px solid var(--mimir-line)" }}>
        <Switch
          label={t("Update automatically")}
          description={t("Looks for a new version every 6 hours and installs it by itself. Off, the server only looks when you press Check now.")}
          checked={u.auto}
          disabled={auto.isPending || u.blocked === UpdateBlock.DEV_BUILD}
          onChange={(e) => auto.mutate({ auto: e.currentTarget.checked })}
        />
        {auto.error && <Text c="red" size="sm">{message(auto.error)}</Text>}
      </Stack>

      <Modal opened={asking} onClose={ask.close} title={t("Update to version {version}?", { version: u.latestVersion })}>
        <Stack gap="sm">
          <Text size="sm">{t("The server downloads the new version, checks it is the real one and runs on this machine, then restarts on it. It's away for a few seconds while it restarts.")}</Text>
          <Text size="sm">{t("Training runs go back in the queue and carry on from their last checkpoint. If the new version doesn't start, the server goes back to this one.")}</Text>
          <Text size="sm" c="dimmed">
            {t("A new version may update the database too. For a way back, first download a backup under")}{" "}
            <Link to="/settings" search={{ tab: "database" }}>{t("Settings → Database")}</Link>.
          </Text>
          {install.error && <Alert color="red" icon={<IconAlertCircle />}>{message(install.error)}</Alert>}
          <Group justify="flex-end" mt="xs">
            <Button variant="default" onClick={ask.close}>{t("Cancel")}</Button>
            <Button leftSection={<IconDownload size={18} />} loading={install.isPending} onClick={() => install.mutate({})}>{t("Update now")}</Button>
          </Group>
        </Stack>
      </Modal>
    </Card>
  );
}

// restarting is the update being installed, kept outside the card so a
// card drawn again while the server restarts still shows it.
let restarting: UpdatesInfo | undefined;
let polling: number | undefined;

function stopWaiting() {
  window.clearInterval(polling);
  polling = undefined;
}

// waitForRestart asks the server its version every 2 seconds and reloads
// the page once it answers with a new one, so the new web app runs too.
function waitForRestart(from: string) {
  if (polling !== undefined) return;
  polling = window.setInterval(async () => {
    try {
      const about = await createClient(SettingsService, transport).getAbout({});
      if (about.version !== from) {
        stopWaiting();
        window.location.reload();
      }
    } catch {
      // Still restarting.
    }
  }, 2000);
}

// Status says what the last check found, in a sentence or two.
function Status({ u, installing }: { u: UpdatesInfo; installing: boolean }) {
  const checked = u.checked ? t("Last checked {when}.", { when: ago(u.checked) }) : t("Not checked yet.");
  if (installing) {
    return (
      <Group gap={8} mt={8} wrap="nowrap">
        <Loader size="xs" />
        <Text size="sm">{t("Downloading version {version} and checking it…", { version: u.latestVersion })}</Text>
      </Group>
    );
  }
  if (u.available) {
    return (
      <Stack gap={4} mt={8}>
        <Group gap={8} wrap="nowrap">
          <ThemeIcon variant="light" color="teal" size={26} radius="md"><IconSparkles size={16} /></ThemeIcon>
          <Text fw={650}>{t("Version {latest} is out. This server runs {current}.", { latest: u.latestVersion, current: u.currentVersion })}</Text>
        </Group>
        {u.releaseUrl && (
          <Anchor href={u.releaseUrl} target="_blank" rel="noreferrer" size="sm" className="inline-flex items-center gap-1">
            {t("See what's new")}<IconExternalLink size={14} />
          </Anchor>
        )}
        {u.blocked === UpdateBlock.DEV_BUILD && <Text size="sm" c="dimmed">{t("This is a development build, so it can't update itself. Build or install the new version yourself.")}</Text>}
        {u.blocked === UpdateBlock.NO_DOWNLOAD && <Text size="sm" c="dimmed">{t("This release has no download for this machine, so upgrade it the way you installed it.")}</Text>}
        <Text size="sm" c="dimmed">{checked}</Text>
      </Stack>
    );
  }
  if (u.latestVersion) {
    return (
      <Stack gap={4} mt={8}>
        <Group gap={8} wrap="nowrap">
          <ThemeIcon variant="light" color="teal" size={26} radius="md"><IconCircleCheck size={16} /></ThemeIcon>
          <Text fw={650}>{t("You have the newest version.")}</Text>
        </Group>
        <Text size="sm" c="dimmed">{checked}</Text>
      </Stack>
    );
  }
  return <Text size="sm" c="dimmed" mt={8}>{u.blocked === UpdateBlock.DEV_BUILD ? t("This is a development build, so it can't update itself.") : checked}</Text>;
}

// Restarting waits for the server to come back on the new version, then
// reloads the page so it runs the new web app too.
function Restarting({ from, to }: { from: string; to: string }) {
  const [slow, setSlow] = useState(false);
  useEffect(() => {
    const id = window.setTimeout(() => setSlow(true), 3 * 60_000);
    return () => window.clearTimeout(id);
  }, []);
  return (
    <Card withBorder radius="lg" padding="lg">
      <Title order={2} fz={18}>{t("Updates")}</Title>
      <Group gap={8} mt={8} wrap="nowrap">
        <Loader size="xs" />
        <Text size="sm">{t("Restarting on version {version}. This page reloads when the server is back.", { version: to })}</Text>
      </Group>
      {slow && <Alert color="yellow" icon={<IconAlertCircle />} mt="md">{t("This is taking longer than it should. If the new version didn't start, the server goes back to version {version} on its own; its log says why.", { version: from })}</Alert>}
    </Card>
  );
}
