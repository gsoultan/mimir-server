// Lists text in the web app that has no Indonesian translation yet, and
// translations no screen uses any more. Run with `bun run i18n:check`.
// It exits non-zero only with --strict, so new screens can land in English
// first and be translated after.
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative } from "node:path";

import { id } from "../src/i18n/id";

const root = join(import.meta.dir, "..", "src");
// Words looked up by count(), duration() and bytes(), not written as t("...").
const dynamic = new Set(["answer", "answers", "thing", "things", "folder", "folders", "row", "rows", "column", "columns", "person", "people", "model", "models", "example", "examples", "number", "numbers", "day", "days", "salesperson", "salespeople", "set of examples", "sets of examples", "hour", "hours", "minute", "minutes", "bytes"]);

function files(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) return name === "gen" || name === "i18n" ? [] : files(path);
    return /\.tsx?$/.test(name) && !name.endsWith(".test.ts") && name !== "routeTree.gen.ts" ? [path] : [];
  });
}

const used = new Map<string, string>();
const call = /\bt\(\s*"((?:[^"\\]|\\.)*)"/g;
for (const file of files(root)) {
  for (const m of readFileSync(file, "utf8").matchAll(call)) {
    const text = JSON.parse(`"${m[1]}"`) as string;
    if (!used.has(text)) used.set(text, relative(root, file));
  }
}

const missing = [...used].filter(([text]) => !(text in id));
const unused = Object.keys(id).filter((text) => !used.has(text) && !dynamic.has(text));
const placeholders = (s: string) => [...s.matchAll(/\{(\w+)\}/g)].map((m) => m[1]).sort().join(",");
const broken = Object.entries(id).filter(([en, tr]) => placeholders(en) !== placeholders(tr));

for (const [text, file] of missing) console.log(`missing  ${file}: ${JSON.stringify(text)}`);
for (const text of unused) console.log(`unused   ${JSON.stringify(text)}`);
for (const [text] of broken) console.log(`placeholders differ  ${JSON.stringify(text)}`);
console.log(`${used.size} texts, ${missing.length} without Indonesian, ${unused.length} unused, ${broken.length} with different placeholders.`);
if (process.argv.includes("--strict") && (missing.length || unused.length || broken.length)) process.exit(1);
