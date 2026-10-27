import { Button, Group, SimpleGrid, Stack, Text, Title } from "@mantine/core";
import { useDisclosure, useMediaQuery } from "@mantine/hooks";
import { IconArrowRight } from "@tabler/icons-react";
import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import type { ReactNode } from "react";

import { listTemplates } from "../../../../api/queries";
import { GoalCard, WayCard, ways, type WayId } from "../../../../components/Goals";
import { Page } from "../../../../components/Page";
import { goals, isTemplateKind, type Goal } from "../../../../lib/goals";

export const Route = createFileRoute("/_app/models/build/")({
  loader: ({ context }) => context.queryClient.ensureQueryData(listTemplates),
  component: Build,
});

// Build is where every new model starts: pick what it should do, or go
// straight to a way of building.
function Build() {
  const { data } = useSuspenseQuery(listTemplates);
  const [more, { toggle }] = useDisclosure(false);
  // On a phone the ways to build wait behind a button, below the goals.
  const phone = useMediaQuery("(max-width: 48em)");
  const how = (g: Goal) => {
    if (isTemplateKind(g.id)) {
      const n = data.templates.filter((t) => t.kind === g.id).length;
      return n === 1 ? "1 template" : `${n} templates`;
    }
    return g.learn ? "From a table, no code" : g.ready === "file" ? "Bring a model file" : "Guide and starter code";
  };

  return (
    <Page title="Build a model" intro="Tell us what you want to know. We show the easiest way to get there, step by step.">
      <Stack gap="sm">
        <Title order={2} fz="lg">1. What should the model do?</Title>
        <SimpleGrid cols={{ base: 2, md: 4 }} spacing={{ base: "sm", md: "md" }}>
          {goals.map((g) => (
            <Link key={g.id} to="/models/build/$goal" params={{ goal: g.id }} className="text-inherit no-underline">
              <GoalCard id={g.id} title={g.title} example={g.example} ready={g.ready} how={how(g)} />
            </Link>
          ))}
        </SimpleGrid>
      </Stack>
      <Button variant="default" hiddenFrom="sm" rightSection={<IconArrowRight size={16} className={more ? "rotate-90" : undefined} />} onClick={toggle}>
        Other ways to build
      </Button>
      {(!phone || more) && (
        <Stack gap="sm">
          <Group justify="space-between" align="flex-end">
            <Title order={2} fz="lg">Or go straight to a way of building</Title>
            <Text size="sm" c="dimmed" visibleFrom="sm">Not sure? Pick a goal above and we suggest one.</Text>
          </Group>
          <SimpleGrid cols={{ base: 1, md: 2 }}>
            {ways.map((w) => <WayLink key={w.id} id={w.id}><WayCard {...w} /></WayLink>)}
          </SimpleGrid>
        </Stack>
      )}
    </Page>
  );
}

function WayLink({ id, children }: { id: WayId; children: ReactNode }) {
  const cls = "text-inherit no-underline";
  switch (id) {
    case "template": return <Link to="/templates" className={cls}>{children}</Link>;
    case "data": return <Link to="/models/teach" className={cls}>{children}</Link>;
    case "file": return <Link to="/models/new" className={cls}>{children}</Link>;
    case "code": return <Link to="/models/build/code" className={cls}>{children}</Link>;
  }
}
