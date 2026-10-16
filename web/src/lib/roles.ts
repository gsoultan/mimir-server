import { Role } from "../api/gen/mimir/v1/access_pb";

// Plain words for each role, shown wherever a role is picked or listed.
export const roles: { value: Role; label: string; blurb: string }[] = [
  { value: Role.VIEWER, label: "Viewer", blurb: "Can look at models and ask them questions." },
  { value: Role.EDITOR, label: "Editor", blurb: "Can also add, rename and delete models." },
  { value: Role.ADMIN, label: "Admin", blurb: "Can also manage people and read the audit log." },
  { value: Role.OWNER, label: "Owner", blurb: "Can do everything, including manage admins." },
];

export function roleLabel(r: Role): string {
  return roles.find((x) => x.value === r)?.label ?? "Unknown";
}
