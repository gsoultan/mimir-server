import { Alert, Badge, Card, Group, SegmentedControl, SimpleGrid, Table, Text, Title } from "@mantine/core";
import { IconAlertCircle } from "@tabler/icons-react";
import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { useState } from "react";

import { message } from "../../../../api/client";
import type { PlanDay, SalespersonPlan } from "../../../../api/gen/mimir/v1/visit_pb";
import { salespersonPlan } from "../../../../api/queries";
import { Page } from "../../../../components/Page";
import { StatTile, StatusPill } from "../../../../components/ui";
import { percent } from "../../../../lib/format";
import { clock, fitPoints } from "../../../../lib/visits";
import { locale, t } from "../../../../i18n";
import { count } from "../../../../lib/templates";

export const Route = createFileRoute("/_app/visits/$id/$salesperson")({
  loader: ({ context, params }) => context.queryClient.ensureQueryData(salespersonPlan(params.id, params.salesperson)),
  errorComponent: ({ error }) => (
    <Alert color="red" icon={<IconAlertCircle />} className="mx-auto max-w-5xl">
      {message(error)} <Link to="/visits">{t("Back to visit plans")}</Link>
    </Alert>
  ),
  component: SalespersonPage,
});

const dayLabel = (d: PlanDay) => new Date(d.date + "T00:00:00").toLocaleDateString(locale, { weekday: "short", day: "numeric", month: "short" });

