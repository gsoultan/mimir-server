import { id } from "./id";

// The web app's languages. English is the source: every piece of text in the
// code is written in English and wrapped in t(), and that English text is the
// key other languages are looked up by. Text a language has no entry for
// shows in English, so new screens work before they are translated.
//
// To add text: write it in English inside t("..."), with {name} for values
// that change: t("Try {name}", { name: model.name }). Then add its
// translation to id.ts; `bun run i18n:check` lists text still missing one.
//
// To add a language: copy id.ts, translate it, and list it below.

export type Lang = "en" | "id";

export const languages: { value: Lang; label: string }[] = [
  { value: "en", label: "English" },
  { value: "id", label: "Bahasa Indonesia" },
];

const catalogs: Record<Lang, Record<string, string>> = { en: {}, id };

const storageKey = "mimir.lang";

function pick(): Lang {
  try {
    const saved = localStorage.getItem(storageKey);
    if (saved === "en" || saved === "id") return saved;
  } catch {
    // Storage can be off in private windows; fall back to the browser.
  }
  const asked = typeof navigator === "undefined" ? [] : navigator.languages ?? [navigator.language];
  for (const l of asked) {
    if (typeof l !== "string") continue;
    const base = l.toLowerCase().split("-")[0];
    if (base === "id" || base === "in" || base === "ms") return "id";
    if (base === "en") return "en";
  }
  return "en";
}

// The language is chosen once, when the page loads, so text can be
// translated anywhere, even in lists defined outside a component. Changing
// it reloads the page.
export const lang: Lang = pick();

if (typeof document !== "undefined") document.documentElement.lang = lang;

// locale is the BCP 47 tag for dates and numbers.
export const locale = lang === "id" ? "id-ID" : "en";

export function setLang(next: Lang) {
  if (next === lang) return;
  try {
    localStorage.setItem(storageKey, next);
  } catch {
    // Without storage the choice lasts until the page reloads.
  }
  location.reload();
}

// t translates English text into the chosen language and fills in {name}
// placeholders from vars.
export function t(text: string, vars?: Record<string, string | number | bigint | undefined | null>): string {
  const out = catalogs[lang][text] || text;
  if (!vars) return out;
  return out.replace(/\{(\w+)\}/g, (whole, name: string) => (name in vars ? String(vars[name] ?? "") : whole));
}
