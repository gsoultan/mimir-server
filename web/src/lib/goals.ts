import type { DataType } from "./templates";

// What a model can be built to do, in plain words, and how it gets built
// here: from a template or data with no code, from a model file trained
// elsewhere, or with a developer's own code on a worker.
export type Ready = "ready" | "file" | "code";

export type GoalId = "yes_no" | "group" | "number" | "segments" | "write" | "recommend" | "forecast" | "odd_one" | "find_in_photo";

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
};

export const goals: Goal[] = [
  { id: "yes_no", title: "Yes or no", example: "Will this customer leave?", ready: "ready", intro: "The model reads one example and answers yes or no, and how sure it is." },
  { id: "group", title: "Pick a group", example: "Which defect is this?", ready: "ready", intro: "The model reads one example and says which of a few groups it belongs to, and how sure it is." },
  { id: "number", title: "A number", example: "How many units next week?", ready: "ready", intro: "The model reads one example and answers with a number, like units, hours or a price." },
  { id: "write", title: "Write or chat", example: "Answer support emails", ready: "file", intro: "A language model writes text: it answers questions, drafts replies and carries on a chat. These are trained elsewhere on a great deal of text; you bring one as a GGUF file." },
  {
    id: "segments", title: "Find groups", example: "What kinds of customers do we have?", ready: "ready", arch: "find-groups",
    intro: "The model sorts your rows into a few groups of similar ones and names each after what sets it apart. It needs no answers to learn from.",
    learn: { amount: "500 rows or more", time: "seconds to teach", answer: "“Group 2: high spend”", data: "Upload a table of the things to group, one row each, with the columns that describe them. No answer column needed." },
    sample: { name: "customers.csv", columns: ["customer", "spend", "visits", "plan"], rows: [["c-1042", "12.40", "1", "basic"], ["c-2210", "210.00", "4", "pro"], ["c-3307", "48.90", "19", "basic"], ["c-4120", "9.80", "2", "basic"], ["c-5031", "189.50", "5", "pro"]] },
  },
  {
    id: "recommend", title: "Recommend", example: "What else will they like?", ready: "ready", arch: "recommend",
    intro: "Given a customer, the model lists the items they're most likely to want next, learned from who picked what.",
    learn: { amount: "10,000 rows or more", time: "seconds to a minute to teach", answer: "“1. sencha, 2. teapot-s, 3. assam…”", data: "Upload a table with one row per purchase, click or rating: who, and which item. Say which column is the item; a column like customer says who." },
    sample: { name: "purchases.csv", columns: ["customer", "item", "when"], rows: [["c-1042", "earl-grey", "2026-09-02"], ["c-1042", "teapot-s", "2026-09-02"], ["c-2210", "sencha", "2026-09-05"], ["c-2210", "earl-grey", "2026-09-11"], ["c-3307", "teapot-l", "2026-09-12"]] },
  },
  {
    id: "forecast", title: "Forecast over time", example: "Sales each day next month", ready: "ready", arch: "forecast",
    intro: "From what happened each day so far, the model says what comes next: ask it for any day up to a month ahead.",
    learn: { amount: "a year of days, or 3 months at least", time: "under a minute to teach", answer: "“About 214 units, 7 days ahead”", data: "Upload a table with one row per day (or hour, week or month): the date and the value. A column like store gives each its own forecast. Say which column is the value." },
    sample: { name: "daily-sales.csv", columns: ["day", "store", "units"], rows: [["2026-09-01", "north", "182"], ["2026-09-01", "south", "96"], ["2026-09-02", "north", "175"], ["2026-09-02", "south", "101"], ["2026-09-03", "north", "240"]] },
  },
  {
    id: "odd_one", title: "Spot the odd one", example: "Unusual machine readings", ready: "ready", arch: "odd-one",
    intro: "The model learns what normal looks like, then scores how unusual each new example is. It needs no answers to learn from.",
    learn: { amount: "5,000 rows or more", time: "seconds to teach", answer: "“0.81: unlike anything usual”", data: "Upload a table of readings from when things were fine, one row each. No answer column needed." },
    sample: { name: "readings.csv", columns: ["machine", "temp_c", "vibration", "amps"], rows: [["press-1", "61.2", "0.31", "12.4"], ["press-1", "60.8", "0.29", "12.1"], ["press-2", "63.0", "0.35", "12.9"], ["press-2", "62.4", "0.33", "12.6"], ["press-3", "59.9", "0.28", "11.8"]] },
  },
  {
    id: "find_in_photo", title: "Find it in a photo", example: "Where is the scratch?", ready: "code", arch: "find-scratch",
    intro: "The model finds things in a photo and says where each one is, with a box around it.",
    steps: [
      { label: "Mark the photos", hint: "Photos plus a table with one row per thing found: photo, what it is, and its box (left, top, width, height). 300 photos or more works well." },
      { label: "Write the model", hint: "Go code with the worker kit, often calling a detector you trained elsewhere. The starting project reads the photos and the boxes." },
      { label: "Connect it", hint: "Run it on any machine with a worker token. It shows up here within seconds." },
      { label: "Teach it and try it", hint: "Teach it or upload what you trained, then send a photo and see the boxes." },
    ],
    sample: { name: "boxes.csv", columns: ["photo", "what", "left", "top", "width", "height"], rows: [["photos/0001.jpg", "scratch", "112", "40", "64", "18"], ["photos/0002.jpg", "dent", "300", "210", "48", "44"], ["photos/0002.jpg", "scratch", "20", "88", "90", "12"]] },
  },
];

export const goalOf = (id: string) => goals.find((g) => g.id === id);

// The template kind each no-code goal matches.
export const isTemplateKind = (id: GoalId): id is "yes_no" | "group" | "number" => id === "yes_no" || id === "group" || id === "number";

// What one example looks like, as asked on a goal's guide.
export const shapes: { value: DataType; label: string }[] = [
  { value: "table", label: "A row in a table" },
  { value: "text", label: "Some text" },
  { value: "photos", label: "A photo" },
  { value: "photo_table", label: "Photo + details" },
];

// How much data each shape wants, how long teaching takes, and what an
// answer reads like, per goal kind.
export function needs(shape: DataType, kind: "yes_no" | "group" | "number") {
  const per = kind === "group" ? " per group" : "";
  const amount = {
    table: kind === "group" ? "200 rows per group" : "1,000 rows to start",
    text: `500 texts${per} to start`,
    photos: `100 photos${per} to start, 500 good`,
    photo_table: `300 rows with photos${per}`,
  }[shape];
  const time = shape === "table" ? "about a minute to teach" : shape === "text" ? "a few minutes to teach" : "5 to 15 minutes to teach";
  const answer = kind === "yes_no"
    ? "“Yes, sure about 87 of 100”"
    : kind === "group"
      ? shape === "photos" || shape === "photo_table" ? "“Scratch, sure about 93 of 100”" : "“Billing, sure about 81 of 100”"
      : "“About 214 units”";
  return { amount, time, answer };
}

// How to bring one's own examples, per shape.
export const ownData: Record<DataType, string> = {
  table: "Upload a spreadsheet as CSV, or connect a database, and say which column is the answer.",
  text: "Upload a table with the text in one column and the answer in another.",
  photos: "Put the photos in one folder per answer, ZIP them, upload. The folder name is the answer.",
  photo_table: "Upload one ZIP with a data.csv and the photos it names. Say which column is the answer.",
};

// csv writes a sample as a CSV file's text.
export function csv(s: { columns: string[]; rows: string[][] }) {
  return [s.columns, ...s.rows].map((r) => r.join(",")).join("\n") + "\n";
}
