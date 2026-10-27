import { Badge, Button, Card, Group, SimpleGrid, Stack, Text, TextInput, Title, UnstyledButton } from "@mantine/core";
import { IconSearch } from "@tabler/icons-react";
import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { z } from "zod";

import type { Template } from "../../../api/gen/mimir/v1/template_pb";
import { listTemplates } from "../../../api/queries";
import { ButtonLink } from "../../../components/links";
import { Page } from "../../../components/Page";
import { categoryColor, count, dataTypeOf, dataTypes, kindColor, kindLabel, kinds, matches } from "../../../lib/templates";

const search = z.object({
  kind: z.enum(["yes_no", "group", "number"]).optional(),
  data: z.enum(["table", "text", "photos", "photo_table", "sounds", "boxes"]).optional(),
  cat: z.string().optional(),
  q: z.string().optional(),
});

export const Route = createFileRoute("/_app/templates/")({
  validateSearch: search,
  loader: ({ context }) => context.queryClient.ensureQueryData(listTemplates),
  component: Gallery,
});

function Gallery() {
  const { data } = useSuspenseQuery(listTemplates);
  const s = Route.useSearch();
  const navigate = Route.useNavigate();
  const set = (patch: z.infer<typeof search>) => void navigate({ search: (cur) => ({ ...cur, ...patch }), replace: true });
  const label = (id: string) => data.categories.find((c) => c.id === id)?.label ?? id;

  const fits = (t: Template, ignore?: "data" | "cat") =>
    (!s.kind || t.kind === s.kind) &&
    (ignore === "data" || !s.data || t.data === s.data) &&
    (ignore === "cat" || !s.cat || t.category === s.cat) &&
    matches(t, label(t.category), s.q ?? "");
  const shown = data.templates.filter((t) => fits(t));
  const groups = data.categories
    .map((c) => ({ ...c, items: shown.filter((t) => t.category === c.id) }))
    .filter((g) => g.items.length > 0);

  return (
    <Page title="Start from a template" intro="Each template is a real business problem a model can help with. It shows exactly what data to bring, a table or photos, with made-up examples you can download and try right away.">
      <SimpleGrid cols={{ base: 1, sm: 3 }}>
        {[
          ["Pick a problem", "Like “which customers will leave?”"],
          ["Bring data shaped like the example", "A spreadsheet, photos or a database query."],
          ["We check it, then teach", "We tell you what to fix before anything runs."],
        ].map(([title, hint], i) => (
          <Card key={title} withBorder radius="lg" padding="md">
            <Group wrap="nowrap" align="flex-start" gap="sm">
              <div className="flex size-8 shrink-0 items-center justify-center rounded-full bg-[var(--mantine-color-teal-light)] font-bold text-[var(--mantine-color-teal-8)]">{i + 1}</div>
              <div><Text fw={700}>{title}</Text><Text size="sm" c="dimmed">{hint}</Text></div>
            </Group>
          </Card>
        ))}
      </SimpleGrid>

      <Stack gap="xs">
        <Group justify="space-between">
          <Title order={2} fz="lg">Not sure where to start? What do you want to know?</Title>
          {s.kind && <Button variant="subtle" size="compact-sm" onClick={() => set({ kind: undefined })}>Show every kind</Button>}
        </Group>
        <SimpleGrid cols={{ base: 1, sm: 3 }}>
          {kinds.map((k) => (
            <UnstyledButton
              key={k.id}
              onClick={() => set({ kind: s.kind === k.id ? undefined : k.id })}
              aria-pressed={s.kind === k.id}
              className={`rounded-xl border-2 bg-[var(--mantine-color-body)] p-4 text-left ${s.kind === k.id ? "border-[var(--mantine-color-teal-7)] ring-4 ring-[var(--mantine-color-teal-light)]" : "border-[var(--mantine-color-default-border)]"}`}
            >
              <Text fw={700}>{k.title}</Text>
              <Text size="sm" c="dimmed">{k.hint}</Text>
            </UnstyledButton>
          ))}
        </SimpleGrid>
      </Stack>

      <TextInput
        leftSection={<IconSearch size={18} />}
        placeholder="Search, for example churn, fraud or machine"
        aria-label="Search templates"
        value={s.q ?? ""}
        onChange={(e) => set({ q: e.currentTarget.value || undefined })}
        className="max-w-md"
      />
      <Chips
        label="What data do you have?"
        items={[{ id: undefined, label: "Any data" }, ...dataTypes.map((d) => ({ id: d.id, label: d.chip }))].map((d) => ({
          ...d, count: data.templates.filter((t) => fits(t, "data") && (!d.id || t.data === d.id)).length,
        }))}
        value={s.data}
        onChange={(v) => set({ data: v as z.infer<typeof search>["data"] })}
      />
      <Chips
        label="Line of business"
        items={[{ id: undefined, label: "All" }, ...data.categories.map((c) => ({ id: c.id, label: c.label }))].map((c) => ({
          ...c, count: data.templates.filter((t) => fits(t, "cat") && (!c.id || t.category === c.id)).length,
        }))}
        value={s.cat}
        onChange={(v) => set({ cat: v })}
      />

      {groups.length === 0 && (
        <Card withBorder radius="lg" padding="lg"><Text c="dimmed">No template fits all of that. Clear a filter, or start from scratch below.</Text></Card>
      )}
      {groups.map((g) => {
        const [bg, fg] = categoryColor(g.id);
        return (
          <Stack key={g.id} gap="sm">
            <Group wrap="nowrap" gap="sm">
              <div className="flex size-11 shrink-0 items-center justify-center rounded-xl" style={{ background: bg, color: fg }}>
                <svg aria-hidden="true" width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><path d={g.icon} /></svg>
              </div>
              <div><Title order={2} fz="xl">{g.label}</Title><Text size="sm" c="dimmed">{g.blurb}</Text></div>
            </Group>
            <SimpleGrid cols={{ base: 1, sm: 2, lg: 3 }}>
              {g.items.map((t) => <TemplateCard key={t.id} t={t} />)}
            </SimpleGrid>
          </Stack>
        );
      })}

      <Card withBorder radius="lg" padding="lg">
        <Group justify="space-between" gap="md">
          <div className="max-w-xl">
            <Text fw={700}>Your problem isn&apos;t here?</Text>
            <Text size="sm" c="dimmed">Start from scratch with your own table, text, photos, or photos with details. We ask what your data looks like and guide you from there.</Text>
          </div>
          <ButtonLink to="/models/teach" variant="default">Start from scratch</ButtonLink>
        </Group>
      </Card>
    </Page>
  );
}

