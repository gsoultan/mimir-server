import { Badge, Button, Card, Group, Select, SimpleGrid, Stack, Text } from "@mantine/core";
import { IconDownload } from "@tabler/icons-react";
import type { ReactNode } from "react";

import type { DataCheck as Check } from "../api/gen/mimir/v1/template_pb";
import { count, download } from "../lib/templates";

const marks = {
  ok: { sign: "✓", cls: "bg-[var(--mantine-color-teal-light)] text-[var(--mantine-color-teal-8)]" },
  warn: { sign: "!", cls: "bg-[var(--mantine-color-orange-light)] text-[var(--mantine-color-orange-9)]" },
  bad: { sign: "×", cls: "bg-[var(--mantine-color-red-light)] text-[var(--mantine-color-red-9)]" },
  info: { sign: "i", cls: "bg-[var(--mantine-color-blue-light)] text-[var(--mantine-color-blue-9)]" },
};
type Level = keyof typeof marks;

function Row({ level, children }: { level: Level; children: ReactNode }) {
  const m = marks[level];
  return (
    <Group wrap="nowrap" align="flex-start" gap="sm">
      <span aria-hidden="true" className={`flex size-6 shrink-0 items-center justify-center rounded-full text-sm font-extrabold ${m.cls}`}>{m.sign}</span>
      <div className="min-w-0 flex-1 text-sm">{children}</div>
    </Group>
  );
}

// answers sums up the answer column in a sentence.
function answers(c: Check) {
  if (c.numberAnswer && c.answers.length >= 20) return "Every row has a number.";
  const top = c.answers.slice(0, 5).map((a) => `${a.value} (${a.count.toLocaleString()})`);
  const more = c.answers.length > 5 ? ` and ${c.answers.length - 5} more` : "";
  return `${count(c.answers.length, "answer")} found: ${top.join(", ")}${more}.`;
}

