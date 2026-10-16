import { timestampDate } from "@bufbuild/protobuf/wkt";
import { Badge, Button, Card, Group, Stack, Table, Tabs, Text } from "@mantine/core";
import { useSuspenseInfiniteQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";

import { auditLog } from "../../api/queries";
import { Page } from "../../components/Page";

export const Route = createFileRoute("/_app/settings")({
  loader: ({ context }) => context.queryClient.ensureInfiniteQueryData(auditLog),
  component: Settings,
});

const later = [
  { title: "Database", blurb: "Built-in SQLite now. Switch to PostgreSQL and run updates from here." },
  { title: "Alerts", blurb: "Get an email or a Telegram message when something needs you." },
  { title: "Log retention", blurb: "Decide how long requests and audit events are kept." },
];

function Settings() {
  return (
    <Page title="Settings" intro="How this server stores data and keeps you informed.">
      <Tabs defaultValue="audit">
        <Tabs.List>
          <Tabs.Tab value="audit">Audit log</Tabs.Tab>
          {later.map((l) => <Tabs.Tab key={l.title} value={l.title}>{l.title}</Tabs.Tab>)}
        </Tabs.List>
        <Tabs.Panel value="audit" pt="md"><AuditLog /></Tabs.Panel>
        {later.map((l) => (
          <Tabs.Panel key={l.title} value={l.title} pt="md">
            <Card withBorder radius="lg" padding="lg">
              <Badge variant="light" mb="sm">Coming soon</Badge>
              <Text fw={600}>{l.title}</Text>
              <Text c="dimmed">{l.blurb}</Text>
            </Card>
          </Tabs.Panel>
        ))}
      </Tabs>
    </Page>
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
