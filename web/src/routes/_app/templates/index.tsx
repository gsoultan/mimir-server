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
import { t } from "../../../i18n";

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

  const fits = (tm: Template, ignore?: "data" | "cat") =>
    (!s.kind || tm.kind === s.kind) &&
    (ignore === "data" || !s.data || tm.data === s.data) &&
    (ignore === "cat" || !s.cat || tm.category === s.cat) &&
    matches(tm, label(tm.category), s.q ?? "");
  const shown = data.templates.filter((tm) => fits(tm));
  const groups = data.categories
    .map((c) => ({ ...c, items: shown.filter((tm) => tm.category === c.id) }))
    .filter((g) => g.items.length > 0);

  const kind = kinds.find((k) => k.id === s.kind);
  return (
    <Page
      title={t("Start from a template")}
      intro={t("Real problems with example data. Bring data shaped like the example and we teach it.")}
      crumbs={[{ label: t("Build a model"), to: "/models/build" }, { label: t("Templates") }]}
      actions={<ButtonLink to="/models/teach" variant="default">{t("Start from scratch")}</ButtonLink>}
    >
      <Stack gap="sm">
        <TextInput
          size="md"
          leftSection={<IconSearch size={18} />}
          placeholder={t("What do you want to know? For example \"who will cancel\" or \"spot bad parts\"")}
          aria-label={t("Search templates")}
          value={s.q ?? ""}
          onChange={(e) => set({ q: e.currentTarget.value || undefined })}
        />
        <Chips
          label={t("Your data")}
          items={[{ id: undefined, label: t("Any") }, ...dataTypes.map((d) => ({ id: d.id, label: d.chip }))].map((d) => ({
            ...d, count: data.templates.filter((tm) => fits(tm, "data") && (!d.id || tm.data === d.id)).length,
          }))}
          value={s.data}
          onChange={(v) => set({ data: v as z.infer<typeof search>["data"] })}
        />
        <Chips
          label={t("Your work")}
          items={[{ id: undefined, label: t("All") }, ...data.categories.map((c) => ({ id: c.id, label: c.label }))].map((c) => ({
            ...c, count: data.templates.filter((tm) => fits(tm, "cat") && (!c.id || tm.category === c.id)).length,
          }))}
          value={s.cat}
          onChange={(v) => set({ cat: v })}
        />
        {kind && (
          <Group gap="xs">
            <Text size="sm" c="dimmed">{t("Only templates that answer")}</Text>
            <Badge size="lg" variant="light" color={kindColor(kind.id)} rightSection={<CloseButton size="xs" aria-label={t("Show every kind")} onClick={() => set({ kind: undefined })} />}>{kind.label}</Badge>
          </Group>
        )}
      </Stack>

      {groups.length === 0 && (
        <Card withBorder radius="lg" padding="lg"><Text c="dimmed">{t("No template fits all of that. Clear a filter, or start from scratch below.")}</Text></Card>
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
              {g.items.map((tm) => <TemplateCard key={tm.id} t={tm} />)}
            </SimpleGrid>
          </Stack>
        );
      })}

      <Card withBorder padding="lg">
        <Group justify="space-between" gap="md">
          <div className="max-w-xl">
            <Text fw={700}>{t("Your problem isn't here?")}</Text>
            <Text size="sm" c="dimmed">{t("Start from scratch with your own table, text, photos, or photos with details. We ask what your data looks like and guide you from there.")}</Text>
          </div>
          <ButtonLink to="/models/teach" variant="default">{t("Start from scratch")}</ButtonLink>
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

export function TemplateCard({ t: tm }: { t: Template }) {
  const photos = tm.data === "photos";
  return (
    <Link
      to="/templates/$id"
      params={{ id: tm.id }}
      className="group flex flex-col gap-1.5 rounded-xl border border-[var(--mimir-line)] bg-[var(--mimir-surface)] p-5 text-left text-inherit no-underline shadow-[var(--mimir-shadow)] transition hover:-translate-y-0.5 hover:border-[var(--mantine-primary-color-filled)]"
    >
      <Group gap={8}>
        <Text size="sm" fw={650} c={`var(--mantine-color-${kindColor(tm.kind)}-light-color)`}>{kindLabel(tm.kind)}</Text>
        {tm.data !== "table" && <Badge variant="light" color="yellow" size="sm">{dataTypeOf(tm.data).chip}</Badge>}
      </Group>
      <Text fw={700} fz="lg" lh={1.25}>{tm.name}</Text>
      <Text size="sm" c="dimmed" className="flex-1">{tm.question}</Text>
      <Group justify="space-between" wrap="nowrap" mt={6}>
        <Text size="xs" c="dimmed">
          {photos ? count(tm.folders.length, "answer") : count(tm.columns.length, "column")} · {photos ? t("{n1}+ photos each", { n1: tm.rowsMin.toLocaleString() }) : t("{n1}+ rows", { n1: tm.rowsMin.toLocaleString() })}{" "}{t("· From")}{" "}{tm.sourceShort}
        </Text>
        <IconArrowRight size={16} className="shrink-0 opacity-50 transition group-hover:translate-x-0.5 group-hover:opacity-100" />
      </Group>
    </Link>
  );
}
