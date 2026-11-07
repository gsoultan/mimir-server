// A look inside a table before it is sent anywhere: its columns, what
// each one holds, and what a model would answer if a column were the
// answer. The server does the real reading; this is enough to pick the
// answer column with open eyes and to catch the usual mistakes first.

export type Parsed = { columns: string[]; rows: string[][] };

// parse reads CSV text with a header row: quoted cells, commas and line
// breaks inside quotes, CRLF and a BOM. When the text is cut short (only
// the start of a file was read), the last, incomplete row is dropped.
export function parse(text: string, { maxRows = Infinity, cut = false } = {}): Parsed {
  if (text.charCodeAt(0) === 0xfeff) text = text.slice(1);
  const records: string[][] = [];
  let row: string[] = [];
  let cell = "";
  let quoted = false;
  let i = 0;
  const n = text.length;
  let ended = true; // the last record closed with a line break
  while (i < n) {
    const ch = text[i]!;
    if (quoted) {
      if (ch === '"') {
        if (text[i + 1] === '"') { cell += '"'; i += 2; continue; }
        quoted = false; i++; continue;
      }
      cell += ch; i++; continue;
    }
    if (ch === '"') { quoted = true; i++; continue; }
    if (ch === ",") { row.push(cell); cell = ""; i++; continue; }
    if (ch === "\n" || ch === "\r") {
      if (ch === "\r" && text[i + 1] === "\n") i++;
      i++;
      row.push(cell); cell = "";
      if (row.length > 1 || row[0] !== "") records.push(row);
      row = [];
      ended = true;
      if (records.length > maxRows) break;
      continue;
    }
    cell += ch; i++;
    ended = false;
  }
  if (!ended || quoted || cell !== "" || row.length > 0) {
    row.push(cell);
    if (!(cut && records.length > 0) && !quoted) records.push(row);
  }
  const columns = (records[0] ?? []).map((c) => c.trim());
  const rows = records.slice(1, 1 + maxRows).map((r) => {
    const out = r.map((c) => c.trim());
    while (out.length < columns.length) out.push("");
    return out.length > columns.length ? out.slice(0, columns.length) : out;
  });
  return { columns, rows };
}

// Kind is what a column looks like it holds, in the server's terms plus
// the ones that matter when picking an answer: a date, an ID that is
// different in every row, or nothing at all.
export type Kind = "number" | "yesno" | "category" | "text" | "date" | "id" | "empty";

export type Column = {
  name: string;
  kind: Kind;
  // filled is how many cells hold something; distinct how many different
  // values they hold.
  filled: number;
  distinct: number;
  // top are the most common values, most common first, at most 5.
  top: { value: string; count: number }[];
  // For numbers.
  min?: number;
  max?: number;
  mean?: number;
  // For text: words per cell.
  words?: number;
  // samples are a few different values, in the order they appear.
  samples: string[];
  // alike groups values that are the same but for case or spacing, such
  // as "Yes" and "yes": each group is one answer spelled several ways.
  alike: string[][];
};

const yes = new Set(["yes", "y", "true", "t", "1"]);
const no = new Set(["no", "n", "false", "f", "0"]);
const idName = /(^|[_\s-])(id|no|nr|num|number|code|key|uuid|guid)$|^id[_\s-]|^(id|uuid|guid)$/i;
const dateName = /date|time|^day$|^week$|^month$|period|created|closed|ordered|_at$|tanggal|tgl/i;

// number reads a cell the way the server does: 1,200 and 1 200 are
// numbers too.
export function number(v: string): number | undefined {
  if (v === "") return undefined;
  let f = Number(v);
  if (Number.isNaN(f)) f = Number(v.replace(/[,\s_]/g, ""));
  if (v.replace(/[,\s_]/g, "") === "" || Number.isNaN(f) || !Number.isFinite(f)) return undefined;
  return f;
}

const dateRe = /^(\d{4}-\d{1,2}-\d{1,2}|\d{1,2}[/.-]\d{1,2}[/.-]\d{2,4}|\d{4}\/\d{1,2}\/\d{1,2})([ T]\d{1,2}:\d{2}(:\d{2})?(\.\d+)?(Z|[+-]\d{2}:?\d{2})?)?$/;
export const isDate = (v: string) => dateRe.test(v);

