import type { DataType } from "./templates";
import { t } from "../i18n";

// What a model can be built to do, in plain words, and how it gets built
// here: from a template or data with no code, from a model file trained
// elsewhere, or with a developer's own code on a worker.
export type Ready = "ready" | "file" | "code";

export type GoalId = "yes_no" | "group" | "number" | "segments" | "write" | "recommend" | "forecast" | "odd_one" | "best_action" | "find_in_photo";

export type Goal = {
  id: GoalId;
  title: string;
  example: string;
  intro: string;
  ready: Ready;
  // For a code goal: what it takes, example data to show and download,
  // and the architecture name a starter project uses. For a ready goal
  // with an arch, that built-in model type teaches it from a table.
  steps?: { label: string; hint: string }[];
  // For a ready goal with an arch: what it wants, how long, and what an
  // answer reads like.
  learn?: { amount: string; time: string; answer: string; data: string };
  sample?: { name: string; columns: string[]; rows: string[][] };
  arch?: string;
  // data is what a ready goal learns from, a table unless set.
  data?: DataType;
};

export const goals: Goal[] = [
  { id: "yes_no", title: t("Yes or no"), example: t("Will this customer leave?"), ready: "ready", intro: t("The model reads one example and answers yes or no, and how sure it is.") },
  { id: "group", title: t("Pick a group"), example: t("Which defect is this?"), ready: "ready", intro: t("The model reads one example and says which of a few groups it belongs to, and how sure it is.") },
  { id: "number", title: t("A number"), example: t("How many units next week?"), ready: "ready", intro: t("The model reads one example and answers with a number, like units, hours or a price.") },
  { id: "write", title: t("Write or chat"), example: t("Answer support emails"), ready: "file", intro: t("A language model writes text: it answers questions, drafts replies and carries on a chat. These are trained elsewhere on a great deal of text; you bring one as a GGUF file.") },
  {
    id: "segments", title: t("Find groups"), example: t("What kinds of customers do we have?"), ready: "ready", arch: "find-groups",
    intro: t("The model sorts your rows into a few groups of similar ones and names each after what sets it apart. It needs no answers to learn from."),
    learn: { amount: t("500 rows or more"), time: t("seconds to teach"), answer: t("“Group 2: high spend”"), data: t("Upload a table of the things to group, one row each, with the columns that describe them. No answer column needed.") },
    sample: { name: "customers.csv", columns: ["customer", "spend", "visits", "plan"], rows: [["c-1042", "12.40", "1", "basic"], ["c-2210", "210.00", "4", "pro"], ["c-3307", "48.90", "19", "basic"], ["c-4120", "9.80", "2", "basic"], ["c-5031", "189.50", "5", "pro"]] },
  },
  {
    id: "recommend", title: t("Recommend"), example: t("What else will they like?"), ready: "ready", arch: "recommend",
    intro: t("Given a customer, the model lists the items they're most likely to want next, learned from who picked what."),
    learn: { amount: t("10,000 rows or more"), time: t("seconds to a minute to teach"), answer: t("“1. sencha, 2. teapot-s, 3. assam…”"), data: t("Upload a table with one row per purchase, click or rating: who, and which item. Say which column is the item; a column like customer says who.") },
    sample: { name: "purchases.csv", columns: ["customer", "item", "when"], rows: [["c-1042", "earl-grey", "2026-09-02"], ["c-1042", "teapot-s", "2026-09-02"], ["c-2210", "sencha", "2026-09-05"], ["c-2210", "earl-grey", "2026-09-11"], ["c-3307", "teapot-l", "2026-09-12"]] },
  },
  {
    id: "forecast", title: t("Forecast over time"), example: t("Sales each day next month"), ready: "ready", arch: "forecast",
    intro: t("From what happened each day so far, the model says what comes next: ask it for any day up to a month ahead."),
    learn: { amount: t("a year of days, or 3 months at least"), time: t("under a minute to teach"), answer: t("“About 214 units, 7 days ahead”"), data: t("Upload a table with one row per day (or hour, week or month): the date and the value. A column like store gives each its own forecast. Say which column is the value.") },
    sample: { name: "daily-sales.csv", columns: ["day", "store", "units"], rows: [["2026-09-01", "north", "182"], ["2026-09-01", "south", "96"], ["2026-09-02", "north", "175"], ["2026-09-02", "south", "101"], ["2026-09-03", "north", "240"]] },
  },
  {
    id: "odd_one", title: t("Spot the odd one"), example: t("Unusual machine readings"), ready: "ready", arch: "odd-one",
    intro: t("The model learns what normal looks like, then scores how unusual each new example is. It needs no answers to learn from."),
    learn: { amount: t("5,000 rows or more"), time: t("seconds to teach"), answer: t("“0.81: unlike anything usual”"), data: t("Upload a table of readings from when things were fine, one row each. No answer column needed.") },
    sample: { name: "readings.csv", columns: ["machine", "temp_c", "vibration", "amps"], rows: [["press-1", "61.2", "0.31", "12.4"], ["press-1", "60.8", "0.29", "12.1"], ["press-2", "63.0", "0.35", "12.9"], ["press-2", "62.4", "0.33", "12.6"], ["press-3", "59.9", "0.28", "11.8"]] },
  },
  {
    id: "best_action", title: t("Pick the best action"), example: t("Which offer should this customer get?"), ready: "ready", arch: "best-action",
    intro: t("From a log of what was tried and how it turned out, the model says which action is likely to turn out best for a new case, and what each one is expected to bring."),
    learn: { amount: t("a few thousand rows, with each action tried often"), time: t("seconds to a minute to teach"), answer: t("“1. free-delivery: about 0.42 expected”"), data: t("Upload a table with one row per time something was tried: who or what it was for, which action was taken, and how it turned out (a number, or yes and no). Say which column is the action.") },
    sample: { name: "offers.csv", columns: ["customer_age", "plan", "offer", "converted"], rows: [["24", "basic", "discount", "yes"], ["58", "pro", "free-delivery", "yes"], ["31", "basic", "newsletter", "no"], ["63", "basic", "discount", "no"], ["27", "pro", "free-delivery", "no"]] },
  },
  {
    id: "find_in_photo", title: t("Find it in a photo"), example: t("Where is the scratch?"), ready: "ready", arch: "find-things", data: "boxes",
    intro: t("The model finds things in a photo and says where each one is, with a box around it."),
    learn: { amount: t("300 photos or more, 50 boxes of each kind"), time: t("5 to 15 minutes to teach"), answer: t("“Scratch, 0.91, here” with a box"), data: t("Upload one ZIP of the photos and a boxes.csv with one row per thing: the photo, what it is, and its box in pixels (left, top, width, height). A photo with nothing in it gets a row with what left empty.") },
    sample: { name: "boxes.csv", columns: ["photo", "what", "left", "top", "width", "height"], rows: [["photos/0001.jpg", "scratch", "112", "40", "64", "18"], ["photos/0002.jpg", "dent", "300", "210", "48", "44"], ["photos/0002.jpg", "scratch", "20", "88", "90", "12"]] },
  },
];

