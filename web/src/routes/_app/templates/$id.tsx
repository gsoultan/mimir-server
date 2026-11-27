import { useMutation } from "@connectrpc/connect-query";
import { Alert, Badge, Button, Card, Code, CopyButton, Group, SimpleGrid, Stack, Table, Text, Title } from "@mantine/core";
import { IconAlertCircle, IconCheck, IconCopy, IconDownload, IconPhoto } from "@tabler/icons-react";
import { useQuery, useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, notFound } from "@tanstack/react-router";
import { useState } from "react";

import { message } from "../../../api/client";
import { TemplateService, type Template } from "../../../api/gen/mimir/v1/template_pb";
import { listArchitectures, listTemplates } from "../../../api/queries";
import { ButtonLink } from "../../../components/links";
import { Page } from "../../../components/Page";
import { categoryColor, columnKinds, download, kindColor, kindLabel } from "../../../lib/templates";
import { t } from "../../../i18n";

export const Route = createFileRoute("/_app/templates/$id")({
  loader: async ({ context, params }) => {
    const data = await context.queryClient.ensureQueryData(listTemplates);
    if (!data.templates.some((tm) => tm.id === params.id)) throw notFound();
    return data;
  },
  component: Detail,
});

function Detail() {
  const { id } = Route.useParams();
  const { data } = useSuspenseQuery(listTemplates);
  const tm = data.templates.find((x) => x.id === id)!;
  const cat = data.categories.find((c) => c.id === tm.category);
  const [bg, fg] = categoryColor(tm.category);
  const photos = tm.data === "photos";
  const example = useMutation(TemplateService.method.getTemplateExample, {
    onSuccess: (res) => download(res.fileName, res.contentType, res.data),
  });
  const unit = photos ? t("photos per answer") : t("rows");
  const algo = useQuery(listArchitectures).data?.architectures.find((a) => a.name === tm.architecture);

  return (
    <Page
      title={tm.name}
      intro={tm.question}
      crumbs={[{ label: t("Templates"), to: "/templates" }, { label: tm.name }]}
      badge={<Badge variant="light" color={kindColor(tm.kind)}>{kindLabel(tm.kind)}</Badge>}
      actions={<ButtonLink to="/models/teach" search={{ template: tm.id }}>{t("Use this template")}</ButtonLink>}
    >
      <Group gap={6} mt={-8}>
        <Badge style={{ background: bg, color: fg }}>{cat?.label}</Badge>
      </Group>

      <div className="grid gap-6 lg:grid-cols-[minmax(0,1fr)_320px]">
        <Stack gap="lg">
          <Card withBorder radius="lg" padding="lg">
            <SimpleGrid cols={{ base: 1, sm: 3 }}>
              <Fact label={t("The model answers")} value={tm.predicts} />
              <Fact label={photos ? t("One photo is") : t("One row is")} value={tm.rowMeans} />
              <Fact label={t("Usually found in")} value={tm.source} />
            </SimpleGrid>
          </Card>

          {photos ? <PhotoNeeds t={tm} onExample={() => example.mutate({ templateId: tm.id })} loading={example.isPending} /> : <TableNeeds t={tm} onExample={() => example.mutate({ templateId: tm.id })} loading={example.isPending} />}
          {example.error && <Alert color="red" icon={<IconAlertCircle />}>{message(example.error)}</Alert>}
        </Stack>

        <Stack gap="lg">
          <Card withBorder radius="lg" padding="lg">
            <Title order={2} fz="lg">{t("How much data?")}</Title>
            <Stack gap="xs" mt="sm">
              {[
                [tm.rowsMin, t("{unit} to start", { unit }), 34],
                [tm.rowsGood, t("{unit}, good", { unit }), 67],
                [tm.rowsGreat, t("{unit}, best", { unit }), 100],
              ].map(([n, what, w]) => (
                <div key={String(what)}>
                  <Group justify="space-between" gap={4}><Text fw={700}>{Number(n).toLocaleString()}</Text><Text size="sm" c="dimmed">{what}</Text></Group>
                  <div className="mt-1 h-1.5 rounded-full bg-[var(--mantine-color-teal-light)]"><div className="h-1.5 rounded-full bg-[var(--mantine-color-teal-6)]" style={{ width: `${w}%` }} /></div>
                </div>
              ))}
            </Stack>
            <Text size="sm" c="dimmed" mt="sm">{tm.balance}</Text>
          </Card>
          {algo && (
            <Card withBorder radius="lg" padding="lg">
              <Title order={2} fz="lg">{t("How it learns")}</Title>
              <Text fw={600} size="sm" mt="sm">{algo.title}</Text>
              {algo.algorithm && <Text size="sm" mt={4}>{algo.algorithm}</Text>}
              <Text size="sm" c="dimmed" mt="xs">{algo.description}</Text>
              <Text size="xs" c="dimmed" mt="xs">{t("You can pick another model type when you teach.")}</Text>
            </Card>
          )}
          <Card withBorder radius="lg" padding="lg">
            <Title order={2} fz="lg">{t("Ready to try it?")}</Title>
            <Stack gap="sm" mt="sm">
              <ButtonLink to="/models/teach" search={{ template: tm.id }} size="md">{t("Use this template")}</ButtonLink>
              <ButtonLink to="/models/teach" search={{ template: tm.id, example: true }} variant="default" size="md">{t("Try it with the example data first")}</ButtonLink>
              <Text size="xs" c="dimmed">{t("Nothing is shared. Your data stays on this server.")}</Text>
            </Stack>
          </Card>
          <Card withBorder radius="lg" padding="lg">
            <Text fw={700}>{t("Tips for this template")}</Text>
            <Stack gap={6} mt="xs">
              {tm.tips.map((tip) => (
                <Group key={tip} wrap="nowrap" align="flex-start" gap="xs">
                  <span aria-hidden="true" className="mt-2 size-1.5 shrink-0 rounded-full bg-[var(--mantine-color-teal-6)]" />
                  <Text size="sm">{tip}</Text>
                </Group>
              ))}
            </Stack>
          </Card>
        </Stack>
      </div>
    </Page>
  );
}

