import { Checkbox, Radio, Stack, Text } from "@mantine/core";
import { useSuspenseQuery } from "@tanstack/react-query";

import { Role, type Access } from "../api/gen/mimir/v1/access_pb";
import { listModels } from "../api/queries";
import { roles } from "../lib/roles";

export type AccessValue = { role: Role; allModels: boolean; modelIds: string[] };

export const defaultAccess: AccessValue = { role: Role.VIEWER, allModels: true, modelIds: [] };

export function fromProto(a: Access | undefined): AccessValue {
  return a ? { role: a.role, allModels: a.allModels, modelIds: [...a.modelIds] } : defaultAccess;
}

// AccessPicker chooses a role and which models it covers. maxRole is the
// highest role the current person may hand out.
// otherTeam means the person is in a team whose models aren't listed here,
// so only "all models" can be chosen.
export function AccessPicker({ value, onChange, maxRole, subject, otherTeam = false }: { value: AccessValue; onChange: (v: AccessValue) => void; maxRole: Role; subject: string; otherTeam?: boolean }) {
  const { data } = useSuspenseQuery(listModels);
  const manager = value.role >= Role.ADMIN;
  return (
    <Stack gap="md">
      <Radio.Group label={`What can ${subject} do?`} value={String(value.role)} onChange={(r) => onChange({ ...value, role: Number(r) as Role })}>
        <Stack gap="xs" mt="xs">
          {roles.filter((r) => r.value <= maxRole).map((r) => (
            <Radio key={r.value} value={String(r.value)} label={r.label} description={r.blurb} />
          ))}
        </Stack>
      </Radio.Group>
      <Radio.Group
        label="Which models?"
        description={manager ? "Admins and owners always reach every model in their team." : otherTeam ? "All of their team's models. To pick some, move yourself to that team first." : undefined}
        value={value.allModels || manager ? "all" : "some"}
        onChange={(v) => onChange({ ...value, allModels: v === "all" })}
      >
        <Stack gap="xs" mt="xs">
          <Radio value="all" label="All models, including ones added later" disabled={manager} />
          <Radio value="some" label="Only the models I pick" disabled={manager || otherTeam} />
        </Stack>
      </Radio.Group>
      {!value.allModels && !manager && (
        data.models.length === 0 ? (
          <Text size="sm" c="dimmed">There are no models to pick yet.</Text>
        ) : (
          <Checkbox.Group value={value.modelIds} onChange={(ids) => onChange({ ...value, modelIds: ids })}>
            <Stack gap="xs" className="max-h-48 overflow-y-auto rounded-md border border-[var(--mantine-color-default-border)] p-3">
              {data.models.map((m) => <Checkbox key={m.id} value={m.id} label={m.name} />)}
            </Stack>
          </Checkbox.Group>
        )
      )}
    </Stack>
  );
}

// describe says what access covers. Without modelNames (another team's
// models) it counts them instead of naming them.
export function describe(a: Access | undefined, modelNames?: Map<string, string>): string {
  if (!a) return "";
  const role = roles.find((r) => r.value === a.role)?.label ?? "Unknown";
  if (a.allModels) return `${role} · all models`;
  if (!modelNames) return `${role} · ${a.modelIds.length === 1 ? "1 model" : `${a.modelIds.length} models`}`;
  const names = a.modelIds.map((id) => modelNames.get(id) ?? "a deleted model");
  return `${role} · ${names.length === 0 ? "no models" : names.length <= 2 ? names.join(", ") : `${names.length} models`}`;
}
