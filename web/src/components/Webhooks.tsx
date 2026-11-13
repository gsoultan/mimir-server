import { timestampDate } from "@bufbuild/protobuf/wkt";
import { useMutation } from "@connectrpc/connect-query";
import { Alert, Badge, Button, Card, Checkbox, Code, Collapse, CopyButton, Group, Modal, Stack, Switch, Table, Text, TextInput, Title } from "@mantine/core";
import { useDisclosure } from "@mantine/hooks";
import { notifications } from "@mantine/notifications";
import { IconAlertCircle, IconCheck, IconCopy, IconSend, IconWebhook } from "@tabler/icons-react";
import { useQueryClient, useSuspenseInfiniteQuery, useSuspenseQuery } from "@tanstack/react-query";
import { useState } from "react";

import { message } from "../api/client";
import { DeliveryStatus, WebhookService, type Delivery, type Webhook } from "../api/gen/mimir/v1/webhook_pb";
import { allWebhookQueries, deliveries, listWebhooks } from "../api/queries";
import { ago } from "../lib/format";
import { t } from "../i18n";

// Plain words for each event type.
const eventWords: Record<string, string> = {
  "model.created": t("A model is added"),
  "model.updated": t("A model is renamed or described"),
  "model.deleted": t("A model is deleted"),
  "model.accuracy_dropped": t("A model gets fewer answers right in use"),
  "training.queued": t("A training run is queued"),
  "training.finished": t("A training run finishes"),
  "training.failed": t("A training run fails"),
  "scoring.finished": t("A list is scored"),
  "scoring.failed": t("Scoring a list fails"),
  "user.created": t("A person is added"),
  "user.deleted": t("A person is removed"),
  test: t("Test events you send from here"),
};

const when = new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" });

export function Webhooks() {
  const { data } = useSuspenseQuery(listWebhooks);
  const queryClient = useQueryClient();
  const [adding, add] = useDisclosure();
  const [editing, setEditing] = useState<Webhook>();
  const [secret, setSecret] = useState<string>();
  const [removing, setRemoving] = useState<Webhook>();
  const refresh = () => queryClient.invalidateQueries({ queryKey: allWebhookQueries });
  const remove = useMutation(WebhookService.method.deleteWebhook, {
    onSuccess: async () => { await refresh(); notifications.show({ message: t("{name} was removed.", { name: removing?.name }) }); setRemoving(undefined); },
  });

  return (
    <Stack gap="sm">
      <Group justify="space-between">
        <div>
          <Title order={2} fz="lg">{t("Webhooks")}</Title>
          <Text size="sm" c="dimmed">{t("mimir calls your app's address when something happens.")}</Text>
        </div>
        <Button variant="default" leftSection={<IconWebhook size={18} />} onClick={add.open}>{t("Add a webhook")}</Button>
      </Group>
      {data.webhooks.length === 0 && (
        <Card withBorder radius="lg" padding="lg" className="border-dashed">
          <Text c="dimmed">{t("No webhooks yet. Add one to get told when a model changes.")}</Text>
        </Card>
      )}
      {data.webhooks.map((w) => <WebhookCard key={w.id} hook={w} onEdit={() => setEditing(w)} onRemove={() => setRemoving(w)} onChange={refresh} />)}

      <WebhookModal key={adding ? "add" : "closed"} opened={adding} onClose={add.close} eventTypes={data.eventTypes} onDone={async (s) => { await refresh(); add.close(); if (s) setSecret(s); }} />
      <WebhookModal key={editing?.id ?? "none"} opened={!!editing} onClose={() => setEditing(undefined)} eventTypes={data.eventTypes} hook={editing} onDone={async () => { await refresh(); setEditing(undefined); }} />

      <Modal opened={!!secret} onClose={() => setSecret(undefined)} title={t("Your webhook's signing secret")} centered closeOnClickOutside={false}>
        <Alert color="yellow" mb="md">{t("Copy it now; it's shown only this once. Your app uses it to check that calls really come from mimir.")}</Alert>
        <Group wrap="nowrap" gap="xs">
          <Code block className="flex-1 break-all">{secret}</Code>
          <CopyButton value={secret ?? ""}>
            {({ copied, copy }) => <Button onClick={copy} leftSection={copied ? <IconCheck size={16} /> : <IconCopy size={16} />}>{copied ? t("Copied") : t("Copy")}</Button>}
          </CopyButton>
        </Group>
        <Text size="sm" c="dimmed" mt="md">{t("Each call has a")}{" "}<Code>Mimir-Signature: t=&lt;time&gt;,v1=&lt;hex&gt;</Code>{" "}{t("header, where v1 is HMAC-SHA256 of")}{" "}<Code>"&lt;time&gt;.&lt;body&gt;"</Code>{" "}{t("with this secret.")}</Text>
        <Group justify="flex-end" mt="lg"><Button onClick={() => setSecret(undefined)}>{t("I've saved it")}</Button></Group>
      </Modal>

      <Modal opened={!!removing} onClose={() => setRemoving(undefined)} title={t("Remove {name}?", { name: removing?.name })} centered>
        <Text mb="lg">{t("Your app at that address will stop getting calls, and the delivery history goes too.")}</Text>
        {remove.error && <Alert color="red" mb="md" icon={<IconAlertCircle />}>{message(remove.error)}</Alert>}
        <Group justify="flex-end">
          <Button variant="default" onClick={() => setRemoving(undefined)}>{t("Keep it")}</Button>
          <Button color="red" loading={remove.isPending} onClick={() => removing && remove.mutate({ id: removing.id })}>{t("Remove")}</Button>
        </Group>
      </Modal>
    </Stack>
  );
}

