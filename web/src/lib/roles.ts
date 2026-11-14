import { Role } from "../api/gen/mimir/v1/access_pb";
import { t } from "../i18n";

// Plain words for each role, shown wherever a role is picked or listed.
export const roles: { value: Role; label: string; blurb: string }[] = [
  { value: Role.VIEWER, label: t("Viewer"), blurb: t("Can look at models and ask them questions.") },
  { value: Role.EDITOR, label: t("Editor"), blurb: t("Can also add, rename and delete models.") },
  { value: Role.ADMIN, label: t("Admin"), blurb: t("Can also manage their team's people and webhooks, and read its audit log.") },
  { value: Role.OWNER, label: t("Owner"), blurb: t("Can do everything: every team, admins, workers and server settings.") },
];

export function roleLabel(r: Role): string {
  return roles.find((x) => x.value === r)?.label ?? t("Unknown");
}
