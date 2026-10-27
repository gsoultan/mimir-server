import { Badge, Card, Group, Text, ThemeIcon } from "@mantine/core";
import {
  IconArrowRight, IconBooks, IconBrandGolang, IconCategory, IconChartDots3, IconChartLine, IconFileUpload, IconHeartHandshake, IconListNumbers,
  IconMessageChatbot, IconScan, IconTable, IconToggleRight, IconZoomExclamation, type Icon,
} from "@tabler/icons-react";
import type { ReactNode } from "react";

import type { GoalId, Ready } from "../lib/goals";

export const goalIcons: Record<GoalId, Icon> = {
  yes_no: IconToggleRight,
  group: IconCategory,
  number: IconListNumbers,
  segments: IconChartDots3,
  write: IconMessageChatbot,
  recommend: IconHeartHandshake,
  forecast: IconChartLine,
  odd_one: IconZoomExclamation,
  find_in_photo: IconScan,
};

export function ReadyBadge({ ready }: { ready: Ready }) {
  return ready === "ready"
    ? <Badge variant="light" color="teal">No code</Badge>
    : ready === "file"
      ? <Badge variant="light" color="blue">Bring a model</Badge>
      : <Badge variant="light" color="grape">Your own code</Badge>;
}

// GoalCard is one goal on the Build a model page. On a phone it shows
// only the icon, the goal and its example.
export function GoalCard({ id, title, example, ready, how }: { id: GoalId; title: string; example: string; ready: Ready; how: string }) {
  const Icon = goalIcons[id];
  return (
    <Card withBorder radius="lg" padding="md" className="h-full transition-colors hover:border-[var(--mantine-color-teal-6)]">
      <Group justify="space-between" align="flex-start" mb="xs" wrap="nowrap">
        <ThemeIcon variant="light" size={38} radius="md"><Icon size={22} /></ThemeIcon>
        <div className="hidden sm:block"><ReadyBadge ready={ready} /></div>
      </Group>
      <Text fw={700}>{title}</Text>
      <Text size="sm" c="dimmed">{example}</Text>
      <Text size="xs" c="teal.8" fw={600} mt="xs" className="hidden sm:block">{how}</Text>
    </Card>
  );
}

export type WayId = "template" | "data" | "file" | "code";

export const ways: { id: WayId; icon: Icon; title: string; hint: string; who: string }[] = [
  { id: "template", icon: IconBooks, title: "Start from a template", hint: "A ready problem with example data. Bring a file shaped like it.", who: "Easiest · no code" },
  { id: "data", icon: IconTable, title: "Start from your own data", hint: "Upload a table, text or photos and say which column is the answer.", who: "No code · you choose" },
  { id: "file", icon: IconFileUpload, title: "Bring a trained model", hint: "A Mimir Model, ONNX or GGUF file trained somewhere else.", who: "Already trained" },
  { id: "code", icon: IconBrandGolang, title: "Write your own model", hint: "Go code on a worker, for anything the others can't do.", who: "For developers" },
];

export function WayCard({ icon: Icon, title, hint, who }: { icon: Icon; title: string; hint: string; who: string }) {
  return (
    <Card withBorder radius="lg" padding="md" className="h-full transition-colors hover:border-[var(--mantine-color-teal-6)]">
      <Group gap="sm" wrap="nowrap" align="flex-start">
        <Icon size={26} stroke={1.6} color="var(--mantine-color-teal-7)" />
        <div className="min-w-0 flex-1">
          <Text fw={700}>{title}</Text>
          <Text size="sm" c="dimmed">{hint}</Text>
          <Text size="xs" fw={600} mt={6}>{who}</Text>
        </div>
        <IconArrowRight size={18} color="var(--mantine-color-gray-5)" />
      </Group>
    </Card>
  );
}

// PathOption is one way to build a goal, ranked on its guide.
export function PathOption({ best, title, hint, action, children }: { best?: boolean; title: string; hint: string; action?: ReactNode; children?: ReactNode }) {
  return (
    <Card withBorder radius="lg" padding="lg" style={best ? { borderColor: "var(--mantine-color-teal-6)", borderWidth: 2 } : undefined}>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 flex-1">
          <Group gap="xs">
            <Text fw={700}>{title}</Text>
            {best && <Badge size="sm">Best for you</Badge>}
          </Group>
          <Text size="sm" c="dimmed">{hint}</Text>
        </div>
        {action}
      </div>
      {children}
    </Card>
  );
}

export function Stat({ k, v }: { k: string; v: string }) {
  return (
    <div className="rounded-md bg-[var(--mantine-color-default-hover)] p-2">
      <Text size="xs" c="dimmed">{k}</Text>
      <Text size="sm" fw={600}>{v}</Text>
    </div>
  );
}
