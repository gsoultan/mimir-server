import { Badge, Card, Group, SimpleGrid, Text, TextInput } from "@mantine/core";
import { IconPlayerPlay, IconPlus, IconSearch, IconUpload } from "@tabler/icons-react";
import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useDeferredValue, useState } from "react";

import { listArchitectures, listModels, whoAmI } from "../../../api/queries";
import { ButtonLink, AnchorLink } from "../../../components/links";
import { Page } from "../../../components/Page";
import { ago, bytes } from "../../../lib/format";

export const Route = createFileRoute("/_app/models/")({
  loader: ({ context }) => Promise.all([
    context.queryClient.ensureQueryData(listModels),
    context.queryClient.ensureQueryData(listArchitectures),
  ]),
  component: Models,
});

function Models() {
  const { data } = useSuspenseQuery(listModels);
  const { data: archs } = useSuspenseQuery(listArchitectures);
  const { data: me } = useSuspenseQuery(whoAmI);
  const canWrite = me.permissions.includes("models.write");
  const [query, setQuery] = useState("");
  const q = useDeferredValue(query).trim().toLowerCase();
  const title = (name: string) => archs.architectures.find((a) => a.name === name)?.title ?? name;
  const shown = data.models.filter((m) => !q || m.name.toLowerCase().includes(q) || m.description.toLowerCase().includes(q));

  return (
    <Page
      title="Models"
      intro="Everything this server can answer with."
      actions={canWrite &&
        <Group gap="sm">
          <ButtonLink variant="default" leftSection={<IconUpload size={18} />} to="/models/new">Upload a model file</ButtonLink>
          <ButtonLink to="/models/build" leftSection={<IconPlus size={18} />}>Build a model</ButtonLink>
        </Group>
      }
    >
      {data.models.length > 6 && (
        <TextInput
          placeholder="Find a model"
          leftSection={<IconSearch size={18} />}
          value={query}
          onChange={(e) => setQuery(e.currentTarget.value)}
          aria-label="Find a model"
          className="max-w-sm"
        />
      )}
      {data.models.length === 0 ? (
        <Card withBorder radius="lg" padding="xl" className="text-center">
          <Text fw={600}>No models yet</Text>
          <Text c="dimmed" mb="md">{canWrite ? "Build one and it shows up here. We walk you through it." : "Ask an editor or admin to share one with you."}</Text>
          {canWrite && <Group justify="center"><ButtonLink to="/models/build" leftSection={<IconPlus size={18} />}>Build a model</ButtonLink></Group>}
        </Card>
      ) : shown.length === 0 ? (
        <Text c="dimmed">No model matches "{query}".</Text>
      ) : (
        <SimpleGrid cols={{ base: 1, sm: 2, md: 3 }}>
          {shown.map((m) => (
            <Card key={m.id} withBorder radius="lg" padding="lg" className="flex flex-col">
              <Group justify="space-between" wrap="nowrap" mb={4}>
                <AnchorLink fw={700} truncate to="/models/$id" params={{ id: m.id }} c="inherit">{m.name}</AnchorLink>
                <Badge color="teal" variant="light">Ready</Badge>
              </Group>
              <Text size="sm" c="dimmed">{title(m.architecture)} · {bytes(m.sizeBytes)}</Text>
              {m.description && <Text size="sm" mt="xs" lineClamp={2}>{m.description}</Text>}
              <Text size="xs" c="dimmed" mt="xs">Added {ago(m.createTime)}</Text>
              <Group mt="md" gap="xs" className="mt-auto pt-3">
                <ButtonLink size="sm" leftSection={<IconPlayerPlay size={16} />} to="/try" search={{ model: m.id }}>Try it</ButtonLink>
                <ButtonLink size="sm" variant="default" to="/models/$id" params={{ id: m.id }}>Details</ButtonLink>
              </Group>
            </Card>
          ))}
        </SimpleGrid>
      )}
    </Page>
  );
}
