import {
  IconBox, IconDeviceLaptop, IconFileText, IconHome, IconListCheck, IconMessageCircle, IconPlugConnected, IconSchool, IconSettings, IconSparkles, IconTrophy, IconUsers,
  type Icon,
} from "@tabler/icons-react";

export type NavTo = "/" | "/models" | "/try" | "/device" | "/training" | "/scoring" | "/documents" | "/benchmarks" | "/models/build" | "/apps" | "/users" | "/settings";

// needs is the permission a person must have to see the item. also lists
// other paths that light the item up, like templates under Build.
export type NavItem = { to: NavTo; label: string; short: string; icon: Icon; needs?: string; also?: string[] };
export type NavGroup = { label?: string; items: NavItem[] };

// The menu in three groups: using models, building them, and running the
// server. People see only what they may open.
export const groups: NavGroup[] = [
  {
    items: [
      { to: "/", label: "Home", short: "Home", icon: IconHome },
      { to: "/models", label: "Models", short: "Models", icon: IconBox },
      { to: "/try", label: "Try a model", short: "Try", icon: IconMessageCircle },
      { to: "/documents", label: "Ask your documents", short: "Docs", icon: IconFileText },
    ],
  },
  {
    label: "Build",
    items: [
      { to: "/models/build", label: "Build a model", short: "Build", icon: IconSparkles, needs: "models.write", also: ["/templates", "/models/teach", "/models/new", "/models/finetune"] },
      { to: "/training", label: "Training", short: "Train", icon: IconSchool, needs: "models.write" },
      { to: "/scoring", label: "Score a list", short: "Score", icon: IconListCheck, needs: "models.write" },
      { to: "/benchmarks", label: "Benchmarks", short: "Tests", icon: IconTrophy, needs: "models.write" },
    ],
  },
  {
    label: "Manage",
    items: [
      { to: "/apps", label: "Connect your apps", short: "Apps", icon: IconPlugConnected },
      { to: "/users", label: "People", short: "People", icon: IconUsers, needs: "users.read" },
      { to: "/settings", label: "Settings", short: "Settings", icon: IconSettings, needs: "audit.read", also: ["/workers"] },
    ],
  },
];

export const deviceItem: NavItem = { to: "/device", label: "On this device", short: "Device", icon: IconDeviceLaptop };

// The flat list, for search.
export const nav: NavItem[] = [...groups.flatMap((g) => g.items), deviceItem];

export function isActive(path: string, item: NavItem) {
  const match = (to: string) => (to === "/" ? path === "/" : path === to || path.startsWith(to + "/"));
  if (item.to === "/models" && ["/models/build", "/models/teach", "/models/new", "/models/finetune"].some((p) => path.startsWith(p))) return false;
  return match(item.to) || (item.also ?? []).some(match);
}
