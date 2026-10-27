import { useMutation } from "@connectrpc/connect-query";
import { Alert, Anchor, Button, Card, Code, CopyButton, Group, Loader, NumberInput, Select, SimpleGrid, Stack, Stepper, Tabs, Text, TextInput } from "@mantine/core";
import { IconAlertCircle, IconArrowLeft, IconCheck, IconCopy } from "@tabler/icons-react";
import { useQuery, useQueryClient, useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import { z } from "zod";

import { message } from "../../../../api/client";
import { ModelService } from "../../../../api/gen/mimir/v1/model_pb";
import { WorkerService } from "../../../../api/gen/mimir/v1/worker_pb";
import { allModelQueries, allWorkerQueries, listArchitectures, listWorkers, whoAmI } from "../../../../api/queries";
import { AnchorLink, ButtonLink } from "../../../../components/links";
import { Page } from "../../../../components/Page";
import { goalOf } from "../../../../lib/goals";
import { mainGo, setup, workerModule } from "../../../../lib/starter";

export const Route = createFileRoute("/_app/models/build/code")({
  // goal is the guide it came from; arch reopens a type described before.
  validateSearch: z.object({ goal: z.string().optional(), arch: z.string().optional() }),
  loader: ({ context }) => context.queryClient.ensureQueryData(listArchitectures),
  component: CodeGuide,
});

const inputs = [
  { value: "table", label: "A row of columns" },
  { value: "image", label: "A photo" },
  { value: "tensor", label: "A list of numbers" },
];
const outputs = [
  { value: "class", label: "One of a few groups" },
  { value: "number", label: "A number" },
  { value: "ranked", label: "A ranked list of items" },
];

// slug makes a name fit for code: lower case, digits and dashes.
const slug = (s: string) => s.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 40);

