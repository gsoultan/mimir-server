import { useMutation } from "@connectrpc/connect-query";
import {
  Alert, Button, Card, Chip, Group, Pagination, Progress, Select, SimpleGrid, Stack, Switch, Table, Text, TextInput,
} from "@mantine/core";
import { useDebouncedValue } from "@mantine/hooks";
import { notifications } from "@mantine/notifications";
import { IconAlertCircle, IconDownload, IconPlayerPlay, IconPlayerStop, IconSearch, IconTrash } from "@tabler/icons-react";
import { keepPreviousData, useQuery, useQueryClient, useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useState } from "react";

import { message } from "../../../../api/client";
import { JobStatus } from "../../../../api/gen/mimir/v1/training_pb";
import { VisitService } from "../../../../api/gen/mimir/v1/visit_pb";
import { allVisitQueries, getVisitPlan, planSalespeople, whoAmI } from "../../../../api/queries";
import { AnchorLink } from "../../../../components/links";
import { Page } from "../../../../components/Page";
import { PlanRepeat } from "../../../../components/PlanRepeat";
import { StatTile, StatusPill } from "../../../../components/ui";
import { ago, percent } from "../../../../lib/format";
import { planDays, planProgress, planStatus, planWaiting, repeatOf, repeatRequest, repeatText, visitsURL, weekdays, type Repeat } from "../../../../lib/visits";
import { t } from "../../../../i18n";
import { count } from "../../../../lib/templates";

export const Route = createFileRoute("/_app/visits/$id/")({
  loader: ({ context, params }) => context.queryClient.ensureQueryData(getVisitPlan(params.id)),
  errorComponent: ({ error }) => (
    <Alert color="red" icon={<IconAlertCircle />} className="mx-auto max-w-5xl">
      {message(error)} <Link to="/visits">{t("Back to visit plans")}</Link>
    </Alert>
  ),
  component: VisitPlanPage,
});

const pageSize = 50;
const orders = [
  { value: "name", label: t("Name") },
  { value: "covered", label: t("Least covered first") },
  { value: "left", label: t("Most left over first") },
  { value: "visits", label: t("Most visits first") },
];