function SalespersonPage() {
  const { id, salesperson } = Route.useParams();
  const { data } = useSuspenseQuery(salespersonPlan(id, salesperson));
  const plan = data.plan!, sp = data.salesperson!, who = sp.salesperson!;
  const [day, setDay] = useState(String(sp.days.findIndex((d) => d.visits.length > 0) >= 0 ? sp.days.findIndex((d) => d.visits.length > 0) : 0));
  const today = sp.days[Number(day)];
  const valued = sp.days.some((d) => d.visits.some((v) => (v.customer?.orderValue ?? 0) > 0));

  return (
    <Page
      title={who.name || who.id}
      crumbs={[{ label: t("Plan visits"), to: "/visits" }, { label: plan.name, to: `/visits/${id}` }, { label: who.name || who.id }]}
      badge={who.area ? <Badge variant="light" color="gray">{who.area}</Badge> : undefined}
      intro={t("{customers} customers · {visits} visits in {days} · about {km} km", { customers: who.customers.toLocaleString(), visits: who.visits.toLocaleString(), days: count(sp.days.length, "day"), km: Math.round(who.km).toLocaleString() })}
    >
      {who.problem && <Alert color="orange" icon={<IconAlertCircle />}>{who.problem}</Alert>}
      <SimpleGrid cols={{ base: 2, sm: 4 }} spacing={{ base: "xs", sm: "md" }}>
        <StatTile value={who.visits.toLocaleString()} label={t("visits")} />
        <StatTile value={Math.round(who.km).toLocaleString()} label="km" />
        <StatTile value={who.customers > 0 ? percent(who.covered) : "–"} label={t("of likely orders visited")} />
        <StatTile value={who.likelyLeft.toLocaleString()} label={t("likely to order, not visited")} />
      </SimpleGrid>
      {sp.days.length > 0 && (
        <SegmentedControl fullWidth value={day} onChange={setDay}
          data={sp.days.map((d, i) => ({ value: String(i), label: `${dayLabel(d)} · ${d.visits.length}` }))} />
      )}
      {today && (
        <SimpleGrid cols={{ base: 1, md: 2 }} spacing="md">
          <Card withBorder padding={0}>
            <Table.ScrollContainer minWidth={360}>
              <Table fz="sm" highlightOnHover>
                <Table.Thead><Table.Tr><Table.Th>#</Table.Th><Table.Th>{t("Arrive")}</Table.Th><Table.Th>{t("Customer")}</Table.Th><Table.Th ta="right">{t("Chance")}</Table.Th>{valued && <Table.Th ta="right">{t("Order value")}</Table.Th>}<Table.Th ta="right">{t("Km")}</Table.Th></Table.Tr></Table.Thead>
                <Table.Tbody>
                  {today.visits.map((v, i) => (
                    <Table.Tr key={v.customer!.id}>
                      <Table.Td>{i + 1}</Table.Td>
                      <Table.Td>{clock(v.arriveMinute)}</Table.Td>
                      <Table.Td>
                        <Text size="sm" fw={600}>{v.customer!.name || v.customer!.id}</Text>
                        <Group gap={4}>{v.customer!.name && <Text size="xs" c="dimmed">{v.customer!.id}</Text>}{v.customer!.mustVisit && <StatusPill tone="busy">{t("Must visit")}</StatusPill>}</Group>
                      </Table.Td>
                      <Table.Td ta="right">{percent(v.customer!.chance)}</Table.Td>
                      {valued && <Table.Td ta="right">{v.customer!.orderValue.toLocaleString()}</Table.Td>}
                      <Table.Td ta="right">{v.kmFromPrevious.toFixed(1)}</Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            </Table.ScrollContainer>
            {today.visits.length === 0
              ? <Text size="sm" c="dimmed" ta="center" py="md">{t("No visits this day.")}</Text>
              : <Text size="sm" c="dimmed" p="sm">{t("Back by")}{" "}{clock(today.endMinute)} · {today.km.toFixed(1)}{" "}{t("km")}</Text>}
          </Card>
          <RouteMap sp={sp} day={today} />
        </SimpleGrid>
      )}
      {sp.likelyLeft.length > 0 && (
        <section>
          <Title order={2} mb={4}>{t("Likely to order, not visited")}</Title>
          <Text size="sm" c="dimmed" mb="sm">{t("They didn't fit in the days. Call them, or move a visit.")}</Text>
          <Card withBorder padding={0}>
            <Table fz="sm">
              <Table.Tbody>
                {sp.likelyLeft.map((c) => (
                  <Table.Tr key={c.id}><Table.Td>{c.name || c.id}</Table.Td><Table.Td c="dimmed">{c.name ? c.id : ""}</Table.Td><Table.Td ta="right">{percent(c.chance)}</Table.Td></Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </Card>
        </section>
      )}
    </Page>
  );
}

// RouteMap draws a day's route from the salesperson's start, north up.
function RouteMap({ sp, day }: { sp: SalespersonPlan; day: PlanDay }) {
  const w = 480, h = 340;
  const start = { lat: sp.startLatitude, lon: sp.startLongitude };
  const stops = day.visits.map((v) => ({ lat: v.customer!.latitude, lon: v.customer!.longitude }));
  const at = fitPoints([start, ...stops], w, h, 24);
  const path = [start, ...stops, start].map((p) => at(p).join(",")).join(" ");
  const [sx, sy] = at(start);
  return (
    <Card withBorder padding="xs" className="self-start">
      <svg viewBox={`0 0 ${w} ${h}`} className="h-auto w-full" role="img" aria-label={t("Route with {stops} visits", { stops: stops.length })}>
        <polyline points={path} fill="none" stroke="var(--mantine-color-violet-5)" strokeWidth={2} strokeLinejoin="round" opacity={0.7} />
        {stops.map((p, i) => {
          const [x, y] = at(p);
          return (
            <g key={i}>
              <circle cx={x} cy={y} r={9} fill="var(--mantine-color-body)" stroke="var(--mantine-color-violet-6)" strokeWidth={2} />
              <text x={x} y={y + 3.5} textAnchor="middle" fontSize={10} fontWeight={700} fill="var(--mantine-color-text)">{i + 1}</text>
            </g>
          );
        })}
        <rect x={sx - 7} y={sy - 7} width={14} height={14} rx={3} fill="var(--mantine-color-teal-6)" />
      </svg>
      <Text size="xs" c="dimmed" px="xs">{t("Green square: where the day starts and ends. Lines are straight; roads add about a third.")}</Text>
    </Card>
  );
}