// CodeGuide walks a developer from an empty folder to a model of their own
// running on a worker. It moves on by itself when the worker connects.
function CodeGuide() {
  const search = Route.useSearch();
  const goal = search.goal ? goalOf(search.goal) : undefined;
  const { data: me } = useSuspenseQuery(whoAmI);
  const { data: archs } = useSuspenseQuery(listArchitectures);
  const canManage = me.permissions.includes("workers.manage");
  const before = archs.architectures.find((a) => a.custom && a.name === search.arch);
  // A goal mimir teaches itself has a built-in type of its name; one's own
  // code for it goes by another.
  const starter = goal?.learn ? `my-${goal.arch}` : goal?.arch;
  const [name, setName] = useState(before?.name ?? starter ?? "my-model");
  const [input, setInput] = useState(before?.input ?? (goal?.id === "find_in_photo" ? "image" : goal?.id === "forecast" ? "tensor" : "table"));
  const [output, setOutput] = useState(before?.output ?? (goal?.id === "recommend" || goal?.id === "best_action" ? "ranked" : goal?.id === "forecast" || goal?.id === "odd_one" ? "number" : "class"));
  const [items, setItems] = useState<number | string>(before?.items || 10);
  const [token, setToken] = useState<string>();
  const arch = slug(name) || "my-model";
  const saved = archs.architectures.find((a) => a.custom && a.name === arch);
  const dirty = !saved || saved.input !== input || saved.output !== output || (output === "ranked" && saved.items !== Number(items));

  const queryClient = useQueryClient();
  const workers = useQuery({ ...listWorkers, enabled: canManage && !!saved, refetchInterval: 3000 });
  const running = saved && workers.data?.workers.find((w) => w.online && w.architectures.includes(arch));
  const create = useMutation(WorkerService.method.createWorker, {
    onSuccess: async (res) => { await queryClient.invalidateQueries({ queryKey: allWorkerQueries }); setToken(res.token); },
  });
  const save = useMutation(ModelService.method.saveArchitecture, {
    onSuccess: async () => { await queryClient.invalidateQueries({ queryKey: allModelQueries }); },
  });
  const describe = () => save.mutate({
    name: arch,
    title: goal && starter === arch ? goal.title : arch,
    description: goal && starter === arch ? goal.intro : "",
    input, output, items: output === "ranked" ? Number(items) || 10 : 0,
  });

  const run = `MIMIR_WORKER_TOKEN=${token ?? "<your worker token>"} go run .`;
  const step = !saved ? 0 : running ? 4 : 3;
  const what = goal ? goal.example.toLowerCase().replace(/\?$/, "") : "the question it was written for";

  return (
    <Stack gap="xs">
      <div className="mx-auto w-full max-w-5xl">
        {goal
          ? <AnchorLink to="/models/build/$goal" params={{ goal: goal.id }} size="sm" c="teal.8" className="inline-flex items-center gap-1"><IconArrowLeft size={16} /> {goal.title}</AnchorLink>
          : <AnchorLink to="/models/build" size="sm" c="teal.8" className="inline-flex items-center gap-1"><IconArrowLeft size={16} /> Build a model</AnchorLink>}
      </div>
      <Page title="Write your own model" intro="For anything mimir can't teach by itself. You write the model in Go; mimir serves it like any other.">
        <SimpleGrid cols={{ base: 1, md: 3 }} spacing="lg">
          <Card withBorder radius="lg" padding="lg" className="md:col-span-2">
            <Stepper active={step} orientation="vertical" size="sm" iconSize={30}>
              <Stepper.Step label="Describe it" description={`${arch} · reads ${inputs.find((i) => i.value === input)!.label.toLowerCase()} · answers ${outputs.find((o) => o.value === output)!.label.toLowerCase()}`}>
                <Stack gap="sm" mt="md">
                  <Text fw={700} size="sm">Now: describe it</Text>
                  <Text size="sm" c="dimmed">Check the card on the right: the name your code runs it under, what people send and what it answers. Then save it, and the next steps open.</Text>
                  <Group><Button loading={save.isPending} onClick={describe}>Save {arch}</Button></Group>
                  {save.error && <Alert color="red" icon={<IconAlertCircle />}>{message(save.error)}</Alert>}
                </Stack>
              </Stepper.Step>
              <Stepper.Step label="Get the starting project" description={`Go 1.27 or newer: go get ${workerModule}`} />
              <Stepper.Step label="Write the model" description="Fill in Predict, and Train if you want to teach it here. The starting main.go is below." />
              <Stepper.Step label="Connect it" description={running ? `${running.name} connected` : "Make a token, then run it from the project's folder."}>
                {!running && (
                  <Stack gap="sm" mt="md">
                    <Text fw={700} size="sm">Now: connect it</Text>
                    {canManage ? (
                      token ? <CodeLine value={run} /> : (
                        <Group gap="sm">
                          <Button variant="default" loading={create.isPending} onClick={() => create.mutate({ name: arch })}>Make a worker token</Button>
                          <Text size="xs" c="dimmed">Shown once. It joins this console as a worker called {arch}.</Text>
                        </Group>
                      )
                    ) : (
                      <Text size="sm" c="dimmed">Ask an admin for a worker token, then run <Code>{run}</Code>.</Text>
                    )}
                    {create.error && <Alert color="red" icon={<IconAlertCircle />}>{message(create.error)}</Alert>}
                    {canManage && (
                      <Card radius="md" padding="sm" className="bg-[var(--mantine-color-default-hover)]">
                        <Group gap="sm" wrap="nowrap">
                          <Loader size="sm" />
                          <div>
                            <Text size="sm" fw={600}>Waiting for a worker that runs {arch}…</Text>
                            <Text size="xs" c="dimmed">This page moves on by itself when it connects. Most people take 5 to 10 minutes.</Text>
                          </div>
                        </Group>
                      </Card>
                    )}
                  </Stack>
                )}
              </Stepper.Step>
              <Stepper.Step label="Teach it, or upload what you trained" description="Then try it and give apps a key.">
                {running && (
                  <Stack gap="sm" mt="md">
                    <Text fw={700} size="sm">Now: teach it, or upload what you trained</Text>
                    <Alert color="teal" icon={<IconCheck size={18} />} p="sm">
                      <Text size="sm"><b>{running.name}</b> is connected and runs <Code>{arch}</Code>.{running.trains ? " It can also teach it." : ""}</Text>
                    </Alert>
                    <Group>
                      {running.trains && <ButtonLink to="/models/teach" search={{ arch, data: input === "image" ? "photos" : "table" }}>Teach it from my data</ButtonLink>}
                      <ButtonLink variant="default" to="/models/new" search={{ kind: arch }}>Upload what you trained</ButtonLink>
                    </Group>
                  </Stack>
                )}
              </Stepper.Step>
            </Stepper>
          </Card>
          <Stack>
            <Card withBorder radius="lg" padding="lg">
              <Text fw={700} mb={4}>{arch}</Text>
              <Text size="xs" c="dimmed" mb="sm">So the Try page and the API know what to ask for.</Text>
              <Stack gap="xs">
                <TextInput size="sm" label="Name in code" description="Lower case, digits and dashes" value={name} onChange={(e) => setName(e.currentTarget.value)} onBlur={() => setName(arch)} />
                <Select size="sm" label="People send" data={inputs} value={input} onChange={(v) => v && setInput(v)} allowDeselect={false} />
                <Select size="sm" label="It answers" data={outputs} value={output} onChange={(v) => v && setOutput(v)} allowDeselect={false} />
                {output === "ranked" && <NumberInput size="sm" label="How many items" min={1} max={100} value={items} onChange={setItems} />}
                {saved && (dirty
                  ? <Button variant="light" size="sm" loading={save.isPending} onClick={describe}>Save changes</Button>
                  : <Text size="xs" c="teal.8" fw={600} className="inline-flex items-center gap-1"><IconCheck size={14} /> Saved. Apps and the Try page use this.</Text>)}
                {saved && save.error && <Alert color="red" icon={<IconAlertCircle />}>{message(save.error)}</Alert>}
              </Stack>
            </Card>
            <Card withBorder radius="lg" padding="lg">
              <Text fw={700} mb={4}>Help</Text>
              <Stack gap={6}>
                <Anchor size="sm" href="https://pkg.go.dev/github.com/gsoultan/mimir-server/worker" target="_blank">Worker kit reference</Anchor>
                {(["recommend", "forecast", "odd_one", "find_in_photo"] as const).filter((g) => g !== goal?.id).map((g) => (
                  <AnchorLink key={g} size="sm" to="/models/build/$goal" params={{ goal: g }}>Guide: {goalOf(g)!.title.toLowerCase()}</AnchorLink>
                ))}
              </Stack>
            </Card>
          </Stack>
        </SimpleGrid>
        {!running && (
          <Card withBorder radius="lg" padding="lg">
            <Group justify="space-between" mb="xs">
              <Text fw={700}>main.go, a starting point</Text>
              <CopyButton value={mainGo(arch, location.origin, what)}>
                {({ copied, copy }) => <Button variant="default" size="xs" onClick={copy} leftSection={copied ? <IconCheck size={14} /> : <IconCopy size={14} />}>{copied ? "Copied" : "Copy"}</Button>}
              </CopyButton>
            </Group>
            <Tabs defaultValue="main">
              <Tabs.List>
                <Tabs.Tab value="main">main.go</Tabs.Tab>
                <Tabs.Tab value="run">Run it</Tabs.Tab>
              </Tabs.List>
              <Tabs.Panel value="main" pt="sm"><Code block className="overflow-x-auto">{mainGo(arch, location.origin, what)}</Code></Tabs.Panel>
              <Tabs.Panel value="run" pt="sm">
                <Stack gap="xs">
                  <Code block>{`${setup(arch)}\n# put main.go in the folder, then\n${run}`}</Code>
                  <Text size="xs" c="dimmed">
                    The worker makes its own key the first time it connects, so only that machine can use the token. On a console that isn't served over https, set Insecure in worker.Config to join from another machine on a network you trust.
                  </Text>
                </Stack>
              </Tabs.Panel>
            </Tabs>
          </Card>
        )}
      </Page>
    </Stack>
  );
}

function CodeLine({ value }: { value: string }) {
  return (
    <Group gap="xs" wrap="nowrap" align="flex-start" mt={6}>
      <Code block className="min-w-0 flex-1 overflow-x-auto">{value}</Code>
      <CopyButton value={value}>
        {({ copied, copy }) => <Button variant="default" size="sm" onClick={copy} leftSection={copied ? <IconCheck size={16} /> : <IconCopy size={16} />}>{copied ? "Copied" : "Copy"}</Button>}
      </CopyButton>
    </Group>
  );
}
