import { Alert, Badge, Button, Card, Group, SegmentedControl, SimpleGrid, Stack, Stepper, Table, Text, Title } from "@mantine/core";
import { useMediaQuery } from "@mantine/hooks";
import { IconArrowLeft, IconArrowRight, IconDownload } from "@tabler/icons-react";
import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, Link, notFound } from "@tanstack/react-router";
import { z } from "zod";

import { listTemplates } from "../../../../api/queries";
import { PathOption, Stat } from "../../../../components/Goals";
import { AnchorLink, ButtonLink } from "../../../../components/links";
import { Page } from "../../../../components/Page";
import { csv, goalOf, isTemplateKind, needs, ownData, shapes, type Goal } from "../../../../lib/goals";
import { download, type DataType } from "../../../../lib/templates";

export const Route = createFileRoute("/_app/models/build/$goal")({
  validateSearch: z.object({
    shape: z.enum(["table", "text", "photos", "photo_table"]).optional(),
    labelled: z.boolean().optional(),
  }),
  loader: ({ context, params }) => {
    if (!goalOf(params.goal)) throw notFound();
    return context.queryClient.ensureQueryData(listTemplates);
  },
  component: GoalGuide,
});

function Back() {
  return (
    <div className="mx-auto w-full max-w-5xl">
      <AnchorLink to="/models/build" size="sm" c="teal.8" className="inline-flex items-center gap-1"><IconArrowLeft size={16} /> Build a model</AnchorLink>
    </div>
  );
}

function GoalGuide() {
  const { goal: id } = Route.useParams();
  const goal = goalOf(id)!;
  return (
    <Stack gap="xs">
      <Back />
      {isTemplateKind(goal.id) ? <NoCode goal={goal} kind={goal.id} /> : goal.ready === "file" ? <Write goal={goal} /> : <Code goal={goal} />}
    </Stack>
  );
}

// NoCode asks what one example looks like and whether past ones have
// answers, then ranks the ways to build it.
function NoCode({ goal, kind }: { goal: Goal; kind: "yes_no" | "group" | "number" }) {
  const { data } = useSuspenseQuery(listTemplates);
  const s = Route.useSearch();
  const navigate = Route.useNavigate();
  const shape: DataType = s.shape ?? "table";
  const labelled = s.labelled ?? true;
  const phone = useMediaQuery("(max-width: 48em)");
  const set = (patch: { shape?: DataType; labelled?: boolean }) => void navigate({ search: (cur) => ({ ...cur, ...patch }), replace: true });
  const matching = data.templates.filter((t) => t.kind === kind && t.data === shape);
  const n = needs(shape, kind);
  const shapeLabel = shapes.find((x) => x.value === shape)!.label.toLowerCase();
  const answers = kind === "group" ? "groups" : kind === "number" ? "numbers" : "answers";

  const template = (
    <PathOption
      key="template"
      best={matching.length > 0}
      title="Start from a template"
      hint={matching.length > 0
        ? `${matching.length === 1 ? "This template answers" : `These ${matching.length} templates answer`} like this from ${shapeLabel}. Pick the closest; names and ${answers} can differ.`
        : `No template answers like this from ${shapeLabel} yet. Others may still be close.`}
      action={<ButtonLink variant={matching.length > 0 ? "light" : "default"} to="/templates" search={matching.length > 0 ? { kind, data: shape } : { kind }}>
        {matching.length > 0 ? `See ${matching.length === 1 ? "it" : `all ${matching.length}`}` : "Browse templates"}
      </ButtonLink>}
    >
      {matching.length > 0 && (
        <Group gap="xs" mt="sm">
          {matching.slice(0, 6).map((t) => (
            <Link key={t.id} to="/templates/$id" params={{ id: t.id }} className="no-underline">
              <Badge variant="outline" color="gray" size="lg" tt="none" fw={500} className="cursor-pointer">{t.name}</Badge>
            </Link>
          ))}
          {matching.length > 6 && <Text size="sm" c="dimmed">and {matching.length - 6} more</Text>}
        </Group>
      )}
    </PathOption>
  );
  const own = (
    <PathOption
      key="own"
      best={matching.length === 0}
      title={`Start from your own ${shape === "photos" ? "photos" : shape === "text" ? "texts" : "data"}`}
      hint={ownData[shape]}
      action={<ButtonLink variant={matching.length === 0 ? "filled" : "default"} to="/models/teach" search={{ data: shape }}>Start</ButtonLink>}
    >
      <SimpleGrid cols={{ base: 1, sm: 3 }} mt="sm" spacing="xs">
        <Stat k="You'll want" v={n.amount} />
        <Stat k="Takes about" v={n.time} />
        <Stat k="You'll get" v={n.answer} />
      </SimpleGrid>
    </PathOption>
  );

  return (
    <Page title={goal.title} intro={goal.intro}>
      <Card withBorder radius="lg" padding="md">
        <Text fw={700} mb="xs">Two quick questions so we can point you the right way</Text>
        <SimpleGrid cols={{ base: 1, md: 2 }}>
          <Stack gap={4}>
            <Text size="sm" fw={500}>What does one example look like?</Text>
            <SegmentedControl data={shapes} value={shape} onChange={(v) => set({ shape: v as DataType })} orientation={phone ? "vertical" : "horizontal"} fullWidth={phone} />
          </Stack>
          <Stack gap={4}>
            <Text size="sm" fw={500}>Do you already have answers for past examples?</Text>
            <SegmentedControl data={[{ value: "yes", label: "Yes, labelled" }, { value: "no", label: "Not yet" }]} value={labelled ? "yes" : "no"} onChange={(v) => set({ labelled: v === "yes" })} className="self-start" />
          </Stack>
        </SimpleGrid>
      </Card>
      {!labelled && (
        <Alert color="yellow" variant="light" title="It learns from past answers">
          <Text size="sm">
            A model can't learn {answers} it has never seen. Write down the answer for {kind === "group" ? "about 100 examples per group" : "a few hundred past examples"} first, in a column next to each one{shape === "photos" ? ", or sort the photos into one folder per group" : ""}.
            {" "}To find unusual examples without answers, see <AnchorLink to="/models/build/$goal" params={{ goal: "odd_one" }} size="sm">Spot the odd one</AnchorLink>.
          </Text>
        </Alert>
      )}
      <Title order={2} fz="lg">Ways to build it</Title>
      {matching.length > 0 ? [template, own] : [own, template]}
      <PathOption
        title="Bring a trained model"
        hint="Trained it in PyTorch or elsewhere? Export it to ONNX and upload it."
        action={<ButtonLink variant="default" to="/models/new" search={{ kind: "mim" }}>Upload</ButtonLink>}
      />
    </Page>
  );
}

