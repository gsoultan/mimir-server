import { Badge, Card, Group, Text, ThemeIcon, type MantineColor } from "@mantine/core";
import type { ReactNode } from "react";

// What a status means, in one place, so every page colors it the same way:
// teal is fine, orange needs a look, red is broken, violet is working on it.
export type Tone = "ok" | "warn" | "bad" | "busy" | "idle";
export const toneColor: Record<Tone, MantineColor> = { ok: "teal", warn: "orange", bad: "red", busy: "violet", idle: "gray" };

// StatusPill is a colored dot and a word. The word carries the meaning, so
// it still reads without color.
export function StatusPill({ tone, children }: { tone: Tone; children: ReactNode }) {
  return <Badge color={toneColor[tone]} className="status-pill" size="md">{children}</Badge>;
}

// StatTile is one big number, what it counts, and an optional hint.
export function StatTile({ value, label, hint }: { value: ReactNode; label: ReactNode; hint?: ReactNode }) {
  return (
    <Card withBorder padding="lg" className="flex-1">
      <Text ff="var(--font-display)" fz={26} fw={700} lh={1.15} style={{ letterSpacing: "-0.02em" }}>{value}</Text>
      <Text size="sm" c="dimmed">{label}</Text>
      {hint && <Text size="xs" c="var(--mimir-faint)" mt={2}>{hint}</Text>}
    </Card>
  );
}

// RowList is one panel holding rows split by hairlines: the shape for
// a handful of things of one kind, each with its own action.
export function RowList({ children }: { children: ReactNode }) {
  return <Card withBorder padding={0}><div className="row-list">{children}</div></Card>;
}

// Row is one entry of a RowList: an icon, a bold line, a plain line, and
// actions on the right. On a phone the actions drop under the text.
export function Row({ icon, tone = "idle", title, detail, children, extra }: { icon?: ReactNode; tone?: Tone; title: ReactNode; detail?: ReactNode; children?: ReactNode; extra?: ReactNode }) {
  return (
    <Group gap="md" px="lg" py="md" wrap="wrap" align="center">
      {icon && <ThemeIcon size={38} radius="md" variant="light" color={toneColor[tone]}>{icon}</ThemeIcon>}
      <div className="min-w-0 flex-1 basis-[220px]">
        <Text fw={650} component="div">{title}</Text>
        {detail && <Text size="sm" c="dimmed" component="div">{detail}</Text>}
        {extra}
      </div>
      {children && <Group gap="xs" wrap="nowrap" className="ml-auto">{children}</Group>}
    </Group>
  );
}