// profile describes each column from the rows given.
export function profile({ columns, rows }: Parsed): Column[] {
  return columns.map((name, j) => {
    const count = new Map<string, number>();
    let filled = 0;
    let numbers = true;
    let yesno = true;
    let dates = 0;
    let sum = 0;
    let min = Infinity;
    let max = -Infinity;
    let words = 0;
    let length = 0;
    let sentences = 0;
    const samples: string[] = [];
    for (const r of rows) {
      const v = r[j] ?? "";
      if (v === "") continue;
      filled++;
      count.set(v, (count.get(v) ?? 0) + 1);
      if (samples.length < 3 && !samples.includes(v)) samples.push(v);
      const f = number(v);
      if (f === undefined) numbers = false;
      else { sum += f; min = Math.min(min, f); max = Math.max(max, f); }
      const low = v.toLowerCase();
      if (!yes.has(low) && !no.has(low)) yesno = false;
      if (isDate(v)) dates++;
      const w = v.split(/[^\p{L}\p{N}]+/u).filter(Boolean).length;
      words += w;
      length += v.length;
      if (w >= 3) sentences++;
    }
    const distinct = count.size;
    const top = [...count.entries()].map(([value, c]) => ({ value, count: c })).sort((a, b) => b.count - a.count || a.value.localeCompare(b.value)).slice(0, 5);
    let kind: Kind;
    if (filled === 0) kind = "empty";
    else if (dates * 10 >= filled * 9 && !(numbers && !dateName.test(name))) kind = "date";
    else if (filled >= 20 && distinct === filled && (!numbers || idName.test(name))) kind = "id";
    else if (numbers) kind = "number";
    else if (yesno) kind = "yesno";
    else if (distinct <= 200 && length / filled <= 30 && !(sentences * 2 > filled)) kind = "category";
    else kind = "text";
    const byKey = new Map<string, string[]>();
    for (const v of count.keys()) {
      const k = v.toLowerCase().replace(/[\s_-]+/g, " ");
      byKey.set(k, [...(byKey.get(k) ?? []), v]);
    }
    const alike = [...byKey.values()].filter((g) => g.length > 1).slice(0, 5);
    const col: Column = { name, kind, filled, distinct, top, samples, alike };
    if (kind === "number" && filled > 0) { col.min = min; col.max = max; col.mean = sum / filled; }
    if (kind === "text") col.words = words / filled;
    return col;
  });
}

// Role is what the answer column is to the model type picked, as in
// Architecture.target: "" for the usual answer, item, value or action.
export type Role = "" | "none" | "item" | "value" | "action";

// Answer says what a model would answer with this column as the answer,
// and what's wrong with that, when something is.
export type Answer = {
  // what is a short name for the kind of answer: "yes or no".
  what: string;
  // detail follows it: "62 of 100 rows say yes".
  detail: string;
  // level is bad when the column can't be an answer, warn when it can
  // but poorly.
  level?: "warn" | "bad";
  // problem says why, in plain words, when level is set.
  problem?: string;
};

const fmt = (n: number) => n.toLocaleString(undefined, { maximumFractionDigits: Math.abs(n) >= 100 ? 0 : 2 });