function Fact({ label, value }: { label: string; value: string }) {
  return <div><Text size="xs" c="dimmed" fw={600} tt="uppercase">{label}</Text><Text size="sm" mt={2}>{value}</Text></div>;
}

function Head({ title, hint, children }: { title: string; hint: string; children?: React.ReactNode }) {
  return (
    <Group justify="space-between" align="flex-start" gap="sm">
      <div className="max-w-2xl"><Title order={2} fz="lg">{title}</Title><Text size="sm" c="dimmed">{hint}</Text></div>
      {children}
    </Group>
  );
}

const tones = ["#DDE5E1", "#E8E2D6", "#D9E0EA", "#E6DCE8"];

function PhotoNeeds({ t: tm, onExample, loading }: { t: Template; onExample: () => void; loading: boolean }) {
  const tree = "my-photos/\n" + tm.folders.map((f, i) => `${i === tm.folders.length - 1 ? "└── " : "├── "}${f.name}/`).join("\n");
  return (
    <>
      <Card withBorder radius="lg" padding="lg">
        <Head title={t("The photos you need")} hint={t("Put the photos in one folder per answer. The folder name is the answer. Then zip the top folder, or drop the whole folder in.")}>
          <Button variant="default" leftSection={<IconDownload size={18} />} loading={loading} onClick={onExample}>{t("Download example photos (ZIP)")}</Button>
        </Head>
        <div className="mt-4 grid gap-4 sm:grid-cols-[180px_minmax(0,1fr)]">
          <pre className="m-0 rounded-lg bg-[var(--mantine-color-default-hover)] p-3 font-mono text-sm">{tree}</pre>
          <Stack gap="sm">
            {tm.folders.map((f, i) => (
              <Group key={f.name} justify="space-between" wrap="nowrap" gap="sm">
                <div className="min-w-0"><Text fw={700} ff="monospace" size="sm">{f.name}/</Text><Text size="sm" c="dimmed">{f.meaning}</Text></div>
                <Group gap={6} wrap="nowrap" aria-hidden="true" visibleFrom="xs">
                  {[0, 1, 2, 3].map((k) => (
                    <div key={k} className="flex size-12 items-center justify-center rounded-lg text-[#6B7773]" style={{ background: tones[(i + k) % 4] }}><IconPhoto size={18} /></div>
                  ))}
                </Group>
              </Group>
            ))}
          </Stack>
        </div>
        <Text size="xs" c="dimmed" mt="sm">{t("Grey boxes stand for the example photos. JPG, PNG and GIF work, any size; we resize them.")}</Text>
      </Card>
      <Card withBorder radius="lg" padding="lg">
        <Title order={2} fz="lg">{t("How to take good photos")}</Title>
        <SimpleGrid cols={{ base: 1, sm: 2 }} mt="sm">
          {[
            [t("Same set-up every time"), t("Same camera, distance, angle and light as you'll use later.")],
            [t("One thing per photo"), t("Fill the frame with the item. Crop out hands, tables and other parts.")],
            [t("Include the hard cases"), t("Small faults and borderline items teach the most.")],
            [t("Sort carefully"), t("A photo in the wrong folder teaches the wrong answer.")],
          ].map(([a, b]) => <div key={a}><Text fw={600} size="sm">{a}</Text><Text size="sm" c="dimmed">{b}</Text></div>)}
        </SimpleGrid>
      </Card>
    </>
  );
}

