import { useMutation } from "@connectrpc/connect-query";
import { Alert, Button, Card, Group, Modal, SimpleGrid, Stack, Tabs, Text, Textarea, TextInput } from "@mantine/core";
import { useDisclosure } from "@mantine/hooks";
import { notifications } from "@mantine/notifications";
import { IconAlertCircle, IconListCheck, IconPlayerPlay, IconRoute, IconSparkles, IconTrash } from "@tabler/icons-react";
import { useQuery, useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { z } from "zod";

import { message } from "../../../api/client";
import { ModelService } from "../../../api/gen/mimir/v1/model_pb";
import { allModelQueries, getModel, listArchitectures, modelQuality, whoAmI } from "../../../api/queries";
import { ButtonLink } from "../../../components/links";
import { CodeBlock } from "../../../components/CodeBlock";
import { Page } from "../../../components/Page";
import { LanguageDefaults } from "../../../components/LanguageDefaults";
import { OnDevice } from "../../../components/OnDevice";
import { RunsOn } from "../../../components/Placement";
import { InUse } from "../../../components/InUse";
import { Versions } from "../../../components/Versions";
import { ago, bytes } from "../../../lib/format";
import { plansVisits, scores } from "../../../lib/scoring";
import { explains } from "../../../lib/explain";
import { ColumnsThatMatter } from "../../../components/Why";
import { StatTile, StatusPill } from "../../../components/ui";

const tabs = ["overview", "use", "versions", "runs", "settings"] as const;
type Tab = (typeof tabs)[number];

export const Route = createFileRoute("/_app/models/$id")({
  validateSearch: z.object({ tab: z.enum(tabs).optional() }),
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
  const search = Route.useSearch();
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
  const number = arch?.output === "number";
  const wantsPhoto = (arch?.input === "image" && shape.length === 0) || arch?.input === "photo_table" || arch?.input === "scene";
  const wantsSound = arch?.input === "sound";
  const columns = arch?.input === "image" || arch?.input === "scene" || wantsSound ? [] : m.columns.length > 0 ? m.columns.filter((c) => c.kind !== "photo") : m.features.map((name) => ({ name, kind: "number" }));
  const named = columns.length > 0 || wantsPhoto || wantsSound;
  const sample = (c: { kind: string; values?: string[] }) => c.kind === "number" ? "1.5" : c.kind === "yesno" ? "yes" : c.kind === "category" || c.kind === "key" ? c.values?.[0] ?? "a" : "some words";
  const fields = JSON.stringify(Object.fromEntries(columns.map((c) => [c.name, sample(c)])));
  const body = (id: string) => named
    ? `{"${id}": "${m.id}"${columns.length > 0 ? `, "fields": ${fields}` : ""}${wantsPhoto ? `, "photo": "<the photo, base64>"` : ""}${wantsSound ? `, "sound": "<the WAV file, base64>"` : ""}}`
    : `{"${id}": "${m.id}", "input": {"values": [ /* ${input} numbers */ ]}}`;
  const writes = arch?.output === "text";
  const method = writes ? "Generate" : "Predict";
  const request = (id: string) => writes ? `{"${id}": "${m.id}", "prompt": "Once upon a time", "${id === "modelId" ? "maxTokens" : "max_tokens"}": 128}` : body(id);
  const curl = `curl ${location.origin}/mimir.v1.PredictService/${method} \\
  -H 'Authorization: Bearer mimir_...' \\
  -H 'Content-Type: application/json' \\
  -d '${request("modelId")}'`;
  const grpc = `grpcurl -plaintext -H 'Authorization: Bearer mimir_...' \\
  -d '${request("model_id")}' \\
  ${location.host} mimir.v1.PredictService/${method}`;
  const answer = arch?.output === "boxes" ? "You get back boxes: each thing found, its score, and where it is as shares of the photo (left, top, width, height)." : number ? "You get back a number in value." : arch?.output === "numbers" ? `You get back a number for each of ${m.labels.join(", ")}, in that order, in output; value is the first.` : `You get back the likeliest of: ${m.labels.join(", ")}.`;

  const { data: quality } = useQuery(modelQuality(m.id));
  const tab = search.tab ?? "overview";
  const setTab = (t: string | null) => void navigate({ to: "/models/$id", params: { id }, search: { tab: (t ?? "overview") as Tab }, replace: true });
  const checked = Number(quality?.weekFeedback ?? 0n);

  return (
    <Page
      title={m.name}
      crumbs={[{ label: "Models", to: "/models" }, { label: m.name }]}
      badge={quality?.dropped || quality?.dataChanged ? <StatusPill tone="warn">Needs a look</StatusPill> : <StatusPill tone="ok">Ready</StatusPill>}
      intro={`${arch?.title ?? m.architecture}${m.baseModelName ? `, fine-tuned from ${m.baseModelName}` : ""} · ${bytes(m.sizeBytes)} · added ${ago(m.createTime)}${m.updateTime && m.createTime && m.updateTime.seconds - m.createTime.seconds > 60n ? ` · last learned ${ago(m.updateTime)}` : ""}`}
      actions={
        <Group gap="xs">
          {canWrite && scores(arch) && <ButtonLink variant="default" to="/scoring" search={{ model: m.id }} leftSection={<IconListCheck size={18} />}>Score a list</ButtonLink>}
          {canWrite && plansVisits(arch) && <ButtonLink variant="default" to="/visits/new" search={{ model: m.id }} leftSection={<IconRoute size={18} />}>Plan visits</ButtonLink>}
          <ButtonLink to="/try" search={{ model: m.id }} leftSection={<IconPlayerPlay size={18} />}>{writes ? "Chat with it" : "Try it"}</ButtonLink>
        </Group>
      }
    >
      <Tabs value={tab} onChange={setTab} keepMounted={false}>
        <Tabs.List mb="lg">
          <Tabs.Tab value="overview">Overview</Tabs.Tab>
          <Tabs.Tab value="use">Use from your app</Tabs.Tab>
          <Tabs.Tab value="versions">Versions</Tabs.Tab>
          {!writes && <Tabs.Tab value="runs">Where it runs</Tabs.Tab>}
          <Tabs.Tab value="settings">{canWrite ? "Settings" : "About"}</Tabs.Tab>
        </Tabs.List>

        <Tabs.Panel value="overview">
          <Stack gap="lg">
            {!writes && (
              <SimpleGrid cols={{ base: 2, md: 4 }}>
                <StatTile value={Number(quality?.weekAnswers ?? 0n).toLocaleString()} label="answers this week" />
                <StatTile value={checked > 0 ? `${Math.round((quality?.weekAccuracy ?? 0) * 100)} of 100` : "–"} label={`${number ? "close" : "right"} this week`} hint={checked > 0 ? `of ${checked.toLocaleString()} checked` : "once apps send answers back"} />
                <StatTile value={m.accuracy > 0 ? `${Math.round(m.accuracy * 100)} of 100` : "–"} label="when taught" hint={m.jobId ? <Link to="/training/$id" params={{ id: m.jobId }}>See how it learned</Link> : undefined} />
                <StatTile value={bytes(m.sizeBytes)} label="model size" hint={arch?.title} />
              </SimpleGrid>
            )}
            <SimpleGrid cols={{ base: 1, md: explains(m, arch) ? 2 : 1 }}>
              {!writes && <InUse model={m} number={number} />}
              {explains(m, arch) && <ColumnsThatMatter model={m} />}
            </SimpleGrid>
            {writes && m.language && <LanguageDefaults model={m} canWrite={canWrite} />}
            {writes && (
              <Card withBorder padding="lg">
                <Text fw={700} mb={4}>{m.baseModelId ? "A fine-tune" : "Teach it your writing"}</Text>
                <Text size="sm" c="dimmed" mb="sm">
                  {m.baseModelId
                    ? <>It runs on <Link to="/models/$id" params={{ id: m.baseModelId }}>{m.baseModelName || "its base model"}</Link> with small adapters it learned from your examples; this file holds only the adapters. Fine-tune it again to keep teaching it.</>
                    : "Fine-tune it on your documents or on example chats, so it knows what you know and answers your way. It learns small adapters next to the model, which stays as it is."}
                </Text>
                <Text size="sm" c="dimmed" mb="sm">It also makes embeddings for search: POST /v1/embeddings with its API name as model.</Text>
                {canWrite && <ButtonLink variant="default" to="/models/finetune" search={{ model: m.id }} leftSection={<IconSparkles size={16} />}>Fine-tune it</ButtonLink>}
              </Card>
            )}
          </Stack>
        </Tabs.Panel>

        <Tabs.Panel value="use">
          <Card withBorder padding="lg">
            <Text fw={700} mb={4}>Use it from your app</Text>
            <Text size="sm" c="dimmed" mb="sm">
              {writes
                ? "Send the start of a text as prompt; get back how the model carries on, in text."
                : named
                ? `Send ${[columns.length > 0 && "each column by name in fields", wantsPhoto && "the photo as a JPG, PNG or GIF"].filter(Boolean).join(", and ")}. ${answer}`
                : arch?.custom
                ? `Send the ${input.toString() === "1" ? "number" : `${input.toString()} numbers`} its code reads; get back ${number ? "a number in value" : arch.output === "ranked" ? `the top ${arch.items}, best first, in guesses` : "the likeliest answers"}.`
                : `Send ${input.toString()} numbers between 0 and 1; get back the likeliest answers.`} Your app needs a key: <Link to="/apps">make one under Connect your apps</Link>.
            </Text>
            <Tabs defaultValue="http">
              <Tabs.List>
                <Tabs.Tab value="http">HTTP</Tabs.Tab>
                <Tabs.Tab value="grpc">gRPC</Tabs.Tab>
              </Tabs.List>
              <Tabs.Panel value="http" pt="sm"><CodeBlock code={curl} /></Tabs.Panel>
              <Tabs.Panel value="grpc" pt="sm"><CodeBlock code={grpc} /></Tabs.Panel>
            </Tabs>
          </Card>
        </Tabs.Panel>

        <Tabs.Panel value="versions">
          <Versions model={m} number={number} canWrite={canWrite} trialable={!writes && arch?.output !== "boxes"} />
        </Tabs.Panel>

        {!writes && (
          <Tabs.Panel value="runs">
            <SimpleGrid cols={{ base: 1, md: 2 }}>
              <RunsOn model={m} canWrite={canWrite} />
              <OnDevice model={m} arch={arch} canWrite={canWrite} />
            </SimpleGrid>
          </Tabs.Panel>
        )}

        <Tabs.Panel value="settings">
          <Stack gap="lg" maw={640}>
            <Card withBorder padding="lg">
              <Stack>
                <TextInput label="Name" maxLength={100} value={name} onChange={(e) => setName(e.currentTarget.value)} readOnly={!canWrite} />
                <Textarea label="Description" description="Shown on the model list and in search." maxLength={500} autosize minRows={2} value={description} onChange={(e) => setDescription(e.currentTarget.value)} readOnly={!canWrite} />
                {update.error && <Alert color="red" icon={<IconAlertCircle />}>{message(update.error)}</Alert>}
                {canWrite && <Group justify="flex-end">
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
            {canWrite && (
              <Card withBorder padding="lg" style={{ borderColor: "var(--mantine-color-red-light-hover)" }}>
                <Group justify="space-between" wrap="wrap" gap="sm">
                  <div className="min-w-0 flex-1">
                    <Text fw={650}>Delete this model</Text>
                    <Text size="sm" c="dimmed">Apps using it stop getting answers. This can't be undone.</Text>
                  </div>
                  <Button variant="default" color="red" c="red" leftSection={<IconTrash size={16} />} onClick={confirm.open}>Delete model</Button>
                </Group>
              </Card>
            )}
          </Stack>
        </Tabs.Panel>
      </Tabs>

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

// CodeBlock is a request to copy, with a copy button in its corner.
