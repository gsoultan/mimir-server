import { ActionIcon, Button, Card, Group, Menu, NumberInput, SegmentedControl, SimpleGrid, Stack, Text } from "@mantine/core";
import { IconArrowDown, IconArrowUp, IconCircleCheck, IconPlus, IconTrash } from "@tabler/icons-react";

import { examplesWanted, learned, medium, sizes, type Size, type Step } from "../lib/shape";

// ModelShape picks how big a table model is, or lets someone design its
// steps themselves, and says what that means in plain numbers.
export function ModelShape({ inputs, columns, answers, rows, size, steps, onChange }: {
  inputs: number; // numbers one example becomes
  columns: string[];
  answers: { what: string; count: number }; // what it answers, and how many numbers that is
  rows: number;
  size: Size;
  steps: Step[];
  onChange: (size: Size, steps: Step[]) => void;
}) {
  const shown = size === "design" ? steps : sizes[size](inputs);
  const params = learned(inputs, shown, answers.count);
  const wanted = examplesWanted(params);
  const set = (i: number, s: Step) => onChange("design", steps.map((x, j) => (j === i ? s : x)));
  const move = (i: number, by: number) => {
    const next = [...steps];
    const [s] = next.splice(i, 1);
    next.splice(i + by, 0, s!);
    onChange("design", next);
  };
  const dense = steps.filter((s) => s.kind === "dense").length;

  return (
    <SimpleGrid cols={{ base: 1, md: 3 }} spacing="lg" mt="md">
      <Stack className="md:col-span-2">
        <Stack gap={4}>
          <Text fw={600} size="sm">How big a model?</Text>
          <Text size="xs" c="dimmed">Bigger finds subtler patterns but needs more examples. Medium suits most tables.</Text>
          <SegmentedControl
            fullWidth
            data={[{ value: "small", label: "Small" }, { value: "medium", label: "Medium" }, { value: "large", label: "Large" }, { value: "design", label: "Design it myself" }]}
            value={size}
            onChange={(v) => onChange(v as Size, v === "design" && steps.length === 0 && size !== "design" ? sizes[size](inputs) : steps)}
          />
        </Stack>
        {size === "design" && (
          <Card withBorder radius="lg" padding="md">
            <Group justify="space-between" mb="sm" wrap="nowrap">
              <Text fw={700} size="sm">Your model, from question to answer</Text>
              <Button variant="subtle" size="compact-sm" onClick={() => onChange("design", medium(inputs))}>Start again from Medium</Button>
            </Group>
            <Stack gap="xs">
              <Fixed title={`Reads ${columns.length > 0 ? `${columns.length} column${columns.length === 1 ? "" : "s"}` : `${inputs} numbers`}`} hint={columns.join(", ")} />
              {steps.map((s, i) => (
                <div key={i} className="flex items-center gap-2 rounded-lg border border-[var(--mantine-color-default-border)] px-3 py-2">
                  <div className="min-w-0 flex-1">
                    <Text size="sm" fw={600}>{s.kind === "dense" ? "Learn patterns" : "Forget a little"}</Text>
                    <Text size="xs" c="dimmed">{s.kind === "dense" ? "Dense, ReLU" : "Dropout, so it doesn't memorise"}</Text>
                  </div>
                  {s.kind === "dense" ? (
                    <NumberInput size="xs" w={110} min={1} max={1024} suffix=" units" value={s.units} onChange={(v) => set(i, { ...s, units: Math.max(1, Math.min(1024, Math.round(Number(v) || 1))) })} aria-label="Units" />
                  ) : (
                    <NumberInput size="xs" w={110} min={1} max={90} suffix="%" value={Math.round(s.rate * 100)} onChange={(v) => set(i, { ...s, rate: Math.max(1, Math.min(90, Number(v) || 10)) / 100 })} aria-label="How much it forgets" />
                  )}
                  <ActionIcon variant="subtle" color="gray" disabled={i === 0} onClick={() => move(i, -1)} aria-label="Move up"><IconArrowUp size={16} /></ActionIcon>
                  <ActionIcon variant="subtle" color="gray" disabled={i === steps.length - 1} onClick={() => move(i, 1)} aria-label="Move down"><IconArrowDown size={16} /></ActionIcon>
                  <ActionIcon variant="subtle" color="gray" disabled={s.kind === "dense" && dense === 1} onClick={() => onChange("design", steps.filter((_, j) => j !== i))} aria-label="Remove step"><IconTrash size={16} /></ActionIcon>
                </div>
              ))}
              <Menu position="bottom-start">
                <Menu.Target>
                  <Button variant="light" leftSection={<IconPlus size={16} />} className="self-start" disabled={steps.length >= 8}>Add a step</Button>
                </Menu.Target>
                <Menu.Dropdown>
                  <Menu.Item onClick={() => onChange("design", [...steps, { kind: "dense", units: 32, rate: 0 }])}>Learn patterns</Menu.Item>
                  <Menu.Item onClick={() => onChange("design", [...steps, { kind: "dropout", units: 0, rate: 0.1 }])}>Forget a little</Menu.Item>
                </Menu.Dropdown>
              </Menu>
              <Fixed title={`Answers ${answers.what}`} hint="With how sure it is." />
            </Stack>
          </Card>
        )}
      </Stack>
      <Stack>
        <Card withBorder radius="lg" padding="md">
          <Text fw={700} size="sm" mb="xs">What this means</Text>
          <Stack gap="xs">
            <Fact k="Numbers it learns" v={params.toLocaleString()} />
            <Fact k="Examples it wants" v={`${wanted.toLocaleString()} or more · you have ${rows.toLocaleString()}`} ok={rows >= wanted} />
            <Fact k="Each answer" v={params < 2_000_000 ? "under 1 ms" : "a few ms"} />
          </Stack>
        </Card>
        {size === "design" && (
          <Card withBorder radius="lg" padding="md">
            <Text fw={700} size="sm" mb={4}>Tips</Text>
            <Text size="xs" c="dimmed">Start with Medium and only change it if the score is low. Two or three “Learn patterns” steps are enough for most tables. If the practice score is far above the real score, add “Forget a little”.</Text>
          </Card>
        )}
      </Stack>
    </SimpleGrid>
  );
}

function Fixed({ title, hint }: { title: string; hint: string }) {
  return (
    <div className="rounded-lg bg-[var(--mantine-color-default-hover)] px-3 py-2">
      <Text size="sm" fw={600}>{title}</Text>
      {hint && <Text size="xs" c="dimmed" truncate>{hint}</Text>}
    </div>
  );
}

function Fact({ k, v, ok }: { k: string; v: string; ok?: boolean }) {
  return (
    <div>
      <Text size="xs" c="dimmed">{k}</Text>
      <Group gap={4} wrap="nowrap">{ok && <IconCircleCheck size={16} color="var(--mantine-color-teal-7)" />}<Text size="sm" fw={600}>{v}</Text></Group>
    </div>
  );
}