// Write is the guide for language models, which are brought, not taught.
function Write({ goal }: { goal: Goal }) {
  return (
    <Page title={goal.title} intro={goal.intro}>
      <PathOption
        best
        title="Bring a language model"
        hint="Upload a GGUF file. It runs here, chats in Try a model, and answers apps through the OpenAI-compatible API."
        action={<ButtonLink to="/models/new" search={{ kind: "language-model" }}>Upload a GGUF file</ButtonLink>}
      >
        <SimpleGrid cols={{ base: 1, sm: 3 }} mt="sm" spacing="xs">
          <Stat k="Works with" v="Llama, TinyLlama, Mistral, SmolLM, Qwen 2" />
          <Stat k="Pick the file ending" v="Q4_K_M or Q8_0" />
          <Stat k="A good start" v="1 to 3 billion parameters" />
        </SimpleGrid>
      </PathOption>
      <Card withBorder radius="lg" padding="lg">
        <Text fw={700} mb={4}>Make it yours without retraining</Text>
        <Text size="sm" c="dimmed">
          Give it instructions on its page: who it is, how to talk and what to stick to. Every chat and app gets them unless it sends its own.
          Teaching a language model on your own texts isn't something this server does yet.
        </Text>
      </Card>
      <Card withBorder radius="lg" padding="lg">
        <Group justify="space-between" wrap="wrap" gap="sm">
          <div>
            <Text fw={700}>Already using OpenAI in an app?</Text>
            <Text size="sm" c="dimmed">Point it here instead. Same requests, a model that runs on your own server.</Text>
          </div>
          <ButtonLink variant="default" to="/apps" rightSection={<IconArrowRight size={16} />}>Connect your apps</ButtonLink>
        </Group>
      </Card>
    </Page>
  );
}

// Code is the guide for goals mimir can't teach by itself yet.
function Code({ goal }: { goal: Goal }) {
  const sample = goal.sample!;
  return (
    <Page title={goal.title} intro={goal.intro}>
      <Alert color="grape" variant="light" title="mimir can't teach this one by itself yet">
        You can still run it here. A developer writes the model in Go and connects it as a worker. mimir then handles the rest: keys, limits, the Try page, usage and retraining on new data.
      </Alert>
      <SimpleGrid cols={{ base: 1, md: 3 }}>
        <Card withBorder radius="lg" padding="lg" className="md:col-span-2">
          <Text fw={700} mb="sm">What it takes</Text>
          <Stepper active={-1} orientation="vertical" size="sm" iconSize={30}>
            {goal.steps!.map((st) => <Stepper.Step key={st.label} label={st.label} description={st.hint} />)}
          </Stepper>
        </Card>
        <Stack>
          <Card withBorder radius="lg" padding="lg">
            <Text fw={700} mb={4}>Example data</Text>
            <div className="overflow-x-auto">
              <Table fz="xs" verticalSpacing={4}>
                <Table.Thead><Table.Tr>{sample.columns.map((c) => <Table.Th key={c}>{c}</Table.Th>)}</Table.Tr></Table.Thead>
                <Table.Tbody>
                  {sample.rows.slice(0, 3).map((r, i) => <Table.Tr key={i}>{r.map((v, j) => <Table.Td key={j}>{v}</Table.Td>)}</Table.Tr>)}
                </Table.Tbody>
              </Table>
            </div>
            <Button variant="default" size="sm" mt="sm" leftSection={<IconDownload size={16} />} onClick={() => download(sample.name, "text/csv", csv(sample))}>
              Download example CSV
            </Button>
          </Card>
          <ButtonLink size="lg" to="/models/build/code" search={{ goal: goal.id }} rightSection={<IconArrowRight size={18} />}>Start: write your own model</ButtonLink>
          <Text size="xs" c="dimmed">Not a developer? Send this page to one. Everything they need is on the next screen.</Text>
        </Stack>
      </SimpleGrid>
    </Page>
  );
}
