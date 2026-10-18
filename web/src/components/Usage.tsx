import { useMutation } from "@connectrpc/connect-query";
import { Alert, Button, Card, Group, NumberInput, Progress, Select, Stack, Table, Text, Title } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { IconAlertCircle } from "@tabler/icons-react";
import { useQuery, useQueryClient, useSuspenseQuery } from "@tanstack/react-query";
import { useState } from "react";

import { message } from "../api/client";
import { SettingsService } from "../api/gen/mimir/v1/settings_pb";
import { allSettingsQueries, settings, usage } from "../api/queries";

// The last twelve months, newest first, as "2026-10".
function months() {
  const now = new Date();
  return Array.from({ length: 12 }, (_, i) => {
    const d = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth() - i, 1));
    const value = d.toISOString().slice(0, 7);
    return { value, label: d.toLocaleDateString(undefined, { month: "long", year: "numeric", timeZone: "UTC" }) };
  });
}

// Limits is the admin form for how much one person or app may use.
export function Limits() {
  const { data } = useSuspenseQuery(settings);
  const queryClient = useQueryClient();
  const [perMinute, setPerMinute] = useState<number | string>(data.limits?.predictionsPerMinute ?? 0);
  const [perMonth, setPerMonth] = useState<number | string>(Number(data.limits?.predictionsPerMonth ?? 0n));
  const [runs, setRuns] = useState<number | string>(data.limits?.trainingRunsPerPerson ?? 0);
  const save = useMutation(SettingsService.method.updateLimits, {
    onSuccess: async () => { await queryClient.invalidateQueries({ queryKey: allSettingsQueries }); notifications.show({ color: "teal", message: "Limits saved." }); },
  });
  return (
    <Card withBorder radius="lg" padding="lg" className="max-w-xl">
      <form onSubmit={(e) => { e.preventDefault(); save.mutate({ limits: { predictionsPerMinute: Number(perMinute) || 0, predictionsPerMonth: BigInt(Number(perMonth) || 0), trainingRunsPerPerson: Number(runs) || 0 } }); }}>
        <Stack>
          <Text c="dimmed">Keep one person or app from using up the server. Leave a box at 0 for no limit. Whoever hits a limit gets a clear message saying when they can try again.</Text>
          <NumberInput label="Predictions a minute" description="For each API key, and for each person on the web, on their own." min={0} max={1000000} thousandSeparator="," value={perMinute} onChange={setPerMinute} />
          <NumberInput label="Predictions a month" description="For each person, counting all their keys. Resets on the 1st." min={0} thousandSeparator="," value={perMonth} onChange={setPerMonth} />
          <NumberInput label="Training runs at once" description="Waiting or learning, for each person." min={0} max={1000} value={runs} onChange={setRuns} />
          {save.error && <Alert color="red" icon={<IconAlertCircle />}>{message(save.error)}</Alert>}
          <Button type="submit" loading={save.isPending} className="self-start">Save</Button>
        </Stack>
      </form>
    </Card>
  );
}

// Usage shows predictions per person and key for a month. Admins see
// everyone; others see their own.
// With userId it shows only that person's own use, even to an admin.
export function Usage({ userId }: { userId?: string }) {
  const compact = userId !== undefined;
  const choices = months();
  const [month, setMonth] = useState(choices[0]!.value);
  const { data, error } = useQuery(usage(month));
  if (error) return <Alert color="red" icon={<IconAlertCircle />}>{message(error)}</Alert>;
  const limit = Number(data?.limit ?? 0n);
  const rows = (data?.rows ?? []).filter((r) => !compact || r.userId === userId);
  const total = compact ? rows.reduce((n, r) => n + Number(r.predictions), 0) : Number(data?.total ?? 0n);
  return (
    <Stack>
      <Group justify="space-between" align="flex-end">
        <div>
          <Title order={3} fz="lg">{compact ? "Your usage" : "Usage"}</Title>
          <Text c="dimmed" size="sm">{compact ? "Predictions you made, on the web and with your keys." : "Predictions made, by person and by app key."}</Text>
        </div>
        <Select aria-label="Month" data={choices} value={month} onChange={(v) => v && setMonth(v)} allowDeselect={false} w={200} />
      </Group>
      <Card withBorder radius="lg" padding="lg">
        <Group justify="space-between" mb={limit > 0 ? "xs" : 0}>
          <Text fw={600}>{total.toLocaleString()} predictions{compact ? "" : " in all"}</Text>
          {limit > 0 && <Text size="sm" c="dimmed">Limit: {limit.toLocaleString()} a person each month</Text>}
        </Group>
        {limit > 0 && compact && <Progress value={Math.min(100, (total / limit) * 100)} color={total >= limit ? "red" : "teal"} aria-label="Share of the monthly limit used" />}
      </Card>
      {rows.length > 0 && (
        <Table.ScrollContainer minWidth={420}>
          <Table verticalSpacing="xs">
            <Table.Thead><Table.Tr>{!compact && <Table.Th>Person</Table.Th>}<Table.Th>Used from</Table.Th><Table.Th ta="right">Predictions</Table.Th></Table.Tr></Table.Thead>
            <Table.Tbody>
              {rows.map((r) => (
                <Table.Tr key={r.userId + r.keyName}>
                  {!compact && <Table.Td>{r.userName} <Text span c="dimmed" size="sm">{r.email}</Text></Table.Td>}
                  <Table.Td>{r.keyName ? `Key "${r.keyName}"` : "The web app"}</Table.Td>
                  <Table.Td ta="right">{Number(r.predictions).toLocaleString()}</Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>
      )}
    </Stack>
  );
}
