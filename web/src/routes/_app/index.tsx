import { Anchor, Card, Group, Progress, SimpleGrid, Table, Text, ThemeIcon, Title } from "@mantine/core";
import { IconAlertTriangle, IconArrowRight, IconBooks, IconCircleCheck, IconLoader2, IconMessageCircle, IconPlayerPlay, IconPlugConnected, IconPlus, IconSparkles, IconUpload, IconX } from "@tabler/icons-react";
import { useQueries, useQuery, useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import type { ReactNode } from "react";

import type { Model } from "../../api/gen/mimir/v1/model_pb";
import { JobStatus } from "../../api/gen/mimir/v1/training_pb";
import { listArchitectures, listJobs, listModels, modelQuality, whoAmI } from "../../api/queries";
import { AnchorLink, ButtonLink } from "../../components/links";
import { Page } from "../../components/Page";
import { Row, RowList, StatusPill } from "../../components/ui";
import { ago } from "../../lib/format";
import { answerWord, rightWord } from "../../lib/models";
import { active, progress } from "../../lib/training";

export const Route = createFileRoute("/_app/")({
  loader: ({ context }) => Promise.all([
    context.queryClient.ensureQueryData(listModels),
    context.queryClient.ensureQueryData(listArchitectures),
  ]),
  component: Home,
});

const day = 86_400_000;

function greeting() {
  const h = new Date().getHours();
  return h < 5 ? "Good evening" : h < 12 ? "Good morning" : h < 18 ? "Good afternoon" : "Good evening";
}

function Home() {
  const { data } = useSuspenseQuery(listModels);
  const { data: archs } = useSuspenseQuery(listArchitectures);
  const { data: me } = useSuspenseQuery(whoAmI);
  const canWrite = me.permissions.includes("models.write");
  const models = data.models;
  const archOf = (m: Model) => archs.architectures.find((a) => a.name === m.architecture);
  const first = (me.user?.name ?? "").split(" ")[0] ?? "";

  // Runs, for editors: live ones and those that ended in the last day.
  const { data: jobs } = useQuery({ ...listJobs, enabled: canWrite, refetchInterval: (q) => (q.state.data?.jobs.some(active) ? 3000 : false) });
  // How each model does in use, to spot ones getting fewer answers right.
  const shown = models.slice(0, 12);
  const quality = useQueries({ queries: shown.map((m) => ({ ...modelQuality(m.id), staleTime: 60_000 })) });
  const qualityOf = (id: string) => quality[shown.findIndex((m) => m.id === id)]?.data;

  if (models.length === 0) return <FirstRun canWrite={canWrite} name={first} />;

  const recent = (t?: { seconds: bigint }) => !!t && Date.now() - Number(t.seconds) * 1000 < day;
  const attention: ReactNode[] = [];
  for (const m of shown) {
    const q = qualityOf(m.id);
    if (q?.dropped) {
      attention.push(
        <Row key={"drop" + m.id} tone="warn" icon={<IconAlertTriangle size={20} />}
          title={`${m.name} is getting fewer answers right`}
          detail={`${Math.round(q.weekAccuracy * 100)} of 100 right this week, against ${Math.round(m.accuracy * 100)} when it was taught. Newer examples would help.`}>
          <ButtonLink size="xs" variant="default" to="/models/$id" params={{ id: m.id }}>Look into it</ButtonLink>
        </Row>,
      );
    }
  }
  for (const j of jobs?.jobs ?? []) {
    if (active(j)) {
      attention.push(
        <Row key={j.id} tone="busy" icon={<IconLoader2 size={20} className="animate-spin [animation-duration:2s]" />}
          title={`${j.name} is ${j.status === JobStatus.QUEUED ? "waiting to start" : "learning"}`}
          detail={`Pass ${Math.min(j.epoch + 1, j.settings?.epochs ?? 1)} of ${j.settings?.epochs ?? 1} · started ${ago(j.createTime)}`}
          extra={<Progress value={progress(j)} color="violet" size="sm" mt={8} maw={380} animated aria-label="Progress" />}>
          <ButtonLink size="xs" variant="subtle" to="/training/$id" params={{ id: j.id }}>Watch</ButtonLink>
        </Row>,
      );
    } else if (j.status === JobStatus.SUCCEEDED && recent(j.endTime)) {
      attention.push(
        <Row key={j.id} tone="ok" icon={<IconCircleCheck size={20} />} title={`${j.name} finished learning`}
          detail={`Gets ${Math.round(j.accuracy * 100)} of 100 right · ${ago(j.endTime)}`}>
          <ButtonLink size="xs" variant="default" to="/try" search={{ model: j.modelId }}>Try it</ButtonLink>
        </Row>,
      );
    } else if (j.status === JobStatus.FAILED && recent(j.endTime ?? j.createTime)) {
      attention.push(
        <Row key={j.id} tone="bad" icon={<IconX size={20} />} title={`${j.name} didn't finish`} detail={j.error || "It stopped with an error."}>
          <ButtonLink size="xs" variant="default" to="/training/$id" params={{ id: j.id }}>See why</ButtonLink>
        </Row>,
      );
    }
  }
  const dropped = shown.filter((m) => qualityOf(m.id)?.dropped).length;
  const summary = `${models.length} ${models.length === 1 ? "model is" : "models are"} ready to answer.${dropped > 0 ? ` ${dropped === 1 ? "One needs" : `${dropped} need`} a look.` : ""}`;

  return (
    <Page
      title={`${greeting()}${first ? `, ${first}` : ""}`}
      intro={summary}
      actions={canWrite && <ButtonLink to="/models/build" leftSection={<IconPlus size={18} />}>Build a model</ButtonLink>}
    >
      {attention.length > 0 && <RowList>{attention.slice(0, 6)}</RowList>}

      <section>
        <Group justify="space-between" mb="sm">
          <Title order={2}>Your models</Title>
          {models.length > 6 && <Anchor component={Link} to="/models" fw={650} size="sm">See all {models.length} <IconArrowRight size={14} className="inline" /></Anchor>}
        </Group>
        <Card withBorder padding={0}>
          <Table highlightOnHover>
            <Table.Thead>
              <Table.Tr>
                <Table.Th>Model</Table.Th>
                <Table.Th visibleFrom="sm">What it answers</Table.Th>
                <Table.Th>Right</Table.Th>
                <Table.Th visibleFrom="md">Answers this week</Table.Th>
                <Table.Th visibleFrom="sm" />
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {models.slice(0, 6).map((m) => {
                const arch = archOf(m);
                const q = qualityOf(m.id);
                const right = rightWord(m, arch);
                return (
                  <Table.Tr key={m.id}>
                    <Table.Td>
                      <AnchorLink to="/models/$id" params={{ id: m.id }} c="inherit" fw={650}>{m.name}</AnchorLink>
                      <Text size="xs" c="dimmed" hiddenFrom="sm">{answerWord(arch, m)}</Text>
                    </Table.Td>
                    <Table.Td visibleFrom="sm" c="dimmed">{answerWord(arch, m)}</Table.Td>
                    <Table.Td>{right ? <StatusPill tone={q?.dropped ? "warn" : "ok"}>{right}</StatusPill> : <Text c="dimmed" size="sm">–</Text>}</Table.Td>
                    <Table.Td visibleFrom="md" c="dimmed">{q ? Number(q.weekAnswers).toLocaleString() : "–"}</Table.Td>
                    <Table.Td visibleFrom="sm" ta="right">
                      <ButtonLink size="xs" variant="subtle" leftSection={<IconPlayerPlay size={14} />} to="/try" search={{ model: m.id }}>Try</ButtonLink>
                    </Table.Td>
                  </Table.Tr>
                );
              })}
            </Table.Tbody>
          </Table>
        </Card>
      </section>

      <section>
        <Title order={2} mb="sm">Quick start</Title>
        <SimpleGrid cols={{ base: 1, sm: 3 }}>
          <QuickStart to="/try" icon={<IconMessageCircle size={20} />} title="Ask a model" text="Type a question or drop in a file and see the answer." />
          {canWrite
            ? <QuickStart to="/templates" icon={<IconBooks size={20} />} title="Start from a template" text="Real problems with example data you can try right away." />
            : <QuickStart to="/documents" icon={<IconBooks size={20} />} title="Ask your documents" text="Questions answered from your own files, with sources." />}
          <QuickStart to="/apps" icon={<IconPlugConnected size={20} />} title="Connect an app" text="Make a key and copy a ready request." />
        </SimpleGrid>
      </section>
    </Page>
  );
}

function QuickStart({ to, icon, title, text }: { to: "/try" | "/templates" | "/documents" | "/apps"; icon: ReactNode; title: string; text: string }) {
  return (
    <Card withBorder padding="lg" component={Link} to={to} className="transition-transform hover:-translate-y-0.5" style={{ color: "inherit", textDecoration: "none" }}>
      <ThemeIcon size={38} radius="md" variant="light" mb="sm">{icon}</ThemeIcon>
      <Text fw={650}>{title}</Text>
      <Text size="sm" c="dimmed" mt={2}>{text}</Text>
    </Card>
  );
}

// A server with no models yet: three ways to start.
function FirstRun({ canWrite, name }: { canWrite: boolean; name: string }) {
  if (!canWrite) {
    return (
      <Page title={`Welcome${name ? `, ${name}` : ""}`} intro="No models have been shared with you yet. Ask an editor or admin to give you access.">
        <Card withBorder padding="xl">
          <Text fw={650}>While you wait</Text>
          <Text c="dimmed" size="sm" mt={4}>You can already ask questions of your team's documents.</Text>
          <ButtonLink mt="md" variant="default" to="/documents">Ask your documents</ButtonLink>
        </Card>
      </Page>
    );
  }
  const ways = [
    { to: "/templates" as const, icon: <IconBooks size={20} />, title: "Start from a template", text: "Pick a real problem, bring data shaped like the example, and we teach it. You can try it with made-up data first.", cta: "Browse templates", primary: true },
    { to: "/models/new" as const, icon: <IconUpload size={20} />, title: "Add a model you have", text: "Upload a Mimir Model, ONNX or GGUF file. It takes a minute.", cta: "Upload a file", primary: false },
    { to: "/models/build" as const, icon: <IconSparkles size={20} />, title: "Not sure?", text: "Say what you want to know and we show the easiest way there, step by step.", cta: "Build a model", primary: false },
  ];
  return (
    <Page title={`Welcome to mimir${name ? `, ${name}` : ""}`} intro="mimir runs your models and lets your apps ask them questions. Start with one of these.">
      <SimpleGrid cols={{ base: 1, md: 3 }}>
        {ways.map((w) => (
          <Card key={w.to} withBorder padding="xl" className="flex flex-col">
            <ThemeIcon size={40} radius="md" variant="light" mb="md">{w.icon}</ThemeIcon>
            <Text fw={650} fz="lg">{w.title}</Text>
            <Text size="sm" c="dimmed" mt={4} className="flex-1">{w.text}</Text>
            <ButtonLink mt="lg" fullWidth variant={w.primary ? "filled" : "default"} to={w.to}>{w.cta}</ButtonLink>
          </Card>
        ))}
      </SimpleGrid>
    </Page>
  );
}
