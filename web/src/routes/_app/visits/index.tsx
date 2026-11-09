import { Badge, Button, Card, Group, Progress, Text, ThemeIcon } from "@mantine/core";
import { IconDownload, IconRoute } from "@tabler/icons-react";
import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";

import { JobStatus } from "../../../api/gen/mimir/v1/training_pb";
import { listVisitPlans, whoAmI } from "../../../api/queries";
import { AnchorLink, ButtonLink } from "../../../components/links";
import { Page } from "../../../components/Page";
import { Row, RowList } from "../../../components/ui";
import { ago } from "../../../lib/format";
import { planDays, planProgress, planRepeats, planStatus, planWaiting, visitsURL } from "../../../lib/visits";

export const Route = createFileRoute("/_app/visits/")({
  loader: ({ context }) => context.queryClient.ensureQueryData(listVisitPlans),
  component: VisitPlans,
});

function VisitPlans() {
  const { data } = useSuspenseQuery({ ...listVisitPlans, refetchInterval: (q) => (q.state.data?.plans.some(planWaiting) ? 2000 : false) });
  const { data: me } = useSuspenseQuery(whoAmI);
  const canMake = me.permissions.includes("models.write");
  const make = canMake && <ButtonLink to="/visits/new" leftSection={<IconRoute size={18} />}>Plan visits</ButtonLink>;

  return (
    <Page
      title="Plan visits"
      crumbs={[{ label: "Try a model", to: "/try" }, { label: "Plan visits" }]}
      intro="Give each salesperson a route for the next 1 to 7 days: the customers most likely to order, in a sensible order, each day. Plans from a database can be made again every morning."
      actions={data.plans.length > 0 && make}
    >
      {data.plans.length === 0 ? (
        <Card withBorder padding="xl" className="text-center">
          <ThemeIcon size={44} radius="xl" variant="light" mx="auto" mb="sm"><IconRoute size={22} /></ThemeIcon>
          <Text fw={650}>No visit plans yet</Text>
          <Text c="dimmed" mb="md">Bring your customers with their places and salespeople, and try a plan before you make it.</Text>
          {make && <Group justify="center">{make}</Group>}
        </Card>
      ) : (
        <RowList>
          {data.plans.map((p) => (
            <Row
              key={p.id}
              tone={p.error || p.status === JobStatus.FAILED ? "bad" : planWaiting(p) ? "busy" : Number(p.needLook) > 0 ? "warn" : "ok"}
              icon={<IconRoute size={20} />}
              title={<Group gap="xs" wrap="nowrap">
                <AnchorLink fw={650} truncate to="/visits/$id" params={{ id: p.id }} c="inherit">{p.name}</AnchorLink>
                {p.repeatSeconds > 0n && <Badge variant="light" color="gray">{planRepeats.find((r) => r.value === p.repeatSeconds.toString())?.label ?? "On a schedule"}</Badge>}
              </Group>}
              detail={<>
                {planDays(p) || `${p.days} ${p.days === 1 ? "day" : "days"}`} · {p.modelName || "by priority"} · {p.endTime ? `made ${ago(p.endTime)}` : `asked ${ago(p.createTime)}`}<br />
                <Text span c={p.error ? "red" : undefined} size="sm">
                  {p.error || planStatus(p)}
                  {!planWaiting(p) && Number(p.needLook) > 0 ? ` · ${Number(p.needLook).toLocaleString()} need a look` : ""}
                </Text>
              </>}
              extra={planWaiting(p) && <Progress value={planProgress(p)} color="violet" size="sm" mt={8} maw={380} animated aria-label="Progress" />}
            >
              {p.runs > 0 && !planWaiting(p) && <Button size="xs" variant="default" component="a" href={visitsURL(p.id)} download leftSection={<IconDownload size={14} />}>Download</Button>}
            </Row>
          ))}
        </RowList>
      )}
    </Page>
  );
}
