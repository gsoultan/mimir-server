import { Alert, Anchor, Badge, Button, Card, Group, Progress, Stack, Text, Title } from "@mantine/core";
import { useNetwork } from "@mantine/hooks";
import { IconArrowLeft, IconDeviceLaptop, IconRefresh, IconTrash, IconWifiOff } from "@tabler/icons-react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { useEffect, useState } from "react";
import { z } from "zod";

import { listModels } from "../api/queries";
import { Ask, askIntro } from "../components/Ask";
import * as device from "../lib/device";
import { bytes, share } from "../lib/format";

// The models saved on this device. The page is outside the signed-in app
// so it opens with no network: everything on it comes from the browser.
export const Route = createFileRoute("/device")({
  validateSearch: z.object({ model: z.string().optional() }),
  component: OnThisDevice,
});

function OnThisDevice() {
  const { model: open } = Route.useSearch();
  const navigate = Route.useNavigate();
  const { online } = useNetwork();
  const saved = device.useSaved();
  // Online, the server says which saved models have a newer version.
  const server = useQuery({ ...listModels, enabled: online, retry: false });
  const [space, setSpace] = useState<{ used: number; quota: number }>();
  useEffect(() => void device.usage().then(setSpace), [saved]);
  const update = useMutation({ mutationFn: (s: device.Saved) => device.save(server.data!.models.find((m) => m.id === s.model.id)!, s.arch) });
  const signedIn = server.isSuccess;

  const asking = saved?.find((s) => s.model.id === open);
  return (
    <div className="min-h-dvh bg-[var(--mantine-color-body)]">
      <header className="flex h-[60px] items-center justify-between gap-3 border-b border-[var(--mantine-color-default-border)] px-4">
        <Group gap="sm">
          <img src="/icon.svg" alt="" width={28} height={28} />
          <Text fw={700} ff="var(--font-display)" fz={22}>mimir</Text>
        </Group>
        {!online ? (
          <Badge color="gray" variant="light" leftSection={<IconWifiOff size={14} />}>Offline</Badge>
        ) : (
          <Button component={Link} to="/" variant="subtle" leftSection={<IconArrowLeft size={16} />}>{signedIn ? "Back to mimir" : "Open mimir"}</Button>
        )}
      </header>
      <Stack gap="lg" className="mx-auto w-full max-w-5xl" p={{ base: "md", sm: "xl" }}>
        <div>
          <Title order={1} fz={{ base: 28, sm: 34 }}>On this device</Title>
          <Text c="dimmed" mt={4}>Models saved in this browser answer here, with no server and no network. What you ask them stays on this device.</Text>
        </div>
        {!device.supported && <Alert color="yellow">This browser can't run models itself. Use a recent Chrome, Edge, Firefox or Safari.</Alert>}
        {saved && saved.length === 0 && (
          <Card withBorder radius="lg" padding="xl" className="border-dashed">
            <Text fw={600} mb={4}>Nothing saved here yet</Text>
            <Text c="dimmed" size="sm">In Try a model, pick a model someone let run on devices and choose “On this device”. It saves the model the first time you ask it.</Text>
          </Card>
        )}
        {asking && (
          <Card withBorder radius="lg" padding="lg">
            <Group justify="space-between" mb="md" wrap="nowrap">
              <div className="min-w-0">
                <Text fw={700} truncate>{asking.model.name}</Text>
                <Text size="sm" c="dimmed">{askIntro(asking.arch)}</Text>
              </div>
              <Button variant="default" onClick={() => void navigate({ search: {} })}>Done</Button>
            </Group>
            <Ask
              key={asking.model.id + asking.model.version}
              model={asking.model}
              arch={asking.arch}
              answer={(q) => device.answer({ id: asking.model.id, fields: q.fields, photo: q.photo, sound: q.sound, input: q.input?.values, topK: q.topK })}
              where="on this device"
            />
          </Card>
        )}
        {saved && saved.length > 0 && (
          <Stack gap="sm">
            {saved.map((s) => {
              const now = server.data?.models.find((m) => m.id === s.model.id);
              const newer = now && now.onDevice && now.version !== s.model.version;
              const gone = server.isSuccess && (!now || !now.onDevice);
              return (
                <Card key={s.model.id} withBorder radius="lg" padding="md">
                  <div className="flex flex-wrap items-center gap-3">
                    <div className="flex size-10 shrink-0 items-center justify-center rounded-xl bg-[var(--mantine-color-teal-light)]">
                      <IconDeviceLaptop size={22} color="var(--mantine-color-teal-7)" />
                    </div>
                    <div className="min-w-0 flex-1">
                      <Group gap="xs">
                        <Text fw={700} truncate>{s.model.name}</Text>
                        {newer && <Badge color="blue" variant="light">Newer version on the server</Badge>}
                        {gone && <Badge color="gray" variant="light">No longer shared to devices</Badge>}
                      </Group>
                      <Text size="sm" c="dimmed">{s.arch.title} · {bytes(s.bytes)} · saved {s.saved.toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric" })}</Text>
                    </div>
                    <Group gap="xs">
                      {newer && <Button variant="light" leftSection={<IconRefresh size={16} />} loading={update.isPending && update.variables?.model.id === s.model.id} onClick={() => update.mutate(s)}>Update</Button>}
                      <Button onClick={() => void navigate({ search: { model: s.model.id } })}>Ask it</Button>
                      <Button variant="subtle" color="red" leftSection={<IconTrash size={16} />} onClick={() => void device.remove(s.model.id)}>Remove</Button>
                    </Group>
                  </div>
                </Card>
              );
            })}
          </Stack>
        )}
        {update.error && <Alert color="red">{update.error.message}</Alert>}
        {space && space.quota > 0 && saved && saved.length > 0 && (
          <Card withBorder radius="lg" padding="md">
            <Group justify="space-between" mb="xs">
              <Text fw={600} size="sm">Space used by mimir in this browser</Text>
              <Text size="sm" c="dimmed">{bytes(space.used)} of {bytes(space.quota)}</Text>
            </Group>
            <Progress value={Math.max(share(space.used, space.quota), 1)} />
            <Group justify="space-between" mt="sm">
              <Text size="xs" c="dimmed">Removing a model here doesn't change it on the server.</Text>
              <Anchor component="button" size="sm" c="red" onClick={() => void device.removeAll()}>Remove everything</Anchor>
            </Group>
          </Card>
        )}
      </Stack>
    </div>
  );
}
