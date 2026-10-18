import { Card, Group, SimpleGrid, Text, ThemeIcon, Title } from "@mantine/core";
import { IconPlayerPlay, IconSchool, IconUpload } from "@tabler/icons-react";
import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";

import { listModels, whoAmI } from "../../api/queries";
import { ButtonLink } from "../../components/links";
import { Page } from "../../components/Page";
import { ago } from "../../lib/format";

export const Route = createFileRoute("/_app/")({
  loader: ({ context }) => context.queryClient.ensureQueryData(listModels),
  component: Home,
});

function Home() {
  const { data } = useSuspenseQuery(listModels);
  const { data: me } = useSuspenseQuery(whoAmI);
  const canWrite = me.permissions.includes("models.write");
  const models = data.models;

  if (models.length === 0 && !canWrite) {
    return (
      <Page title="Welcome to mimir" intro="No models have been shared with you yet. Ask an editor or admin to give you access.">
        <></>
      </Page>
    );
  }
  if (models.length === 0) {
    return (
      <Page title="Welcome to mimir" intro="mimir runs your models and lets your apps ask them questions.">
        <SimpleGrid cols={{ base: 1, sm: 2 }}>
          <Card withBorder radius="lg" padding="xl">
            <ThemeIcon size={44} radius="md" variant="light"><IconUpload /></ThemeIcon>
            <Title order={3} mt="md">Add a model you already have</Title>
            <Text c="dimmed" mt={4} mb="lg">Upload a .mim file saved from mimir. It takes a minute.</Text>
            <ButtonLink to="/models/new">Add a model</ButtonLink>
          </Card>
          <Card withBorder radius="lg" padding="xl">
            <ThemeIcon size={44} radius="md" variant="light" color="gray"><IconSchool /></ThemeIcon>
            <Title order={3} mt="md">Teach a new model</Title>
            <Text c="dimmed" mt={4} mb="lg">Upload a table of examples, or use the built-in handwritten digits, and watch it learn.</Text>
            <ButtonLink variant="default" to="/models/teach">Teach a new model</ButtonLink>
          </Card>
        </SimpleGrid>
      </Page>
    );
  }

  return (
    <Page
      title="Home"
      intro={`You have ${models.length} ${models.length === 1 ? "model" : "models"} ready to answer.`}
      actions={canWrite && <ButtonLink to="/models/new" leftSection={<IconUpload size={18} />}>Add a model</ButtonLink>}
    >
      <Title order={2} fz="lg">Recently added</Title>
      <SimpleGrid cols={{ base: 1, sm: 2, md: 3 }}>
        {models.slice(0, 6).map((m) => (
          <Card key={m.id} withBorder radius="lg" padding="lg">
            <Text fw={700} truncate>{m.name}</Text>
            <Text size="sm" c="dimmed">Added {ago(m.createTime)}</Text>
            <Group mt="md" gap="xs">
              <ButtonLink size="sm" leftSection={<IconPlayerPlay size={16} />} to="/try" search={{ model: m.id }}>Try it</ButtonLink>
              <ButtonLink size="sm" variant="default" to="/models/$id" params={{ id: m.id }}>Details</ButtonLink>
            </Group>
          </Card>
        ))}
      </SimpleGrid>
    </Page>
  );
}