export function answer(c: Column, role: Role = ""): Answer {
  const share = (v: string) => Math.round(100 * (c.top.find((t) => t.value === v)?.count ?? 0) / Math.max(c.filled, 1));
  if (c.filled === 0) return { what: "nothing", detail: "The column is empty.", level: "bad", problem: "Every cell is empty, so there is nothing to learn. Pick another column." };
  if (role === "item") return { what: "a ranked list of items", detail: `${c.distinct.toLocaleString()} different ${c.name} values in these rows.`, ...(c.kind === "number" || c.distinct < 2 ? { level: "warn" as const, problem: "An item column holds a name or code that comes up again and again, one per row." } : {}) };
  if (role === "value") return c.kind === "number"
    ? { what: "a number ahead in time", detail: `${c.name} runs from ${fmt(c.min!)} to ${fmt(c.max!)} here.` }
    : { what: "a number ahead in time", detail: `${c.name} doesn't read as numbers.`, level: "bad", problem: "The value to forecast must be a number in every row, like units sold. Pick a number column." };
  if (role === "action") return { what: "the best action and what each one brings", detail: `${c.distinct.toLocaleString()} different actions in these rows.`, ...(c.distinct < 2 ? { level: "bad" as const, problem: "Only one action was tried, so there is nothing to compare. The action column holds what was tried, like the offer shown." } : {}) };
  // Long group names read as text but are still a few answers to pick from.
  const kind = c.kind === "text" && c.distinct <= 50 ? "category" : c.kind;
  if ((kind === "category" || kind === "yesno") && c.alike.length > 0 && c.distinct >= 2) {
    const g = c.alike[0]!;
    return { what: `one of ${c.distinct} answers`, detail: `${g.map((v) => `“${v}”`).join(" and ")} count as different answers.`, level: "warn", problem: `They look like the same answer spelled differently. Make them the same in the file, or the model learns them as separate answers.` };
  }
  switch (kind) {
    case "id":
      return { what: "one of thousands of values", detail: `${c.name} is different in every row.`, level: "bad", problem: "It looks like an ID. Every row has its own value, so there is nothing to learn. Pick the column that holds what you want to know." };
    case "date":
      return { what: "a date", detail: `${c.name} holds dates.`, level: "bad", problem: "A model here doesn't answer with a date. To forecast what happens on a day, pick the value column as the answer, or choose Forecast over time." };
    case "text":
      return { what: "free text", detail: `About ${Math.round(c.words ?? 0)} words per row.`, level: "bad", problem: "A model here can't write text as an answer. Pick a column that holds a yes or no, a group, or a number." };
    case "yesno": {
      const y = c.top.find((t) => yes.has(t.value.toLowerCase()))?.value ?? c.top[0]!.value;
      return { what: "yes or no", detail: `${share(y)} of 100 rows say ${y}; a model that beats that has learned something.`, ...(c.distinct < 2 ? { level: "bad" as const, problem: `Every row says ${c.top[0]!.value}, so there is nothing to tell apart.` } : share(y) >= 95 || share(y) <= 5 ? { level: "warn" as const, problem: "One answer is rare. It can still learn, but it needs many more of the rare rows to answer them well." } : {}) };
    }
    case "number": {
      if (c.distinct === 1) return { what: "a number", detail: `Every row says ${c.top[0]!.value}.`, level: "bad", problem: "Every row holds the same number, so there is nothing to learn." };
      const whole = c.distinct <= 10 && c.top.every((t) => Number.isInteger(number(t.value)));
      return whole
        ? { what: `one of ${c.distinct} values`, detail: `${c.name} is ${c.top.map((t) => t.value).join(", ")}${c.distinct > 5 ? "…" : ""}. It reads as groups to pick from, not as an amount.` }
        : { what: "a number", detail: `From ${fmt(c.min!)} to ${fmt(c.max!)}, usually about ${fmt(c.mean!)}.` };
    }
    case "category": {
      const low = c.top.length >= 2 ? c.top[c.top.length - 1]! : undefined;
      const two = c.distinct === 2;
      const what = two ? `${c.top[0]!.value} or ${c.top[1]!.value}` : `one of ${c.distinct} groups`;
      const detail = two
        ? `${share(c.top[0]!.value)} of 100 rows say ${c.top[0]!.value}.`
        : `Most often ${c.top.slice(0, 3).map((t) => `${t.value} (${t.count.toLocaleString()})`).join(", ")}${c.distinct > 3 ? ` and ${c.distinct - 3} more` : ""}.`;
      if (c.distinct < 2) return { what, detail: `Every row says ${c.top[0]!.value}.`, level: "bad", problem: "Every row has the same answer, so there is nothing to tell apart." };
      if (c.distinct > 50) return { what, detail, level: "warn", problem: `${c.distinct} different answers is a lot. The model keeps the most common ones; the rest read as “something else”. If these are names or codes, pick another column.` };
      if (low && low.count < 20 && c.filled >= 100) return { what, detail, level: "warn", problem: `Only ${low.count} rows say ${low.value}. Aim for 100 or more of each answer.` };
      return { what, detail };
    }
    default:
      return { what: "nothing", detail: "", level: "bad", problem: "The column is empty." };
  }
}

// guess picks the answer column to start with: the one a column's role
// names, else the last column that could be an answer.
export function guess(cols: Column[], role: Role = ""): string {
  const by = (re: RegExp) => cols.find((c) => re.test(c.name) && c.kind !== "id" && c.kind !== "date")?.name;
  if (role === "item") return by(/item|product|sku|title|movie|film|song|track|book|article|course|dish|recipe/i) ?? cols[cols.length - 1]?.name ?? "";
  if (role === "action") return by(/offer|action|treatment|variant|choice|campaign|\barm\b|promo/i) ?? cols[cols.length - 1]?.name ?? "";
  if (role === "value") return [...cols].reverse().find((c) => c.kind === "number")?.name ?? cols[cols.length - 1]?.name ?? "";
  const fine = [...cols].reverse().find((c) => !answer(c).level || answer(c).level === "warn");
  return fine?.name ?? cols[cols.length - 1]?.name ?? "";
}

// describe says what a column holds, in a few words.
export function describe(c: Column): string {
  switch (c.kind) {
    case "empty": return "empty";
    case "id": return "an ID, different in every row";
    case "date": return "dates";
    case "number": return c.min !== undefined && c.max !== undefined && c.min !== c.max ? `numbers, ${fmt(c.min)} to ${fmt(c.max)}` : "numbers";
    case "yesno": return "yes or no";
    case "text": return `text, about ${Math.round(c.words ?? 0)} words`;
    case "category": return c.distinct === 1 ? `always ${c.top[0]!.value}` : `one of ${c.distinct.toLocaleString()} values`;
  }
}

// tips says what to know about the columns that aren't the answer.
export function tips(cols: Column[], target: string): { column: string; text: string }[] {
  const out: { column: string; text: string }[] = [];
  for (const c of cols) {
    if (c.name === target) continue;
    if (c.kind === "empty") out.push({ column: c.name, text: "is empty. It tells the model nothing." });
    else if (c.kind === "id") out.push({ column: c.name, text: "looks like an ID. It tells the model nothing about a new row; it does no harm, but don't expect it to help." });
    else if (c.distinct === 1 && (c.kind === "category" || c.kind === "yesno")) out.push({ column: c.name, text: `is ${c.top[0]!.value} in every row, so it tells the model nothing.` });
  }
  return out;
}
