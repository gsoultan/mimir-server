import {
  IconBox, IconDeviceLaptop, IconInfoCircle, IconFileText, IconHome, IconListCheck, IconMessageCircle, IconPlugConnected, IconRoute, IconSchool, IconSettings, IconSparkles, IconTrophy, IconUsers,
  type Icon,
} from "@tabler/icons-react";
import { t } from "../i18n";

export type NavTo = "/" | "/models" | "/try" | "/device" | "/training" | "/scoring" | "/visits" | "/documents" | "/benchmarks" | "/models/build" | "/apps" | "/users" | "/settings" | "/about";

// needs is the permission a person must have to see the item. also lists
// other paths that light the item up, like templates under Build.
export type NavItem = { to: NavTo; label: string; short: string; icon: Icon; needs?: string; also?: string[] };
export type NavGroup = { label?: string; items: NavItem[] };

// The menu: six places, no headings. Everything else opens from the page
// it belongs to (Build and Benchmarks from Models, Score a list and Plan visits from Try,
// People from Settings) and from search. People see only what they may open.
export const groups: NavGroup[] = [
  {
    items: [
      { to: "/", label: t("Home"), short: t("Home"), icon: IconHome },
      { to: "/models", label: t("Models"), short: t("Models"), icon: IconBox, also: ["/templates", "/benchmarks"] },
      { to: "/try", label: t("Try a model"), short: t("Try"), icon: IconMessageCircle, also: ["/scoring", "/visits"] },
      { to: "/documents", label: t("Documents"), short: t("Docs"), icon: IconFileText },
      { to: "/training", label: t("Training"), short: t("Train"), icon: IconSchool, needs: "models.write" },
      { to: "/apps", label: t("Connect apps"), short: t("Apps"), icon: IconPlugConnected },
    ],
  },
];

// Pinned at the bottom of the menu, above the account.
export const settingsItem: NavItem = { to: "/settings", label: t("Settings"), short: t("Settings"), icon: IconSettings, also: ["/users", "/workers"] };

// Pages search finds that the menu leaves out.
const extra: NavItem[] = [
  { to: "/models/build", label: t("Build a model"), short: t("Build"), icon: IconSparkles, needs: "models.write" },
  { to: "/scoring", label: t("Score a list"), short: t("Score"), icon: IconListCheck, needs: "models.write" },
  { to: "/visits", label: t("Plan visits"), short: t("Visits"), icon: IconRoute, needs: "models.read" },
  { to: "/benchmarks", label: t("Benchmarks"), short: t("Tests"), icon: IconTrophy, needs: "models.write" },
  { to: "/users", label: t("People"), short: t("People"), icon: IconUsers, needs: "users.read" },
];

export const deviceItem: NavItem = { to: "/device", label: t("On this device"), short: t("Device"), icon: IconDeviceLaptop };

// In the account menu: version, author and license.
export const aboutItem: NavItem = { to: "/about", label: t("About mimir"), short: t("About"), icon: IconInfoCircle };

// The flat list, for search.
export const nav: NavItem[] = [...groups.flatMap((g) => g.items), ...extra, settingsItem, deviceItem, aboutItem];

export function isActive(path: string, item: NavItem) {
  const match = (to: string) => (to === "/" ? path === "/" : path === to || path.startsWith(to + "/"));
  if (item.to === "/models" && ["/models/build", "/models/teach", "/models/new", "/models/finetune"].some((p) => path.startsWith(p))) return false;
  return match(item.to) || (item.also ?? []).some(match);
}
