import {
  IconBox, IconDeviceLaptop, IconInfoCircle, IconFileText, IconHome, IconListCheck, IconMessageCircle, IconPlugConnected, IconSchool, IconSettings, IconSparkles, IconTrophy, IconUsers,
  type Icon,
} from "@tabler/icons-react";

export type NavTo = "/" | "/models" | "/try" | "/device" | "/training" | "/scoring" | "/documents" | "/benchmarks" | "/models/build" | "/apps" | "/users" | "/settings" | "/about";

// needs is the permission a person must have to see the item. also lists
// other paths that light the item up, like templates under Build.
export type NavItem = { to: NavTo; label: string; short: string; icon: Icon; needs?: string; also?: string[] };
export type NavGroup = { label?: string; items: NavItem[] };

// The menu: six places, no headings. Everything else opens from the page
// it belongs to (Build and Benchmarks from Models, Score a list from Try,
// People from Settings) and from search. People see only what they may open.
export const groups: NavGroup[] = [
  {
    items: [
      { to: "/", label: "Home", short: "Home", icon: IconHome },
      { to: "/models", label: "Models", short: "Models", icon: IconBox, also: ["/templates", "/benchmarks"] },
      { to: "/try", label: "Try a model", short: "Try", icon: IconMessageCircle, also: ["/scoring"] },
      { to: "/documents", label: "Documents", short: "Docs", icon: IconFileText },
      { to: "/training", label: "Training", short: "Train", icon: IconSchool, needs: "models.write" },
      { to: "/apps", label: "Connect apps", short: "Apps", icon: IconPlugConnected },
    ],
  },
];

// Pinned at the bottom of the menu, above the account.
export const settingsItem: NavItem = { to: "/settings", label: "Settings", short: "Settings", icon: IconSettings, also: ["/users", "/workers"] };

// Pages search finds that the menu leaves out.
const extra: NavItem[] = [
  { to: "/models/build", label: "Build a model", short: "Build", icon: IconSparkles, needs: "models.write" },
  { to: "/scoring", label: "Score a list", short: "Score", icon: IconListCheck, needs: "models.write" },
  { to: "/benchmarks", label: "Benchmarks", short: "Tests", icon: IconTrophy, needs: "models.write" },
  { to: "/users", label: "People", short: "People", icon: IconUsers, needs: "users.read" },
];

export const deviceItem: NavItem = { to: "/device", label: "On this device", short: "Device", icon: IconDeviceLaptop };

// In the account menu: version, author and license.
export const aboutItem: NavItem = { to: "/about", label: "About mimir", short: "About", icon: IconInfoCircle };

// The flat list, for search.
export const nav: NavItem[] = [...groups.flatMap((g) => g.items), ...extra, settingsItem, deviceItem, aboutItem];

export function isActive(path: string, item: NavItem) {
  const match = (to: string) => (to === "/" ? path === "/" : path === to || path.startsWith(to + "/"));
  if (item.to === "/models" && ["/models/build", "/models/teach", "/models/new", "/models/finetune"].some((p) => path.startsWith(p))) return false;
  return match(item.to) || (item.also ?? []).some(match);
}
