import type { MessageInitShape } from "@bufbuild/protobuf";
import { useMutation } from "@connectrpc/connect-query";
import {
  Alert, Button, Card, Chip, Group, NumberInput, PasswordInput, SegmentedControl, Select, SimpleGrid, Stack, Table, Text, Textarea, TextInput, Title,
} from "@mantine/core";
import { Dropzone } from "@mantine/dropzone";
import { notifications } from "@mantine/notifications";
import { IconAlertCircle, IconFlask, IconRoute } from "@tabler/icons-react";
import { useQueryClient, useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { z } from "zod";

import { message } from "../../../api/client";
import { DatasetKind } from "../../../api/gen/mimir/v1/training_pb";
import { VisitService, type SimulateVisitPlanResponse, type VisitPlanSettingsSchema } from "../../../api/gen/mimir/v1/visit_pb";
import { allVisitQueries, listArchitectures, listDatasets, listModels, whoAmI } from "../../../api/queries";
import { Page } from "../../../components/Page";
import { StatTile, StatusPill } from "../../../components/ui";
import { bytes, percent } from "../../../lib/format";
import { scores } from "../../../lib/scoring";
import { planDays, planRepeats, weekdays } from "../../../lib/visits";

export const Route = createFileRoute("/_app/visits/new")({
  validateSearch: z.object({ model: z.string().optional() }),
  loader: ({ context }) => Promise.all([
    context.queryClient.ensureQueryData(listModels),
    context.queryClient.ensureQueryData(listArchitectures),
    context.queryClient.ensureQueryData(listDatasets),
  ]),
  component: NewVisitPlan,
});

const drivers = [
  { value: "postgres", label: "PostgreSQL", example: "postgres://user:password@db.example.com:5432/shop" },
  { value: "mysql", label: "MySQL", example: "user:password@tcp(db.example.com:3306)/shop" },
  { value: "sqlite", label: "SQLite", example: "/srv/data/shop.db" },
];

type File = { name: string; csv: Uint8Array };

function Upload({ file, onFile, hint }: { file?: File; onFile: (f: File) => void; hint: string }) {
  return (
    <Dropzone accept={["text/csv", ".csv"]} multiple={false} maxSize={64 << 20}
      onDrop={async ([f]) => f && onFile({ name: f.name, csv: new Uint8Array(await f.arrayBuffer()) })}
      onReject={() => notifications.show({ color: "red", message: "Choose a .csv file of up to 64 MB." })}>
      <Stack align="center" gap={2} py="md" className="pointer-events-none">
        <Text fw={600}>{file ? file.name : "Drop your .csv file here"}</Text>
        <Text size="sm" c="dimmed" ta="center">{file ? bytes(file.csv.length) : hint}</Text>
      </Stack>
    </Dropzone>
  );
}

function NewVisitPlan() {
  const { model: modelParam } = Route.useSearch();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { data: me } = useSuspenseQuery(whoAmI);
  const { data: models } = useSuspenseQuery(listModels);
  const { data: archs } = useSuspenseQuery(listArchitectures);
  const { data: datasets } = useSuspenseQuery(listDatasets);
  const canConnect = me.permissions.includes("databases.query");
  const fit = models.models
    .filter((m) => scores(archs.architectures.find((a) => a.name === m.architecture)))
    .map((m) => ({ value: m.id, label: m.name }));
  const tables = datasets.datasets.filter((d) => d.kind === DatasetKind.TABLE).map((d) => ({ value: d.id, label: `${d.name} (${Number(d.rows).toLocaleString()} rows)` }));

  const [model, setModel] = useState<string>(fit.find((m) => m.value === modelParam)?.value ?? "");
  const [source, setSource] = useState(canConnect ? "database" : "upload");
  const [customers, setCustomers] = useState<File>();
  const [dataset, setDataset] = useState<string | null>(tables.at(0)?.value ?? null);
  const [driver, setDriver] = useState("postgres");
  const [dsn, setDsn] = useState("");
  const [customersQuery, setCustomersQuery] = useState("");
  const [peopleSource, setPeopleSource] = useState(canConnect ? "database" : "upload");
  const [people, setPeople] = useState<File>();
  const [peopleQuery, setPeopleQuery] = useState("");
  const [days, setDays] = useState("7");
  const [startDate, setStartDate] = useState("");
  const [timeZone, setTimeZone] = useState(Intl.DateTimeFormat().resolvedOptions().timeZone);
  const [skip, setSkip] = useState<string[]>(["0"]);
  const [skipVisited, setSkipVisited] = useState<number>(3);
  const [visitMinutes, setVisitMinutes] = useState<number>(30);
  const [repeat, setRepeat] = useState("0");
  const [name, setName] = useState("");
  const [preview, setPreview] = useState<SimulateVisitPlanResponse>();

  const usesDB = source === "database" || peopleSource === "database";
  const peopleDB = source === "database" && peopleSource === "database";
  const ready = (source === "upload" ? !!customers : source === "dataset" ? !!dataset : !!customersQuery.trim())
    && (peopleSource === "upload" ? !!people : !!peopleQuery.trim())
    && (!usesDB || !!dsn.trim());

  const settings = (): MessageInitShape<typeof VisitPlanSettingsSchema> => ({
    modelId: model,
    customersCsv: source === "upload" ? customers?.csv : undefined,
    customersDatasetId: source === "dataset" ? dataset ?? "" : "",
    connection: usesDB ? { driver, dsn } : undefined,
    customersQuery: source === "database" ? customersQuery.trim() : "",
    salespeopleCsv: peopleSource === "upload" ? people?.csv : undefined,
    salespeopleQuery: peopleSource === "database" ? peopleQuery.trim() : "",
    days: Number(days),
    startDate: repeat === "0" ? startDate : "",
    timeZone,
    skipWeekdays: skip.map(Number),
    skipVisitedDays: skipVisited,
    visitMinutes,
  });
  const simulate = useMutation(VisitService.method.simulateVisitPlan, { onSuccess: setPreview });
  const create = useMutation(VisitService.method.createVisitPlan, {
    onSuccess: async (res) => {
      await queryClient.invalidateQueries({ queryKey: allVisitQueries });
      notifications.show({ message: `Making ${res.plan?.name}.` });
      await navigate({ to: "/visits/$id", params: { id: res.plan!.id } });
    },
  });
  const error = simulate.error ?? create.error;
  const changed = () => { setPreview(undefined); simulate.reset(); create.reset(); };

  return (
    <Page
      title="Plan visits"
      crumbs={[{ label: "Plan visits", to: "/visits" }, { label: "New plan" }]}
      intro="Who visits whom, and in what order, for the next days. Try the plan on your data first; nothing is kept until you make it."
    >
      <Card withBorder padding="lg">
        <Stack gap="md">
          <Title order={2}>Customers</Title>
          <Text size="sm" c="dimmed">
            One row per customer with <b>customer_id</b>, <b>latitude</b> and <b>longitude</b>, and <b>salesperson_id</b> when there is more than one salesperson.
            Without a model, add a <b>priority</b> column. Optional: name, must_visit, open_minute, close_minute, visit_minutes, days_since_last_visit.
          </Text>
          <SegmentedControl value={source} onChange={(v) => { setSource(v); if (v !== "database") setPeopleSource("upload"); changed(); }} data={[
            ...(canConnect ? [{ value: "database", label: "Database" }] : []),
            { value: "upload", label: "A file" },
            { value: "dataset", label: "Examples" },
          ]} />
          {source === "upload" && <Upload file={customers} onFile={(f) => { setCustomers(f); changed(); }} hint="up to 64 MB; for more customers, read them from a database" />}
          {source === "dataset" && (tables.length === 0
            ? <Text size="sm" c="dimmed">No tables of examples yet.</Text>
            : <Select label="Examples" data={tables} value={dataset} onChange={(v) => { setDataset(v); changed(); }} allowDeselect={false} />)}
          {usesDB && (
            <Stack gap="sm">
              <SegmentedControl value={driver} onChange={(v) => { setDriver(v); changed(); }} data={drivers.map((d) => ({ value: d.value, label: d.label }))} />
              {driver === "sqlite"
                ? <TextInput label="Database file" description="The path to the file on the server." value={dsn} onChange={(e) => { setDsn(e.currentTarget.value); changed(); }} />
                : <PasswordInput label="Connection" description={`For example ${drivers.find((d) => d.value === driver)!.example}. Kept on the server; never shown again.`} value={dsn} onChange={(e) => { setDsn(e.currentTarget.value); changed(); }} />}
            </Stack>
          )}
          {source === "database" && (
            <Textarea label="Customers query" autosize minRows={3} classNames={{ input: "font-mono" }} value={customersQuery} onChange={(e) => { setCustomersQuery(e.currentTarget.value); changed(); }}
              placeholder={"SELECT c.id AS customer_id, c.name, c.lat AS latitude, c.lon AS longitude,\n  a.salesperson_id, current_date - c.last_visit AS days_since_last_visit, ...\nFROM customers c JOIN assignments a ON a.customer_id = c.id AND a.month = date_trunc('month', current_date)"}
              description="Join this month's salesperson to each customer, so a rotation shows up the day it starts." />
          )}
          <Select label="Model" placeholder="None: use the priority column" clearable data={fit} value={model || null} onChange={(v) => { setModel(v ?? ""); changed(); }}
            description="A model that answers whether a customer orders soon, or in how many days, ranks the customers. The customers need the columns it reads." />
        </Stack>
      </Card>

      <Card withBorder padding="lg">
        <Stack gap="md">
          <Title order={2}>Salespeople</Title>
          <Text size="sm" c="dimmed">
            One row per salesperson with <b>id</b> and where they start each day, <b>start_latitude</b> and <b>start_longitude</b>. Optional: name, area, day_start_minute, day_end_minute, max_visits.
          </Text>
          <SegmentedControl value={peopleSource} onChange={(v) => { setPeopleSource(v); changed(); }} data={[
            ...(source === "database" ? [{ value: "database", label: "Same database" }] : []),
            { value: "upload", label: "A file" },
          ]} />
          {peopleSource === "upload" && <Upload file={people} onFile={(f) => { setPeople(f); changed(); }} hint="up to 5,000 salespeople" />}
          {peopleDB && (
            <Textarea label="Salespeople query" autosize minRows={2} classNames={{ input: "font-mono" }} value={peopleQuery} onChange={(e) => { setPeopleQuery(e.currentTarget.value); changed(); }}
              placeholder="SELECT id, name, area, home_lat AS start_latitude, home_lon AS start_longitude FROM salespeople WHERE active" />
          )}
        </Stack>
      </Card>

      <Card withBorder padding="lg">
        <Stack gap="md">
          <Title order={2}>The days</Title>
          <SimpleGrid cols={{ base: 1, sm: 2 }}>
            <div>
              <Text size="sm" fw={500} mb={4}>Days to plan</Text>
              <SegmentedControl fullWidth value={days} onChange={(v) => { setDays(v); changed(); }} data={["1", "2", "3", "4", "5", "6", "7"]} />
            </div>
            <Select label="Make it" data={planRepeats} value={repeat} allowDeselect={false} disabled={source !== "database"} onChange={(v) => { setRepeat(v ?? "0"); changed(); }}
              description={source === "database" ? "Made again, it reads the customers afresh and starts from that day." : "Only a plan from a database can be made again."} />
            <TextInput type="date" label="First day" description="Empty means the day it is made." value={startDate} disabled={repeat !== "0"} onChange={(e) => { setStartDate(e.currentTarget.value); changed(); }} />
            <TextInput label="Time zone" description="Whose day it is." value={timeZone} onChange={(e) => { setTimeZone(e.currentTarget.value); changed(); }} />
          </SimpleGrid>
          <div>
            <Text size="sm" fw={500} mb={6}>Days off</Text>
            <Chip.Group multiple value={skip} onChange={(v) => { setSkip(v); changed(); }}>
              <Group gap="xs">{weekdays.map((d, i) => <Chip key={d} value={String(i)} size="sm">{d}</Chip>)}</Group>
            </Chip.Group>
          </div>
          <SimpleGrid cols={{ base: 1, sm: 2 }}>
            <NumberInput label="Leave out customers visited in the last" rightSection={<Text size="sm" c="dimmed" pr="sm">days</Text>} rightSectionWidth={56} min={0} max={90}
              description="By any salesperson, from days_since_last_visit. 0 plans everyone. A must_visit customer is always planned."
              value={skipVisited} onChange={(v) => { setSkipVisited(typeof v === "number" ? v : 0); changed(); }} />
            <NumberInput label="A visit takes" rightSection={<Text size="sm" c="dimmed" pr="sm">min</Text>} rightSectionWidth={48} min={0} max={720}
              description="Unless the customer's visit_minutes says otherwise."
              value={visitMinutes} onChange={(v) => { setVisitMinutes(typeof v === "number" ? v : 30); changed(); }} />
          </SimpleGrid>
          <TextInput label="Name" placeholder="Optional" maxLength={100} value={name} onChange={(e) => setName(e.currentTarget.value)} />
        </Stack>
      </Card>

      {error && <Alert color="red" icon={<IconAlertCircle />}>{message(error)}</Alert>}
      {preview && <Preview res={preview} />}
      <Group justify="flex-end">
        <Button variant="default" leftSection={<IconFlask size={18} />} disabled={!ready} loading={simulate.isPending}
          onClick={() => simulate.mutate({ settings: settings() })}>Try it</Button>
        <Button leftSection={<IconRoute size={18} />} disabled={!ready} loading={create.isPending}
          onClick={() => create.mutate({ name: name.trim(), settings: settings(), repeatSeconds: BigInt(repeat) })}>Make the plan</Button>
      </Group>
      <Text size="xs" c="dimmed" ta="right">Try it plans up to 20,000 customers and 100 salespeople and keeps nothing. Make the plan has no such limit and runs in the background.</Text>
    </Page>
  );
}

function Preview({ res }: { res: SimulateVisitPlanResponse }) {
  const p = res.plan!;
  const shown = res.salespeople.slice(0, 50);
  return (
    <Card withBorder padding="lg">
      <Stack gap="md">
        <Group justify="space-between">
          <Title order={2}>The plan, tried</Title>
          <Text size="sm" c="dimmed">{planDays(p)}</Text>
        </Group>
        <SimpleGrid cols={{ base: 2, sm: 4 }} spacing={{ base: "xs", sm: "md" }}>
          <StatTile value={Number(p.salespeople).toLocaleString()} label="salespeople" />
          <StatTile value={Number(p.visits).toLocaleString()} label="visits" hint={`of ${Number(p.customers).toLocaleString()} customers`} />
          <StatTile value={percent(p.covered)} label="of likely orders visited" />
          <StatTile value={Number(p.needLook).toLocaleString()} label="need a look" />
        </SimpleGrid>
        {p.warnings.map((w) => <Alert key={w} color="orange" icon={<IconAlertCircle />}>{w}</Alert>)}
        <Table.ScrollContainer minWidth={520}>
          <Table fz="sm">
            <Table.Thead><Table.Tr><Table.Th>Salesperson</Table.Th><Table.Th>Area</Table.Th><Table.Th ta="right">Customers</Table.Th><Table.Th ta="right">Visits</Table.Th><Table.Th ta="right">Km</Table.Th><Table.Th>Note</Table.Th></Table.Tr></Table.Thead>
            <Table.Tbody>
              {shown.map((s) => {
                const sp = s.salesperson!;
                return (
                  <Table.Tr key={sp.id}>
                    <Table.Td>{sp.name || sp.id}</Table.Td>
                    <Table.Td>{sp.area}</Table.Td>
                    <Table.Td ta="right">{sp.customers.toLocaleString()}</Table.Td>
                    <Table.Td ta="right">{sp.visits.toLocaleString()}</Table.Td>
                    <Table.Td ta="right">{Math.round(sp.km).toLocaleString()}</Table.Td>
                    <Table.Td>{sp.problem ? <StatusPill tone="warn">{sp.problem}</StatusPill> : ""}</Table.Td>
                  </Table.Tr>
                );
              })}
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>
        {res.salespeople.length > shown.length && <Text size="sm" c="dimmed">And {res.salespeople.length - shown.length} more. Make the plan to see them all, with each day's route.</Text>}
      </Stack>
    </Card>
  );
}