function statusBadge(d: Delivery | undefined) {
  if (!d) return <Badge variant="light" color="gray">{t("Nothing sent yet")}</Badge>;
  switch (d.status) {
    case DeliveryStatus.DELIVERED: return <Badge variant="light" color="teal">{t("Working")}</Badge>;
    case DeliveryStatus.FAILED: return <Badge variant="light" color="red">{t("Failing")}</Badge>;
    default: return <Badge variant="light" color="yellow">{d.attempts > 0 ? t("Retrying") : t("Sending")}</Badge>;
  }
}

function WebhookCard({ hook, onEdit, onRemove, onChange }: { hook: Webhook; onEdit: () => void; onRemove: () => void; onChange: () => Promise<unknown> }) {
  const [open, { toggle }] = useDisclosure();
  const test = useMutation(WebhookService.method.sendTestEvent, {
    onSuccess: async () => { notifications.show({ message: t("Test event sent. Check the history in a moment.") }); await onChange(); },
  });
  return (
    <Card withBorder radius="lg" padding="md">
      <Group justify="space-between" wrap="nowrap">
        <div className="min-w-0">
          <Group gap="xs" wrap="nowrap">
            <Text fw={700} truncate>{hook.name}</Text>
            {!hook.enabled ? <Badge variant="light" color="gray">{t("Off")}</Badge> : statusBadge(hook.lastDelivery)}
          </Group>
          <Text size="sm" c="dimmed" truncate>{hook.url}</Text>
          <Text size="xs" c="dimmed">{hook.events.length === 0 ? t("Every event") : hook.events.map((e) => eventWords[e] ?? e).join(" · ")}</Text>
        </div>
        <Group gap="xs" wrap="nowrap">
          <Button size="xs" variant="default" leftSection={<IconSend size={14} />} loading={test.isPending} onClick={() => test.mutate({ id: hook.id })}>{t("Send a test")}</Button>
          <Button size="xs" variant="default" onClick={onEdit}>{t("Edit")}</Button>
          <Button size="xs" variant="subtle" onClick={toggle}>{open ? t("Hide history") : t("History")}</Button>
        </Group>
      </Group>
      {test.error && <Alert color="red" mt="sm" icon={<IconAlertCircle />}>{message(test.error)}</Alert>}
      <Collapse expanded={open}>
        <div className="mt-3">{open && <Deliveries hookId={hook.id} />}</div>
        <Group justify="flex-end" mt="sm"><Button size="xs" variant="subtle" color="red" onClick={onRemove}>{t("Remove webhook")}</Button></Group>
      </Collapse>
    </Card>
  );
}