// DataCheck shows how well uploaded data fits, and lets people match
// columns the server couldn't find by name. onMatch gets the template
// column and the dataset column picked for it, "" for none.
export function DataCheck({ check, template, photos, sounds, onMatch, busy }: {
  check: Check;
  template?: string;
  photos?: boolean;
  // sounds marks folders of sounds rather than photos.
  sounds?: boolean;
  onMatch?: (column: string, found: string) => void;
  busy?: boolean;
}) {
  const answer = check.columns.find((c) => c.answer);
  const unit = sounds ? "sound" : "photo";
  const missingAnswer = !!template && !photos && !answer;
  const lost = check.columns.filter((c) => c.status === "missing");
  const found = check.columns.filter((c) => c.status !== "missing").length;
  // Notes the rows below already say in their own way.
  const notes = check.notes.filter((n) => !(template && (n.title === "Which column is the answer?" || /(isn't|aren't) there/.test(n.title) && !photos)));
  const fixes = check.notes.filter((n) => n.level === "bad").length;
  const tips = check.notes.filter((n) => n.level === "warn").length;
  const pill = check.verdict === "bad"
    ? { color: "red", text: `${count(fixes, "thing")} to fix first` }
    : tips > 0 ? { color: "orange", text: `Ready, ${count(tips, "thing")} to look at` } : { color: "teal", text: "Ready" };
  const all = [...check.columns.filter((c) => c.found).map((c) => c.found), ...check.extraColumns];
  const choices = (keep: string) => [...new Set([...check.extraColumns, keep].filter(Boolean))];

  return (
    <Card withBorder radius="lg" padding="lg" aria-live="polite">
      <Group justify="space-between" mb="md">
        <Text fw={700}>{template ? "Checking your data against the template" : "Checking your data"}</Text>
        <Badge size="lg" radius="xl" variant="light" color={pill.color}>{pill.text}</Badge>
      </Group>
      <Stack gap="sm">
        {photos || sounds ? (
          <>
            <Row level="ok"><b>{count(check.folders.length, "folder")} found.</b> Each folder becomes one answer.</Row>
            <SimpleGrid cols={{ base: 2, sm: 4 }} spacing="xs">
              {check.folders.map((f) => {
                const low = f.photos < (check.rowsMin > 0 ? check.rowsMin : 20);
                return (
                  <div key={f.name} className={`rounded-lg border px-3 py-2 ${low ? "border-[var(--mantine-color-orange-3)] bg-[var(--mantine-color-orange-light)]" : "border-[var(--mantine-color-default-border)]"}`}>
                    <Text ff="monospace" size="sm" fw={600} truncate>{f.name}/</Text>
                    <Text size="xs" c="dimmed">{count(f.photos, unit)}{template && !f.expected ? " · new answer" : ""}</Text>
                  </div>
                );
              })}
            </SimpleGrid>
            <Row level="ok"><b>{count(check.rows, unit)} in all.</b> {sounds ? "Each is heard as one second, from its middle; we convert every rate for you." : "They come in different sizes; we resize them for you."}</Row>
          </>
        ) : (
          <>
            {template && <Row level={lost.length ? "warn" : "ok"}><b>{found} of {check.columns.length} columns found</b>{lost.length || check.columns.some((c) => c.status === "renamed") ? "." : " by name."}</Row>}
            {template && check.columns.filter((c) => c.status === "renamed" && !c.answer).map((c) => (
              <Row key={c.name} level="ok"><b>{c.found}</b> is used as <code>{c.name}</code>.</Row>
            ))}
            {template && lost.filter((c) => c.name !== check.columns[check.columns.length - 1]?.name).map((c) => (
              <Row key={c.name} level="warn">
                <div><b><code>{c.name}</code> isn&apos;t in your file by that name.</b> Is it there under another one?</div>
                <Select
                  mt={6}
                  className="max-w-xs"
                  size="sm"
                  aria-label={`Your column for ${c.name}`}
                  placeholder="Not in my file"
                  data={choices("")}
                  value={null}
                  disabled={busy || check.extraColumns.length === 0}
                  onChange={(v) => v && onMatch?.(c.name, v)}
                />
              </Row>
            ))}
            {missingAnswer ? (
              <Row level="bad">
                <div><b>Which column is the answer?</b> We couldn&apos;t find <code>{check.columns[check.columns.length - 1]?.name}</code>, so <code>{check.answerColumn}</code> is the answer for now.</div>
                <Select
                  mt={6}
                  className="max-w-xs"
                  size="sm"
                  aria-label="The answer column"
                  data={[...new Set([check.answerColumn, ...all])]}
                  value={check.answerColumn}
                  disabled={busy}
                  onChange={(v) => v && onMatch?.(check.columns[check.columns.length - 1]!.name, v)}
                />
                {onMatch && <Button mt={6} size="xs" variant="light" loading={busy} onClick={() => onMatch(check.columns[check.columns.length - 1]!.name, check.answerColumn)}>Yes, {check.answerColumn} is the answer</Button>}
              </Row>
            ) : (
              <Row level="ok"><b>The answer column is {check.answerColumn}.</b> {answers(check)}</Row>
            )}
            {!check.notes.some((n) => /, (enough to start|fewer than)/.test(n.title)) && <Row level="ok"><b>{count(check.rows, "row")}.</b></Row>}
          </>
        )}
        {notes.map((n, i) => (
          <Row key={i} level={n.level as Level}>
            <b>{n.title}</b> {n.body}
            {check.missingPhotos.length > 0 && /no photo/.test(n.title) && (
              <div className="mt-1">
                <Button size="compact-sm" variant="subtle" leftSection={<IconDownload size={14} />} onClick={() => download("missing-photos.txt", "text/plain", check.missingPhotos.join("\n") + "\n")}>
                  Download the list
                </Button>
              </div>
            )}
          </Row>
        ))}
      </Stack>
    </Card>
  );
}