function VisitPlanPage() {
  const { id } = Route.useParams();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { data: me } = useSuspenseQuery(whoAmI);
  const canChange = me.permissions.includes("models.write");
  const { data } = useSuspenseQuery({ ...getVisitPlan(id), refetchInterval: (q) => (q.state.data?.plan && planWaiting(q.state.data.plan) ? 1500 : false) });
  const p = data.plan!;
  const busy = planWaiting(p);
  const [area, setArea] = useState("");
  const [search, setSearch] = useState("");
  const [needLook, setNeedLook] = useState(false);
  const [order, setOrder] = useState("name");
  const [page, setPage] = useState(1);
  const [q] = useDebouncedValue(search, 250);
  const people = useQuery({
    ...planSalespeople({ planId: id, area, search: q.trim(), needLook, order, pageSize, offset: (page - 1) * pageSize }),
    enabled: p.runs > 0,
    placeholderData: keepPreviousData,
  });
  const refresh = () => queryClient.invalidateQueries({ queryKey: allVisitQueries });
  const run = useMutation(VisitService.method.runVisitPlan, { onSuccess: refresh });
  const cancel = useMutation(VisitService.method.cancelVisitPlan, { onSuccess: refresh });
  const repeat = useMutation(VisitService.method.setVisitPlanRepeat, { onSuccess: refresh });
  const remove = useMutation(VisitService.method.deleteVisitPlan, {
    onSuccess: async () => {
      await navigate({ to: "/visits" });
      queryClient.removeQueries({ queryKey: getVisitPlan(id).queryKey });
      await refresh();
      notifications.show({ message: t("{name} was deleted.", { name: p.name }) });
    },
  });
  const error = run.error ?? cancel.error ?? repeat.error ?? remove.error ?? people.error;
  const fromDB = p.source === "database";
  const total = people.data?.total ?? 0;
  const filter = (f: () => void) => { f(); setPage(1); };

  return (
    <Page
      title={p.name}
      crumbs={[{ label: t("Plan visits"), to: "/visits" }, { label: p.name }]}
      badge={<StatusPill tone={p.error || p.status === JobStatus.FAILED ? "bad" : busy ? "busy" : p.status === JobStatus.SUCCEEDED ? "ok" : "idle"}>{p.error || p.status === JobStatus.FAILED ? t("Failed") : busy ? t("Planning") : p.status === JobStatus.SUCCEEDED ? t("Ready") : t("Stopped")}</StatusPill>}
      intro={<>
        {planDays(p) || count(p.days, "day")}
        {p.skipWeekdays.length > 0 ? t(", not on {n1}", { n1: p.skipWeekdays.map((d) => weekdays[d]).join(t(" or ")) }) : ""}{" "}{t("· customers from")}{" "}{p.sourceDetail}
        {" · "}{p.modelId ? <>{t("ranked by")}{" "}<AnchorLink to="/models/$id" params={{ id: p.modelId }}>{p.modelName || t("a deleted model")}</AnchorLink></> : t("ranked by priority")}
        {p.skipVisitedDays > 0 ? t(" · leaves out customers visited in the last {skipVisitedDays} days", { skipVisitedDays: p.skipVisitedDays }) : ""}
      </>}
      actions={
        <Group gap="xs">
          {p.runs > 0 && <Button component="a" href={visitsURL(p.id)} download leftSection={<IconDownload size={18} />}>{t("Download visits")}</Button>}
          {canChange && (busy
            ? <Button variant="default" color="red" leftSection={<IconPlayerStop size={18} />} loading={cancel.isPending} onClick={() => cancel.mutate({ id })}>{t("Stop")}</Button>
            : <Button variant="default" leftSection={<IconPlayerPlay size={18} />} loading={run.isPending} onClick={() => run.mutate({ id })}>{fromDB ? t("Fetch and plan again") : t("Plan again")}</Button>)}
          {canChange && <Button variant="subtle" color="red" leftSection={<IconTrash size={16} />} disabled={p.status === JobStatus.RUNNING} loading={remove.isPending} onClick={() => window.confirm(t("Delete {name}?", { name: p.name })) && remove.mutate({ id })}>{t("Delete")}</Button>}
        </Group>
      }
    >
      {error && <Alert color="red" icon={<IconAlertCircle />}>{message(error)}</Alert>}
      {(busy || !!p.error || fromDB) && <Card withBorder padding="lg">
        <Stack gap="xs">
          <Text fw={650}>{planStatus(p)}</Text>
          {busy && <Progress value={planProgress(p)} color="violet" animated />}
          {p.error && <Alert color="red" icon={<IconAlertCircle />}>{p.error}</Alert>}
          <Text size="sm" c="dimmed">
            {p.runs > 0 ? (p.runs === 1 ? t("Made once") : t("Made {runs} times", { runs: p.runs })) : t("Not made yet")}
            {p.endTime ? t(" · last {endTime}", { endTime: ago(p.endTime) }) : ""}
            {p.repeatSeconds > 0n ? (p.schedule ? t(" · made again at {when}", { when: repeatText(p) }) : t(" · made again {when}", { when: repeatText(p).toLowerCase() })) : ""}
            {p.repeatSeconds > 0n && p.nextRunTime ? t(" · next {nextRunTime}", { nextRunTime: ago(p.nextRunTime) }) : ""}
          </Text>
          {fromDB && canChange && (
            <RepeatEditor key={`${p.repeatSeconds}-${p.schedule?.atMinute}-${p.schedule?.weekdays.join()}`} initial={repeatOf(p)} saving={repeat.isPending}
              onSave={(r) => repeat.mutate({ id, ...repeatRequest(r) }, { onSuccess: () => notifications.show({ message: r.mode === "0" ? t("It won't be made again on its own.") : t("Saved.") }) })} />
          )}
        </Stack>
      </Card>}
      {p.runs > 0 && (
        <>
          <SimpleGrid cols={{ base: 2, sm: 4 }} spacing={{ base: "xs", sm: "md" }}>
            <StatTile value={Number(p.salespeople).toLocaleString()} label={t("salespeople")} hint={p.areas.length > 1 ? t("in {length} areas", { length: p.areas.length }) : undefined} />
            <StatTile value={Number(p.visits).toLocaleString()} label={t("visits")} hint={t("of {n1} customers", { n1: Number(p.customers).toLocaleString() })} />
            <StatTile value={percent(p.covered)} label={t("of likely orders visited")} />
            <StatTile value={Number(p.needLook).toLocaleString()} label={t("need a look")} hint={Number(p.needLook) > 0 ? t("Filter them below") : undefined} />
          </SimpleGrid>
          {p.warnings.map((w) => <Alert key={w} color="orange" icon={<IconAlertCircle />}>{w}</Alert>)}
          <Card withBorder padding="lg">
            <Stack gap="sm">
              {p.areas.length > 1 && (
                <Chip.Group value={area} onChange={(v) => filter(() => setArea(v as string))}>
                  <Group gap="xs">
                    <Chip value="" size="sm">{t("All areas")}</Chip>
                    {p.areas.map((a) => <Chip key={a.area} value={a.area} size="sm">{a.area || t("No area")} · {a.salespeople}</Chip>)}
                  </Group>
                </Chip.Group>
              )}
              <Group gap="sm" align="end">
                <TextInput className="min-w-[200px] flex-1" leftSection={<IconSearch size={16} />} placeholder={t("Find a salesperson by name or id")} aria-label={t("Find a salesperson")}
                  value={search} onChange={(e) => filter(() => setSearch(e.currentTarget.value))} />
                <Select w={210} data={orders} value={order} onChange={(v) => filter(() => setOrder(v ?? "name"))} allowDeselect={false} aria-label={t("Order")} />
                <Switch label={t("Need a look")} checked={needLook} onChange={(e) => { const v = e.currentTarget.checked; filter(() => setNeedLook(v)); }} mb={8} />
              </Group>
              <Table.ScrollContainer minWidth={560}>
                <Table fz="sm" highlightOnHover>
                  <Table.Thead>
                    <Table.Tr><Table.Th>{t("Salesperson")}</Table.Th><Table.Th>{t("Area")}</Table.Th><Table.Th ta="right">{t("Customers")}</Table.Th><Table.Th ta="right">{t("Visits")}</Table.Th><Table.Th ta="right">{t("Km")}</Table.Th><Table.Th ta="right">{t("Covered")}</Table.Th><Table.Th>{t("Note")}</Table.Th></Table.Tr>
                  </Table.Thead>
                  <Table.Tbody>
                    {(people.data?.salespeople ?? []).map((sp) => (
                      <Table.Tr key={sp.id}>
                        <Table.Td><AnchorLink to="/visits/$id/$salesperson" params={{ id, salesperson: sp.id }} fw={600}>{sp.name || sp.id}</AnchorLink>{sp.name && <Text size="xs" c="dimmed">{sp.id}</Text>}</Table.Td>
                        <Table.Td>{sp.area}</Table.Td>
                        <Table.Td ta="right">{sp.customers.toLocaleString()}</Table.Td>
                        <Table.Td ta="right">{sp.visits.toLocaleString()}</Table.Td>
                        <Table.Td ta="right">{Math.round(sp.km).toLocaleString()}</Table.Td>
                        <Table.Td ta="right">{sp.customers > 0 ? percent(sp.covered) : ""}</Table.Td>
                        <Table.Td>{sp.problem && <StatusPill tone="warn">{sp.problem}</StatusPill>}</Table.Td>
                      </Table.Tr>
                    ))}
                  </Table.Tbody>
                </Table>
              </Table.ScrollContainer>
              {people.data && total === 0 && <Text size="sm" c="dimmed" ta="center" py="md">{t("No salespeople match.")}</Text>}
              <Group justify="space-between">
                <Text size="sm" c="dimmed">{count(total, "salesperson", "salespeople")}</Text>
                {total > pageSize && <Pagination size="sm" total={Math.ceil(total / pageSize)} value={page} onChange={setPage} />}
              </Group>
            </Stack>
          </Card>
        </>
      )}
    </Page>
  );
}

// RepeatEditor changes how a plan is made again, saved with a button so
// a time being typed isn't sent on every key.
function RepeatEditor({ initial, saving, onSave }: { initial: Repeat; saving: boolean; onSave: (r: Repeat) => void }) {
  const [r, setR] = useState(initial);
  const changed = JSON.stringify(r) !== JSON.stringify(initial);
  return (
    <Stack gap="xs" maw={480}>
      <PlanRepeat label={t("Make it again")} value={r} onChange={setR} />
      {changed && <Group><Button size="xs" loading={saving} onClick={() => onSave(r)}>{t("Save")}</Button><Button size="xs" variant="subtle" onClick={() => setR(initial)}>{t("Undo")}</Button></Group>}
    </Stack>
  );
}