function TableNeeds({ t: tm, onExample, loading }: { t: Template; onExample: () => void; loading: boolean }) {
  const [sql, setSql] = useState(false);
  const last = tm.columns.length - 1;
  const photo = tm.columns.find((c) => c.kind === "photo");
  const pack = `my-data.zip\n├── data.csv\n└── photos/\n    ├── ${photo?.examples[0]?.replace("photos/", "") ?? ""}\n    ├── ${photo?.examples[1]?.replace("photos/", "") ?? ""}\n    └── ...`;
  const header = tm.columns.map((c) => c.name).join(",");
  return (
    <>
      <Card withBorder radius="lg" padding="lg">
        <Head title={t("The data you need")} hint={t("One column per line below. Names can differ in your file; we help you match them later. The highlighted column is the answer the model learns to give. Optional columns and any others you have make answers more specific.")} />
        <Table.ScrollContainer minWidth={560} mt="sm">
          <Table verticalSpacing="sm">
            <Table.Thead><Table.Tr><Table.Th>{t("Column")}</Table.Th><Table.Th>{t("What it means")}</Table.Th><Table.Th>{t("Kind of value")}</Table.Th><Table.Th>{t("Example")}</Table.Th></Table.Tr></Table.Thead>
            <Table.Tbody>
              {tm.columns.map((c, i) => (
                <Table.Tr key={c.name} bg={i === last ? "var(--mantine-color-teal-light)" : undefined}>
                  <Table.Td><Text ff="monospace" size="sm" fw={600}>{c.name}</Text>{i === last && <Text size="xs" c="teal.8" fw={700}>{t("The answer")}</Text>}{c.optional && <Text size="xs" c="dimmed">{t("Optional, makes answers more specific")}</Text>}</Table.Td>
                  <Table.Td><Text size="sm">{c.meaning}</Text></Table.Td>
                  <Table.Td><Badge variant="light" color={columnKinds[c.kind]?.color} radius="xl" className="shrink-0" styles={{ root: { overflow: "visible" }, label: { overflow: "visible" } }}>{columnKinds[c.kind]?.label ?? c.kind}</Badge></Table.Td>
                  <Table.Td><Text size="sm" ff="monospace">{c.examples[0]}</Text></Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>
        <Group gap="md" mt="sm">
          {Object.entries(columnKinds).filter(([k]) => tm.columns.some((c) => c.kind === k)).map(([k, v]) => (
            <Text key={k} size="xs" c="dimmed"><b>{v.label}</b> {v.hint}</Text>
          ))}
        </Group>
      </Card>

      {tm.data === "photo_table" && (
        <Card withBorder radius="lg" padding="lg">
          <Head title={t("How to pack photos with details")} hint={t("Put the table and a photos folder in one ZIP. Each row names its photo in the photo column.")} />
          <div className="mt-4 grid gap-4 sm:grid-cols-[220px_minmax(0,1fr)]">
            <pre className="m-0 rounded-lg bg-[var(--mantine-color-default-hover)] p-3 font-mono text-sm">{pack}</pre>
            <Stack gap="sm">
              <div><Text fw={600} size="sm">{t("1. Make the table")}</Text><Text size="sm" c="dimmed">{t("Same columns as above, saved as data.csv. In the")}{" "}{photo?.name}{" "}{t("column, write the photo's path, like")}{" "}{photo?.examples[0]}.</Text></div>
              <div><Text fw={600} size="sm">{t("2. Add the photos folder")}</Text><Text size="sm" c="dimmed">{t("Next to the table. One photo per row; file names must match exactly.")}</Text></div>
              <div><Text fw={600} size="sm">{t("3. Zip both and upload")}</Text><Text size="sm" c="dimmed">{t("Rows whose photo is missing are left out, and we list them for you.")}</Text></div>
            </Stack>
          </div>
        </Card>
      )}

      {tm.columns.some((c) => c.kind === "text") && (
        <Alert color="cyan" variant="light" title={t("About the text columns")}>{t("Put each piece of text in one cell, as people wrote it. No need to clean it up. Very long texts are read up to the first 500 words. Remove names and phone numbers if the text is about people.")}</Alert>
      )}

      <Card withBorder radius="lg" padding="lg">
        <Head title={t("What your file should look like")} hint={t("The first rows of the example file. A header row on top, then one row per example.")}>
          <Group gap="xs">
            <Button variant="default" leftSection={<IconDownload size={18} />} loading={loading} onClick={onExample}>{tm.data === "photo_table" ? t("Download example ZIP") : t("Download example CSV")}</Button>
            <CopyButton value={header}>
              {({ copied, copy }) => <Button variant="subtle" leftSection={copied ? <IconCheck size={18} /> : <IconCopy size={18} />} onClick={copy}>{copied ? t("Copied") : t("Copy header row")}</Button>}
            </CopyButton>
          </Group>
        </Head>
        <Table.ScrollContainer minWidth={560} mt="sm">
          <Table withTableBorder fz="sm">
            <Table.Thead><Table.Tr>{tm.columns.map((c, i) => <Table.Th key={c.name} ff="monospace" bg={i === last ? "var(--mantine-color-teal-light)" : undefined}>{c.name}</Table.Th>)}</Table.Tr></Table.Thead>
            <Table.Tbody>
              {[0, 1, 2].map((r) => (
                <Table.Tr key={r}>{tm.columns.map((c, i) => <Table.Td key={c.name} fw={i === last ? 600 : undefined} bg={i === last ? "var(--mantine-color-teal-light)" : undefined}>{c.examples[r]}</Table.Td>)}</Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>
        <Text size="xs" c="dimmed" mt="sm">{t("The example file has")}{" "}{tm.data === "photo_table" ? t("600 rows with photos") : t("2,000 rows")}{t(", made up but enough to teach a working model, so you can see how it behaves before using your own data.")}</Text>
      </Card>

      {tm.query && tm.data !== "photo_table" && (
        <Card withBorder radius="lg" padding="lg">
          <Head title={t("Data in a database?")} hint={t("A starting query. Change the table and column names to match yours; we test it before anything is saved.")}>
            <Button variant="default" onClick={() => setSql(!sql)}>{sql ? t("Hide the query") : t("Show a starting query")}</Button>
          </Head>
          {sql && <Code block mt="sm">{tm.query}</Code>}
        </Card>
      )}
    </>
  );
}