function Chips({ label, items, value, onChange }: { label: string; items: { id?: string; label: string; count: number }[]; value?: string; onChange: (v?: string) => void }) {
  return (
    <div role="group" aria-label={label} className="flex flex-wrap items-center gap-2">
      <Text size="sm" fw={600} mr={4}>{label}</Text>
      {items.map((c) => {
        const on = value === c.id;
        return (
          <Button
            key={c.id ?? "all"}
            radius="xl"
            size="sm"
            variant={on ? "filled" : "default"}
            color={on ? "dark" : undefined}
            aria-pressed={on}
            onClick={() => onChange(c.id)}
            rightSection={<Badge size="sm" variant={on ? "white" : "light"} color={on ? "dark" : "gray"}>{c.count}</Badge>}
          >
            {c.label}
          </Button>
        );
      })}
    </div>
  );
}

export function TemplateCard({ t }: { t: Template }) {
  const photos = t.data === "photos";
  return (
    <Link
      to="/templates/$id"
      params={{ id: t.id }}
      className="flex flex-col gap-2 rounded-xl text-inherit no-underline border border-[var(--mantine-color-default-border)] bg-[var(--mantine-color-body)] p-4 text-left transition hover:border-[var(--mantine-color-teal-6)] hover:shadow-sm"
    >
      <Group gap={6}>
        <Badge variant="light" color={kindColor(t.kind)} radius="xl">{kindLabel(t.kind)}</Badge>
        {t.data !== "table" && <Badge variant="light" color="yellow" radius="xl">{dataTypeOf(t.data).chip}</Badge>}
      </Group>
      <Text fw={700} fz="lg" lh={1.25}>{t.name}</Text>
      <Text size="sm" c="dimmed" className="flex-1">{t.question}</Text>
      <Text size="xs" c="dimmed">
        {photos ? count(t.folders.length, "answer") : count(t.columns.length, "column")} · {photos ? `${t.rowsMin.toLocaleString()}+ photos each` : `${t.rowsMin.toLocaleString()}+ rows`} · From {t.sourceShort}
      </Text>
    </Link>
  );
}
