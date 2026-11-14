import { Alert, Badge, Group, Stack, Table, Text } from "@mantine/core";
import { IconAlertCircle, IconAlertTriangle, IconCheck } from "@tabler/icons-react";
import { useMediaQuery } from "@mantine/hooks";
import { useMemo } from "react";

import { answer, describe, profile, tips, type Column, type Role } from "../lib/csv";
import { t } from "../i18n";

// The question asked, in the words of what the answer is to the model
// type picked.
const words: Record<Role, { label: string; hint: string }> = {
  "": { label: t("Which column is the answer?"), hint: t("The model learns to answer this from the other columns. Click a column to pick it.") },
  none: { label: t("The model reads every column"), hint: t("No answer column is needed.") },
  item: { label: t("Which column is the item?"), hint: t("What each person picked, bought or rated. Another column, such as customer, says who.") },
  value: { label: t("Which column holds the value to forecast?"), hint: t("A number in every row, like units sold. A date column says when, and a column like store says which series.") },
  action: { label: t("Which column is the action taken?"), hint: t("What was tried each time, like the offer shown. A column like reward, revenue or converted says how it turned out.") },
};

const kindColor: Record<Column["kind"], string> = { number: "blue", yesno: "gray", category: "gray", text: "grape", date: "gray", id: "orange", empty: "red" };

