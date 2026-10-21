// Chats with a language model, kept in this browser only: one list per
// model, newest first, for 30 days.

export type ChatTurn = { role: "user" | "assistant"; content: string; note?: string };

// Answer is how the model answers in one chat; unset fields use the
// model's defaults.
export type Answer = { instructions?: string; temperature?: number; maxTokens?: number; seed?: number };

export type SavedChat = { id: string; title: string; updated: number; turns: ChatTurn[]; answer?: Answer };

const keep = 30 * 24 * 3600 * 1000;
const most = 30;
const key = (modelId: string) => `mimir.chats.${modelId}`;

export function loadChats(modelId: string): SavedChat[] {
  try {
    const raw = localStorage.getItem(key(modelId));
    const all = raw ? (JSON.parse(raw) as SavedChat[]) : [];
    const since = Date.now() - keep;
    return all.filter((c) => c.updated >= since && Array.isArray(c.turns));
  } catch {
    return [];
  }
}

export function saveChat(modelId: string, chat: SavedChat): SavedChat[] {
  const all = [chat, ...loadChats(modelId).filter((c) => c.id !== chat.id)].slice(0, most);
  store(modelId, all);
  return all;
}

export function deleteChat(modelId: string, id: string): SavedChat[] {
  const all = loadChats(modelId).filter((c) => c.id !== id);
  store(modelId, all);
  return all;
}

function store(modelId: string, all: SavedChat[]) {
  try {
    localStorage.setItem(key(modelId), JSON.stringify(all));
  } catch {
    // Storage full or off: the chat still works, it just isn't kept.
  }
}

// titleOf names a chat after its first message.
export function titleOf(turns: ChatTurn[]): string {
  const first = turns.find((t) => t.role === "user")?.content.trim().replace(/\s+/g, " ") ?? "New chat";
  return first.length > 48 ? first.slice(0, 47) + "…" : first;
}

// Word choice and answer length, in plain words, as model settings.
export const wordChoices = [
  { value: "0", label: "Steady" },
  { value: "0.7", label: "Varied" },
  { value: "1.2", label: "Wild" },
];
export const lengths = [
  { value: "48", label: "A line" },
  { value: "256", label: "A paragraph" },
  { value: "768", label: "A page" },
];

// nearest picks the choice closest to v, so a custom value still shows
// which preset it is near.
export function nearest(choices: { value: string }[], v: number): string {
  return choices.reduce((best, c) => (Math.abs(Number(c.value) - v) < Math.abs(Number(best.value) - v) ? c : best)).value;
}

// pages says how many pages of text a number of tokens is, about 340 each.
export function pages(tokens: number): number {
  return Math.max(1, Math.round(tokens / 340));
}
