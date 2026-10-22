// The PostgreSQL connection the setup screen asks for, field by field,
// and the URL the server takes for it.
export type Postgres = {
  host: string;
  port: number;
  user: string;
  password: string;
  database: string;
  // sslmode: "prefer" tries TLS and falls back, "require" insists on it,
  // "disable" is for a server on the same machine or a private network.
  ssl: "prefer" | "require" | "disable";
};

export const defaultPostgres: Postgres = { host: "localhost", port: 5432, user: "mimir", password: "", database: "mimir", ssl: "prefer" };

// postgresUrl writes the fields as a postgres:// URL, escaping each part so
// a password with @, : or / in it stays a password.
export function postgresUrl(p: Postgres): string {
  const host = p.host.trim().includes(":") && !p.host.trim().startsWith("[") ? `[${p.host.trim()}]` : p.host.trim();
  const auth = p.password === "" ? encodeURIComponent(p.user.trim()) : `${encodeURIComponent(p.user.trim())}:${encodeURIComponent(p.password)}`;
  return `postgres://${auth}@${host}:${p.port}/${encodeURIComponent(p.database.trim())}?sslmode=${p.ssl}`;
}

// postgresReady says whether the fields are filled in enough to try.
export function postgresReady(p: Postgres): boolean {
  return p.host.trim() !== "" && p.user.trim() !== "" && p.database.trim() !== "" && Number.isInteger(p.port) && p.port > 0 && p.port < 65536;
}

// newSecretKey makes a 32-byte key, as base64, in the browser.
export function newSecretKey(): string {
  const b = new Uint8Array(32);
  crypto.getRandomValues(b);
  return btoa(String.fromCharCode(...b));
}
