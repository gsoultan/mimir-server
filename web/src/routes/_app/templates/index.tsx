import { Badge, Button, Card, CloseButton, Group, SimpleGrid, Stack, Text, TextInput, Title } from "@mantine/core";
import { IconArrowRight, IconSearch } from "@tabler/icons-react";
import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { z } from "zod";

import type { Template } from "../../../api/gen/mimir/v1/template_pb";
import { listTemplates } from "../../../api/queries";
import { ButtonLink } from "../../../components/links";
import { Page } from "../../../components/Page";
import { categoryColor, count, dataTypeOf, dataTypes, kindColor, kindLabel, kinds, matches } from "../../../lib/templates";

const search = z.object({
  kind: z.enum(["yes_no", "group", "number", "ranked"]).optional(),
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

  const kind = kinds.find((k) => k.id === s.kind);
  return (
    <Page
      title="Start from a template"
      intro="Real problems with example data. Bring data shaped like the example and we teach it."
      crumbs={[{ label: "Build a model", to: "/models/build" }, { label: "Templates" }]}
      actions={<ButtonLink to="/models/teach" variant="default">Start from scratch</ButtonLink>}
    >
      <Stack gap="sm">
        <TextInput
          size="md"
          leftSection={<IconSearch size={18} />}
          placeholder='What do you want to know? For example "who will cancel" or "spot bad parts"'
          aria-label="Search templates"
          value={s.q ?? ""}
          onChange={(e) => set({ q: e.currentTarget.value || undefined })}
        />
        <Chips
          label="Your data"
          items={[{ id: undefined, label: "Any" }, ...dataTypes.map((d) => ({ id: d.id, label: d.chip }))].map((d) => ({
            ...d, count: data.templates.filter((t) => fits(t, "data") && (!d.id || t.data === d.id)).length,
          }))}
          value={s.data}
          onChange={(v) => set({ data: v as z.infer<typeof search>["data"] })}
        />
        <Chips
          label="Your work"
          items={[{ id: undefined, label: "All" }, ...data.categories.map((c) => ({ id: c.id, label: c.label }))].map((c) => ({
            ...c, count: data.templates.filter((t) => fits(t, "cat") && (!c.id || t.category === c.id)).length,
          }))}
          value={s.cat}
          onChange={(v) => set({ cat: v })}
        />
        {kind && (
          <Group gap="xs">
            <Text size="sm" c="dimmed">Only templates that answer</Text>
            <Badge size="lg" variant="light" color={kindColor(kind.id)} rightSection={<CloseButton size="xs" aria-label="Show every kind" onClick={() => set({ kind: undefined })} />}>{kind.label}</Badge>
          </Group>
        )}
      </Stack>

      {groups.length === 0 && (
        <Card withBorder radius="lg" padding="lg"><Text c="dimmed">No template fits all of that. Clear a filter, or start from scratch below.</Text></Card>
      )}
      {groups.map((g) => {
        const [bg, fg] = categoryColor(g.id);
        return (
          <Stack key={g.id} gap="sm">
            <Group wrap="nowrap" gap="sm">
              <div className="flex size-8 shrink-0 items-center justify-center rounded-lg" style={{ background: bg, color: fg }}>
                <svg aria-hidden="true" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><path d={g.icon} /></svg>
              </div>
              <div><Title order={2}>{g.label}</Title><Text size="sm" c="dimmed">{g.blurb}</Text></div>
            </Group>
            <SimpleGrid cols={{ base: 1, sm: 2, lg: 3 }} spacing="md">
              {g.items.map((t) => <TemplateCard key={t.id} t={t} />)}
            </SimpleGrid>
          </Stack>
        );
      })}

      <Card withBorder padding="lg">
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
    <div role="group" aria-label={label} className="flex flex-wrap items-center gap-1.5">
      <Text size="sm" c="dimmed" w={88} className="shrink-0">{label}</Text>
      {items.filter((c) => c.count > 0 || value === c.id).map((c) => {
        const on = value === c.id;
        return (
          <Button
            key={c.id ?? "all"}
            radius="xl"
            size="compact-sm"
            fw={600}
            variant={on ? "filled" : "default"}
            color={on ? "dark" : undefined}
            aria-pressed={on}
            onClick={() => onChange(on ? undefined : c.id)}
            rightSection={<Text span size="xs" opacity={0.7}>{c.count}</Text>}
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
      className="group flex flex-col gap-1.5 rounded-xl border border-[var(--mimir-line)] bg-[var(--mimir-surface)] p-5 text-left text-inherit no-underline shadow-[var(--mimir-shadow)] transition hover:-translate-y-0.5 hover:border-[var(--mantine-primary-color-filled)]"
    >
      <Group gap={8}>
        <Text size="sm" fw={650} c={`var(--mantine-color-${kindColor(t.kind)}-light-color)`}>{kindLabel(t.kind)}</Text>
        {t.data !== "table" && <Badge variant="light" color="yellow" size="sm">{dataTypeOf(t.data).chip}</Badge>}
      </Group>
      <Text fw={700} fz="lg" lh={1.25}>{t.name}</Text>
      <Text size="sm" c="dimmed" className="flex-1">{t.question}</Text>
      <Group justify="space-between" wrap="nowrap" mt={6}>
        <Text size="xs" c="dimmed">
          {photos ? count(t.folders.length, "answer") : count(t.columns.length, "column")} · {photos ? `${t.rowsMin.toLocaleString()}+ photos each` : `${t.rowsMin.toLocaleString()}+ rows`} · From {t.sourceShort}
        </Text>
        <IconArrowRight size={16} className="shrink-0 opacity-50 transition group-hover:translate-x-0.5 group-hover:opacity-100" />
      </Group>
    </Link>
  );
}
