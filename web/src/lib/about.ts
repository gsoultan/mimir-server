import type { GetAboutResponse } from "../api/gen/mimir/v1/settings_pb";
import { t } from "../i18n";

// workers says how many are connected and whether they match the server.
export function workers(about: Pick<GetAboutResponse, "version" | "workerVersions">): string {
  const n = about.workerVersions.length;
  if (n === 0) return t("None connected");
  const versions = new Set(about.workerVersions.map((v) => v.replace(/^v/, "")));
  if (versions.size === 1 && versions.has(about.version)) return t("{n} connected · all on {version}", { n, version: about.version });
  const behind = about.workerVersions.filter((v) => v.replace(/^v/, "") !== about.version).length;
  return t("{n} connected · {behind} on another version", { n, behind });
}

// details is the version as text, for a bug report.
export function details(about: Pick<GetAboutResponse, "version" | "commit" | "modified" | "goVersion" | "schemaVersion" | "workerVersions" | "parts">): string {
  return [
    `mimir-server ${about.version}`,
    about.commit && t("commit {commit}{n2}", { commit: about.commit, n2: about.modified ? t(" (changed)") : "" }),
    about.goVersion && `Go ${about.goVersion}`,
    t("database schema {schemaVersion}", { schemaVersion: about.schemaVersion }),
    `workers: ${workers(about)}`,
    ...about.parts.map((p) => `${p.path} ${p.version}`),
  ].filter(Boolean).join("\n");
}