// ColumnPicker shows what each column of a table holds and what the
// model would answer with the column picked as the answer, so the answer
// column is chosen with open eyes. rows are the first rows of the table,
// read in the browser or sampled by the server; total is how many rows
// the whole table has, when known.
export function ColumnPicker({ columns, rows, total, value, onChange, role = "" }: {
  columns: string[];
  rows: string[][];
  total?: number;
  value: string;
  onChange: (column: string) => void;
  role?: Role;
}) {
  const cols = useMemo(() => profile({ columns, rows }), [columns, rows]);
  const picked = cols.find((c) => c.name === value);
  const verdict = picked && role !== "none" ? answer(picked, role) : undefined;
  const notes = role === "none" ? tips(cols, "") : tips(cols, value);
  const w = words[role];
  // On a phone each column is a row of its own, not a table.
  const phone = useMediaQuery("(max-width: 48em)");
  const partial = total !== undefined && total > rows.length;

  return (
    <Stack gap="sm">
      <div>
        <Text fw={600} size="sm">{w.label}</Text>
        <Text size="xs" c="dimmed">{w.hint}{partial ? t(" Read from the first {n1} of {n2} rows.", { n1: rows.length.toLocaleString(), n2: total.toLocaleString() }) : ""}</Text>
      </div>
      {phone ? (
        <Stack gap={6}>
          {cols.map((c) => {
            const on = c.name === value;
            const a = role !== "none" ? answer(c, role) : undefined;
            return (
              <div
                key={c.name}
                role={role !== "none" ? "radio" : undefined}
                aria-checked={role !== "none" ? on : undefined}
                tabIndex={role !== "none" ? 0 : undefined}
                onClick={role !== "none" ? () => onChange(c.name) : undefined}
                onKeyDown={role !== "none" ? (e) => { if (e.key === " " || e.key === "Enter") { e.preventDefault(); onChange(c.name); } } : undefined}
                className={`flex items-start gap-3 rounded-lg border px-3 py-2 ${on ? "border-[var(--mantine-color-teal-6)]" : "border-[var(--mantine-color-default-border)]"} ${role !== "none" ? "cursor-pointer" : ""}`}
                style={on ? { backgroundColor: "light-dark(var(--mantine-color-teal-1), var(--mantine-color-teal-9))" } : undefined}
              >
                {role !== "none" && <input type="radio" name="answer-column" checked={on} onChange={() => onChange(c.name)} aria-label={t("{name} is the answer", { name: c.name })} tabIndex={-1} className={`mt-1 size-4 ${a?.level === "bad" ? "accent-[var(--mantine-color-red-7)]" : "accent-[var(--mantine-color-teal-7)]"}`} />}
                <div className="min-w-0 flex-1">
                  <Group gap={6} wrap="nowrap">
                    <Text size="sm" fw={on ? 700 : 500} ff="monospace" truncate>{c.name}</Text>
                    {on && <Badge size="xs" variant="filled" color={verdict?.level === "bad" ? "red" : "teal"}>{t("Answer")}</Badge>}
                  </Group>
                  <Group gap={6} mt={2}>
                    <Badge variant="light" color={kindColor[c.kind]} tt="none" fw={500} size="sm">{describe(c)}</Badge>
                    {rows.length > 0 && c.filled < rows.length * 0.8 && <Text size="xs" c="orange.8">{Math.round(100 * c.filled / rows.length)}{t("% filled")}</Text>}
                  </Group>
                  <Text size="xs" c="dimmed" truncate mt={2}>{c.samples.map((s) => (s.length > 28 ? `${s.slice(0, 28)}…` : s)).join(" · ")}</Text>
                </div>
              </div>
            );
          })}
        </Stack>
      ) : (
      <Table.ScrollContainer minWidth={520}>
        <Table fz="sm" withTableBorder highlightOnHover={role !== "none"} verticalSpacing={6}>
          <Table.Thead>
            <Table.Tr>
              {role !== "none" && <Table.Th w={36} aria-label={t("Answer")} />}
              <Table.Th>{t("Column")}</Table.Th>
              <Table.Th>{t("Holds")}</Table.Th>
              <Table.Th>{t("Filled")}</Table.Th>
              <Table.Th>{t("For example")}</Table.Th>
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {cols.map((c) => {
              const on = c.name === value;
              const a = role !== "none" ? answer(c, role) : undefined;
              return (
                <Table.Tr
                  key={c.name}
                  onClick={role !== "none" ? () => onChange(c.name) : undefined}
                  className={role !== "none" ? "cursor-pointer" : undefined}
                  style={on ? { backgroundColor: "light-dark(var(--mantine-color-teal-1), var(--mantine-color-teal-9))" } : undefined}
                  aria-selected={role !== "none" ? on : undefined}
                >
                  {role !== "none" && (
                    <Table.Td>
                      {/* A plain input: a Mantine Radio here would answer the
                          source Radio.Group this table sits in. */}
                      <input type="radio" name="answer-column" checked={on} onChange={() => onChange(c.name)} aria-label={t("{name} is the answer", { name: c.name })} className={`size-4 cursor-pointer ${a?.level === "bad" ? "accent-[var(--mantine-color-red-7)]" : "accent-[var(--mantine-color-teal-7)]"}`} />
                    </Table.Td>
                  )}
                  <Table.Td>
                    <Group gap={6} wrap="nowrap">
                      <Text size="sm" fw={on ? 700 : 500} ff="monospace" className="break-words">{c.name}</Text>
                      {on && <Badge size="xs" variant="filled" color={verdict?.level === "bad" ? "red" : "teal"}>{t("Answer")}</Badge>}
                    </Group>
                  </Table.Td>
                  <Table.Td><Badge variant="light" color={kindColor[c.kind]} tt="none" fw={500} size="sm">{describe(c)}</Badge></Table.Td>
                  <Table.Td c={c.filled < rows.length * 0.8 ? "orange.8" : undefined}>{rows.length > 0 ? `${Math.round(100 * c.filled / rows.length)}%` : "–"}</Table.Td>
                  <Table.Td c="dimmed" className="max-w-[18rem] truncate">{c.samples.map((s) => (s.length > 28 ? `${s.slice(0, 28)}…` : s)).join(" · ")}</Table.Td>
                </Table.Tr>
              );
            })}
          </Table.Tbody>
        </Table>
      </Table.ScrollContainer>
      )}
      {verdict && (
        <Alert
          color={verdict.level === "bad" ? "red" : verdict.level === "warn" ? "orange" : "teal"}
          variant="light"
          icon={verdict.level === "bad" ? <IconAlertCircle /> : verdict.level === "warn" ? <IconAlertTriangle /> : <IconCheck />}
          title={verdict.level === "bad" ? t("{name} can't be the answer", { name: picked!.name }) : t("The model will answer {what}", { what: verdict.what })}
        >
          <Text size="sm">{verdict.level === "bad" ? verdict.problem : verdict.detail}{verdict.level === "warn" ? ` ${verdict.problem}` : ""}</Text>
        </Alert>
      )}
      {notes.length > 0 && (
        <Stack gap={2}>
          {notes.map((n) => <Text key={n.column} size="xs" c="dimmed"><b className="font-mono">{n.column}</b> {n.text}</Text>)}
        </Stack>
      )}
    </Stack>
  );
}
