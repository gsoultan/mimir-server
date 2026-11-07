import type { GetAboutResponse } from "../api/gen/mimir/v1/settings_pb";

// workers says how many are connected and whether they match the server.
export function workers(about: Pick<GetAboutResponse, "version" | "workerVersions">): string {
  const n = about.workerVersions.length;
  if (n === 0) return "None connected";
  const versions = new Set(about.workerVersions.map((v) => v.replace(/^v/, "")));
  if (versions.size === 1 && versions.has(about.version)) return `${n} connected · all on ${about.version}`;
  const behind = about.workerVersions.filter((v) => v.replace(/^v/, "") !== about.version).length;
  return `${n} connected · ${behind} on another version`;
}

// details is the version as text, for a bug report.
export function details(about: Pick<GetAboutResponse, "version" | "commit" | "modified" | "goVersion" | "schemaVersion" | "workerVersions" | "parts">): string {
  return [
    `mimir-server ${about.version}`,
    about.commit && `commit ${about.commit}${about.modified ? " (changed)" : ""}`,
    about.goVersion && `Go ${about.goVersion}`,
    `database schema ${about.schemaVersion}`,
    `workers: ${workers(about)}`,
    ...about.parts.map((p) => `${p.path} ${p.version}`),
  ].filter(Boolean).join("\n");
}
