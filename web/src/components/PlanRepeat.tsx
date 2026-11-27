import { Chip, Group, Select, Stack, Text, TextInput } from "@mantine/core";

import { clock, planRepeats, weekdays, type Repeat } from "../lib/visits";
import { t } from "../i18n";

// PlanRepeat picks how a visit plan is made again: once, every day or
// week from now, or at a time of day on chosen weekdays.
export function PlanRepeat({ value, onChange, disabled, description, label = t("Make it") }: {
  value: Repeat; onChange: (r: Repeat) => void; disabled?: boolean; description?: string; label?: string;
}) {
  return (
    <Stack gap="xs">
      <Select label={label} data={planRepeats} value={value.mode} allowDeselect={false} disabled={disabled} description={description}
        onChange={(v) => onChange({ ...value, mode: v ?? "0" })} />
      {value.mode === "schedule" && !disabled && (
        <>
          <TextInput type="time" label={t("At")} description={t("In the plan's time zone.")} w={160} value={clock(value.atMinute)}
            onChange={(e) => {
              const [h, m] = e.currentTarget.value.split(":").map(Number);
              if (Number.isFinite(h) && Number.isFinite(m)) onChange({ ...value, atMinute: h! * 60 + m! });
            }} />
          <div>
            <Text size="sm" fw={500} mb={6}>{t("On")}</Text>
            <Chip.Group multiple value={value.weekdays.map(String)} onChange={(v) => onChange({ ...value, weekdays: v.map(Number).sort() })}>
              <Group gap="xs">{weekdays.map((d, i) => <Chip key={d} value={String(i)} size="sm">{d}</Chip>)}</Group>
            </Chip.Group>
            {value.weekdays.length === 0 && <Text size="xs" c="dimmed" mt={4}>{t("No day picked: every day.")}</Text>}
          </div>
        </>
      )}
    </Stack>
  );
}