function Deliveries({ hookId }: { hookId: string }) {
  const { data, hasNextPage, fetchNextPage, isFetchingNextPage, refetch } = useSuspenseInfiniteQuery(deliveries(hookId));
  const rows = data.pages.flatMap((p) => p.deliveries);
  const redeliver = useMutation(WebhookService.method.redeliver, {
    onSuccess: async () => { notifications.show({ message: t("Sent again.") }); await refetch(); },
  });
  if (rows.length === 0) return <Text size="sm" c="dimmed">{t("Nothing has been sent to this webhook yet.")}</Text>;
  return (
    <Stack gap="xs">
      <Table.ScrollContainer minWidth={520}>
        <Table verticalSpacing="xs" fz="sm">
          <Table.Thead><Table.Tr><Table.Th>{t("When")}</Table.Th><Table.Th>{t("Event")}</Table.Th><Table.Th>{t("Result")}</Table.Th><Table.Th></Table.Th></Table.Tr></Table.Thead>
          <Table.Tbody>
            {rows.map((d) => (
              <Table.Tr key={d.id}>
                <Table.Td className="whitespace-nowrap">{d.createTime ? when.format(timestampDate(d.createTime)) : ""}</Table.Td>
                <Table.Td>{d.eventType}</Table.Td>
                <Table.Td>
                  {d.status === DeliveryStatus.DELIVERED && <Text span c="teal.8">{t("Delivered (")}{d.responseCode})</Text>}
                  {d.status === DeliveryStatus.FAILED && <Text span c="red">{t("Gave up after")}{" "}{d.attempts}{" "}{t("tries:")}{" "}{d.error}</Text>}
                  {d.status === DeliveryStatus.PENDING && (
                    <Text span c="dimmed">{d.attempts === 0 ? t("Sending…") : t("Try {attempts} failed ({error}); next {n3}", { attempts: d.attempts, error: d.error, n3: d.nextAttemptTime ? ago(d.nextAttemptTime) : t("soon") })}</Text>
                  )}
                </Table.Td>
                <Table.Td className="whitespace-nowrap">
                  {d.status !== DeliveryStatus.PENDING && <Button size="compact-xs" variant="subtle" onClick={() => redeliver.mutate({ id: d.id })}>{t("Send again")}</Button>}
                </Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      </Table.ScrollContainer>
      <Group>
        <Button size="xs" variant="subtle" onClick={() => refetch()}>{t("Refresh")}</Button>
        {hasNextPage && <Button size="xs" variant="default" loading={isFetchingNextPage} onClick={() => fetchNextPage()}>{t("Show older")}</Button>}
      </Group>
    </Stack>
  );
}

function WebhookModal({ opened, onClose, eventTypes, hook, onDone }: { opened: boolean; onClose: () => void; eventTypes: string[]; hook?: Webhook; onDone: (secret?: string) => Promise<void> }) {
  const [name, setName] = useState(hook?.name ?? "");
  const [url, setUrl] = useState(hook?.url ?? "");
  const [all, setAll] = useState(hook ? hook.events.length === 0 : true);
  const [events, setEvents] = useState<string[]>(hook?.events ?? []);
  const [enabled, setEnabled] = useState(hook?.enabled ?? true);
  const create = useMutation(WebhookService.method.createWebhook, { onSuccess: (res) => onDone(res.secret) });
  const update = useMutation(WebhookService.method.updateWebhook, { onSuccess: () => onDone() });
  const error = create.error ?? update.error;
  const chosen = all ? [] : events;
  return (
    <Modal opened={opened} onClose={onClose} title={hook ? t("Edit {name}", { name: hook.name }) : t("Add a webhook")} centered size="lg">
      <form onSubmit={(e) => { e.preventDefault(); hook ? update.mutate({ id: hook.id, name, url, events: chosen, enabled }) : create.mutate({ name, url, events: chosen }); }}>
        <Stack>
          <TextInput label={t("Name")} placeholder={t("e.g. Shop backend")} required maxLength={100} value={name} onChange={(e) => setName(e.currentTarget.value)} data-autofocus />
          <TextInput label={t("Address to call")} placeholder="https://example.com/mimir-hook" description={t("Must be https. mimir sends a POST with a JSON body.")} required value={url} onChange={(e) => setUrl(e.currentTarget.value)} />
          <Checkbox label={t("Tell me about everything")} checked={all} onChange={(e) => setAll(e.currentTarget.checked)} />
          {!all && (
            <Checkbox.Group label={t("Which events?")} value={events} onChange={setEvents}>
              <Stack gap="xs" mt="xs">
                {eventTypes.map((t) => <Checkbox key={t} value={t} label={eventWords[t] ?? t} />)}
              </Stack>
            </Checkbox.Group>
          )}
          {hook && <Switch label={t("Turned on")} checked={enabled} onChange={(e) => setEnabled(e.currentTarget.checked)} />}
          {error && <Alert color="red" icon={<IconAlertCircle />}>{message(error)}</Alert>}
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>{t("Cancel")}</Button>
            <Button type="submit" loading={create.isPending || update.isPending}>{hook ? t("Save") : t("Add webhook")}</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}
