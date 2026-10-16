import {
  IconHome, IconLayoutGrid, IconPlayerPlay, IconPlugConnected, IconSettings, IconUsers,
  type Icon,
} from "@tabler/icons-react";

// needs is the permission a person must have to see the item.
export type NavItem = { to: "/" | "/models" | "/try" | "/apps" | "/users" | "/settings"; label: string; short: string; icon: Icon; needs?: string };

export const nav: NavItem[] = [
  { to: "/", label: "Home", short: "Home", icon: IconHome },
  { to: "/models", label: "Models", short: "Models", icon: IconLayoutGrid },
  { to: "/try", label: "Try a model", short: "Try", icon: IconPlayerPlay },
  { to: "/apps", label: "Connect your apps", short: "Apps", icon: IconPlugConnected },
  { to: "/users", label: "Users", short: "Users", icon: IconUsers, needs: "users.read" },
  { to: "/settings", label: "Settings", short: "Settings", icon: IconSettings, needs: "audit.read" },
];
