import type { Template } from "../api/gen/mimir/v1/template_pb";
import { DatasetKind } from "../api/gen/mimir/v1/training_pb";

// What a template answers, in plain words.
export const kinds = [
  { id: "yes_no", label: "Yes or no", title: "Will something happen?", hint: "Yes or no. Will a customer leave, a machine fail, a payment be fraud." },
  { id: "group", label: "Pick a group", title: "Which group does it belong to?", hint: "One of a few answers. Which team, which grade, which reason." },
  { id: "number", label: "A number", title: "How much or how many?", hint: "A number. Units next week, energy per shift." },
] as const;

export type DataType = "table" | "text" | "photos" | "photo_table" | "sounds" | "boxes";

// What the examples look like: the cards people pick from first.
export const dataTypes: { id: DataType; title: string; chip: string; hint: string; examples: string; pic: string; scratch: string }[] = [
  {
    id: "table", title: "A table", chip: "A table",
    hint: "Rows and columns, like Excel, Google Sheets or a database. Columns hold numbers or short words.",
    examples: "sales, machine readings, HR records",
    pic: "plan   seats  left\nteam   25     no\nbasic  4      yes",
    scratch: "Upload my own table",
  },
  {
    id: "text", title: "Text", chip: "Text",
    hint: "Reviews, messages, notes or comments. Put each text in one cell of a table, next to its answer.",
    examples: "reviews, tickets, survey comments",
    pic: "review               mood\n“Fast, works great”   pos\n“Box was crushed”     neg",
    scratch: "Upload my own table with text",
  },
  {
    id: "photos", title: "Photos in folders", chip: "Photos",
    hint: "Only photos. One folder per answer, and the folder name is the answer.",
    examples: "defects, produce grades, documents",
    pic: "ok/      □ □ □\nscratch/ □ □\ndent/    □",
    scratch: "Upload my own photo folders",
  },
  {
    id: "photo_table", title: "Photos with details", chip: "Photos with details",
    hint: "A table where every row also has a photo, plus text or numbers. Upload one ZIP with both.",
    examples: "listings, claims, inspections",
    pic: "photo      title     price\n□ L-1001   iPhone..  0.35\n□ L-1002   Table..   1.02",
    scratch: "Upload my own ZIP of a table and photos",
  },
  {
    id: "sounds", title: "Sounds in folders", chip: "Sounds",
    hint: "Short recordings as WAV files. One folder per answer, and the folder name is the answer.",
    examples: "machine noises, bird calls, spoken words",
    pic: "normal/  ♪ ♪ ♪\nworn/    ♪ ♪\nbroken/  ♪",
    scratch: "Upload my own sound folders",
  },
  {
    id: "boxes", title: "Photos with boxes", chip: "Photos with boxes",
    hint: "Photos and a boxes.csv saying where each thing in them is: the photo, what it is, and its box in pixels.",
    examples: "scratches, packages on a shelf, people in a doorway",
    pic: "photo     what     left top\n0001.jpg  scratch  112  40\n0002.jpg  dent     300  210",
    scratch: "Upload my own photos and boxes",
  },
];

export const dataTypeOf = (id: string) => dataTypes.find((d) => d.id === id) ?? dataTypes[0]!;

export function kindLabel(kind: string) {
  return kinds.find((k) => k.id === kind)?.label ?? kind;
}

// Colours of each category's tile and pill, light and readable.
const categoryColors: Record<string, [string, string]> = {
  mfg: ["#FDF0DC", "#8A4B08"],
  qc: ["#E6ECF7", "#2B4A8A"],
  b2b: ["#E2F1EC", "#0A5246"],
  b2c: ["#F3E8F7", "#6A2F86"],
  fin: ["#FFF4D6", "#7A5A00"],
  hr: ["#FBE5E1", "#8E2F1F"],
  log: ["#E5F0F5", "#1F5F80"],
  sup: ["#EEF2EF", "#33403C"],
};
export const categoryColor = (id: string) => categoryColors[id] ?? ["#EEF2EF", "#33403C"];

// The colour of a column kind's pill.
export const columnKinds: Record<string, { label: string; color: string; hint: string }> = {
  number: { label: "Number", color: "blue", hint: "any number, no currency signs" },
  category: { label: "Category", color: "grape", hint: "a word from a short list, like a plan name" },
  yesno: { label: "Yes / no", color: "teal", hint: "yes or no, 1 or 0, true or false" },
  text: { label: "Text", color: "cyan", hint: "words as people wrote them, one per cell" },
  photo: { label: "Photo", color: "yellow", hint: "the photo's file name, like photos/1001.jpg" },
};

export const kindColor = (kind: string) => (kind === "yes_no" ? "teal" : kind === "number" ? "orange" : "indigo");

// The dataset kind a template's data makes.
export function datasetKindOf(t: Template | undefined): DatasetKind {
  switch (t?.data) {
    case "photos": return DatasetKind.PHOTOS;
    case "photo_table": return DatasetKind.PHOTO_TABLE;
  }
  return DatasetKind.TABLE;
}

// The data type a dataset holds, for picking which datasets fit.
export function dataTypeOfKind(k: DatasetKind): DataType[] {
  switch (k) {
    case DatasetKind.PHOTOS:
    case DatasetKind.MNIST:
      return ["photos"];
    case DatasetKind.PHOTO_TABLE:
      return ["photo_table"];
    case DatasetKind.SOUNDS:
      return ["sounds"];
    case DatasetKind.BOXES:
      return ["boxes"];
  }
  return ["table", "text"];
}

// count reads a number of things with its word: "1 row", "3 rows".
export function count(n: number | bigint, one: string, many = `${one}s`) {
  const v = Number(n);
  return `${v.toLocaleString()} ${v === 1 ? one : many}`;
}

// matches says whether a template fits a search: any word of the query in
// its name, question, category or columns.
export function matches(t: Template, categoryLabel: string, q: string) {
  const words = q.toLowerCase().split(/\s+/).filter(Boolean);
  if (words.length === 0) return true;
  const hay = [t.name, t.question, categoryLabel, t.sourceShort, ...t.columns.map((c) => c.name), ...t.folders.map((f) => f.name)].join(" ").toLowerCase();
  return words.every((w) => hay.includes(w));
}

// download saves bytes as a file.
export function download(name: string, type: string, data: Uint8Array | string) {
  const url = URL.createObjectURL(new Blob([data as BlobPart], { type }));
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}
