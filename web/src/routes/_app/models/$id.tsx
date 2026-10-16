import { useMutation } from "@connectrpc/connect-query";
import { Alert, Button, Card, Code, Group, Modal, SimpleGrid, Stack, Tabs, Text, Textarea, TextInput } from "@mantine/core";
import { useDisclosure } from "@mantine/hooks";
import { notifications } from "@mantine/notifications";
import { IconAlertCircle, IconPlayerPlay, IconTrash } from "@tabler/icons-react";
import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useState } from "react";

import { message } from "../../../api/client";
import { ModelService } from "../../../api/gen/mimir/v1/model_pb";
import { allModelQueries, getModel, listArchitectures, whoAmI } from "../../../api/queries";
import { ButtonLink } from "../../../components/links";
import { Page } from "../../../components/Page";
import { ago, bytes } from "../../../lib/format";

export const Route = createFileRoute("/_app/models/$id")({
  loader: ({ context, params }) => Promise.all([
    context.queryClient.ensureQueryData(getModel(params.id)),
    context.queryClient.ensureQueryData(listArchitectures),
  ]),
  errorComponent: ({ error }) => (
    <Alert color="red" icon={<IconAlertCircle />} className="mx-auto max-w-5xl">
      {message(error)} <Link to="/models">Back to models</Link>
    </Alert>
  ),
  component: ModelDetails,
});

function ModelDetails() {
  const { id } = Route.useParams();
  const { queryClient } = Route.useRouteContext();
  const navigate = useNavigate();
  const { data } = useSuspenseQuery(getModel(id));
  const { data: archs } = useSuspenseQuery(listArchitectures);
  const { data: me } = useSuspenseQuery(whoAmI);
  const canWrite = me.permissions.includes("models.write");
  const m = data.model!;
  const arch = archs.architectures.find((a) => a.name === m.architecture);
  const [name, setName] = useState(m.name);
  const [description, setDescription] = useState(m.description);
  const [confirming, confirm] = useDisclosure();

  const refresh = () => queryClient.invalidateQueries({ queryKey: allModelQueries });
  const update = useMutation(ModelService.method.updateModel, {
    onSuccess: async () => { await refresh(); notifications.show({ color: "teal", message: "Saved." }); },
  });
  const remove = useMutation(ModelService.method.deleteModel, {
    onSuccess: async () => {
      await navigate({ to: "/models" });
      queryClient.removeQueries({ queryKey: getModel(id).queryKey });
      await refresh();
      notifications.show({ message: `${m.name} was deleted.` });
    },
  });

  const shape = m.inputShape.length > 0 ? m.inputShape : arch?.inputShape ?? [];
  const input = shape.reduce((a, b) => a * b, 1n);
  const table = m.features.length > 0;
  const curl = `curl ${location.origin}/mimir.v1.PredictService/Predict \\
  -H 'Authorization: Bearer mimir_...' \\
  -H 'Content-Type: application/json' \\
  -d '{"modelId": "${m.id}", "input": {"values": [ /* ${input} numbers */ ]}}'`;
  const grpc = `grpcurl -plaintext -H 'Authorization: Bearer mimir_...' \\
  -d '{"model_id": "${m.id}", "input": {"values": [...]}}' \\
  ${location.host} mimir.v1.PredictService/Predict`;

  return (
    <Page
      title={m.name}
      intro={`${arch?.title ?? m.architecture} · ${bytes(m.sizeBytes)} · added ${ago(m.createTime)}${m.accuracy > 0 ? ` · gets ${Math.round(m.accuracy * 100)} of 100 right` : ""}${m.updateTime && m.createTime && m.updateTime.seconds - m.createTime.seconds > 60n ? ` · last learned ${ago(m.updateTime)}` : ""}`}
      actions={<ButtonLink to="/try" search={{ model: m.id }} leftSection={<IconPlayerPlay size={18} />}>Try it</ButtonLink>}
    >
      <SimpleGrid cols={{ base: 1, md: 2 }}>
        <Card withBorder radius="lg" padding="lg">
          <Stack>
            <TextInput label="Name" maxLength={100} value={name} onChange={(e) => setName(e.currentTarget.value)} readOnly={!canWrite} />
            <Textarea label="Description" maxLength={500} autosize minRows={2} value={description} onChange={(e) => setDescription(e.currentTarget.value)} readOnly={!canWrite} />
            {update.error && <Alert color="red" icon={<IconAlertCircle />}>{message(update.error)}</Alert>}
            {m.jobId && <Text size="sm" c="dimmed">Taught here. <Link to="/training/$id" params={{ id: m.jobId }}>See how it learned</Link>.</Text>}
            {canWrite && <Group justify="space-between">
              <Button variant="subtle" color="red" leftSection={<IconTrash size={16} />} onClick={confirm.open}>Delete model</Button>
              <Button
                loading={update.isPending}
                disabled={!name.trim() || (name === m.name && description === m.description)}
                onClick={() => update.mutate({ id, name, description })}
              >
                Save
              </Button>
            </Group>}
          </Stack>
        </Card>
        <Card withBorder radius="lg" padding="lg">
          <Text fw={700} mb={4}>Use it from your app</Text>
          <Text size="sm" c="dimmed" mb="sm">
            {table
              ? `Send ${input.toString()} numbers, in this order: ${m.features.join(", ")}. You get back the likeliest of: ${m.labels.join(", ")}.`
              : `Send ${input.toString()} numbers between 0 and 1; get back the likeliest answers.`} Make a key under Connect your apps.
          </Text>
          <Tabs defaultValue="http">
            <Tabs.List>
              <Tabs.Tab value="http">HTTP</Tabs.Tab>
              <Tabs.Tab value="grpc">gRPC</Tabs.Tab>
            </Tabs.List>
            <Tabs.Panel value="http" pt="sm"><Code block>{curl}</Code></Tabs.Panel>
            <Tabs.Panel value="grpc" pt="sm"><Code block>{grpc}</Code></Tabs.Panel>
          </Tabs>
        </Card>
      </SimpleGrid>

      <Modal opened={confirming} onClose={confirm.close} title={`Delete ${m.name}?`} centered>
        <Text mb="lg">Apps using this model will stop getting answers. This can't be undone.</Text>
        {remove.error && <Alert color="red" mb="md" icon={<IconAlertCircle />}>{message(remove.error)}</Alert>}
        <Group justify="flex-end">
          <Button variant="default" onClick={confirm.close}>Keep it</Button>
          <Button color="red" loading={remove.isPending} onClick={() => remove.mutate({ id })}>Delete</Button>
        </Group>
      </Modal>
    </Page>
  );
}