export const goalOf = (id: string) => goals.find((g) => g.id === id);

// The template kind each no-code goal matches.
export const isTemplateKind = (id: GoalId): id is "yes_no" | "group" | "number" => id === "yes_no" || id === "group" || id === "number";

// What one example looks like, as asked on a goal's guide.
export const shapes: { value: DataType; label: string }[] = [
  { value: "table", label: t("A row in a table") },
  { value: "text", label: t("Some text") },
  { value: "photos", label: t("A photo") },
  { value: "photo_table", label: t("Photo + details") },
  { value: "sounds", label: t("A sound") },
];

// How much data each shape wants, how long teaching takes, and what an
// answer reads like, per goal kind.
export function needs(shape: DataType, kind: "yes_no" | "group" | "number") {
  const per = kind === "group" ? t(" per group") : "";
  const amount = {
    table: kind === "group" ? t("200 rows per group") : t("1,000 rows to start"),
    text: t("500 texts{per} to start", { per }),
    photos: t("100 photos{per} to start, 500 good", { per }),
    photo_table: t("300 rows with photos{per}", { per }),
    sounds: t("100 sounds{per} to start, 500 good", { per }),
    boxes: t("300 photos with boxes"),
  }[shape];
  const time = shape === "table" ? t("about a minute to teach") : shape === "text" ? t("a few minutes to teach") : t("5 to 15 minutes to teach");
  const answer = kind === "yes_no"
    ? t("“Yes, sure about 87 of 100”")
    : kind === "group"
      ? shape === "sounds" ? t("“Worn bearing, sure about 91 of 100”") : shape === "photos" || shape === "photo_table" ? t("“Scratch, sure about 93 of 100”") : t("“Billing, sure about 81 of 100”")
      : t("“About 214 units”");
  return { amount, time, answer };
}

// How to bring one's own examples, per shape.
export const ownData: Record<DataType, string> = {
  table: t("Upload a spreadsheet as CSV, or connect a database, and say which column is the answer."),
  text: t("Upload a table with the text in one column and the answer in another."),
  photos: t("Put the photos in one folder per answer, ZIP them, upload. The folder name is the answer."),
  photo_table: t("Upload one ZIP with a data.csv and the photos it names. Say which column is the answer."),
  sounds: t("Put the WAV recordings in one folder per answer, ZIP them, upload. The folder name is the answer; each sound is heard as one second."),
  boxes: t("Upload one ZIP of the photos and a boxes.csv saying where each thing in them is."),
};

// csv writes a sample as a CSV file's text.
export function csv(s: { columns: string[]; rows: string[][] }) {
  return [s.columns, ...s.rows].map((r) => r.join(",")).join("\n") + "\n";
}
