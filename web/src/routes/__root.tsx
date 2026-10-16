import { AppShell, Burger, Group, NavLink, Text, UnstyledButton } from "@mantine/core";
import { useDisclosure, useMediaQuery } from "@mantine/hooks";
import { IconDots } from "@tabler/icons-react";
import type { QueryClient } from "@tanstack/react-query";
import { createRootRouteWithContext, Link, Outlet, useRouterState } from "@tanstack/react-router";

import { nav } from "../components/nav";
import { UpdatePrompt } from "../components/UpdatePrompt";

export const Route = createRootRouteWithContext<{ queryClient: QueryClient }>()({
  component: Layout,
  notFoundComponent: () => <Text p="xl">That page doesn't exist. Use the menu to find your way back.</Text>,
});

function active(path: string, to: string) {
  return to === "/" ? path === "/" : path === to || path.startsWith(to + "/");
}

function Layout() {
  const [opened, { toggle, close }] = useDisclosure();
  const phone = useMediaQuery("(max-width: 48em)") ?? false;
  const path = useRouterState({ select: (s) => s.location.pathname });

  return (
    <AppShell
      header={{ height: 60, collapsed: false }}
      navbar={{ width: 248, breakpoint: "sm", collapsed: { mobile: !opened } }}
      footer={{ height: 68, collapsed: !phone }}
      padding={{ base: "md", sm: "xl" }}
    >
      <AppShell.Header px="md">
        <Group h="100%" justify="space-between">
          <Group gap="sm">
            <Burger opened={opened} onClick={toggle} hiddenFrom="sm" size="sm" aria-label="Open menu" />
            <Link to="/" className="flex items-center gap-2 no-underline" style={{ color: "inherit" }}>
              <img src="/icon.svg" alt="" width={28} height={28} />
              <Text fw={700} ff="var(--font-display)" fz={22}>mimir</Text>
            </Link>
          </Group>
        </Group>
      </AppShell.Header>

      <AppShell.Navbar p="sm" aria-label="Main menu">
        {nav.map((n) => (
          <NavLink
            key={n.to}
            component={Link}
            to={n.to}
            label={n.label}
            leftSection={<n.icon size={20} stroke={1.8} />}
            active={active(path, n.to)}
            onClick={close}
            variant="light"
            className="rounded-md"
            py={10}
          />
        ))}
      </AppShell.Navbar>

      <AppShell.Main>
        <Outlet />
      </AppShell.Main>

      {/* Phones get a bottom bar with the four things people do most. */}
      <AppShell.Footer aria-label="Quick menu">
        <nav className="grid h-full grid-cols-4">
          {nav.slice(0, 3).map((n) => (
            <UnstyledButton
              key={n.to}
              component={Link}
              to={n.to}
              aria-current={active(path, n.to) ? "page" : undefined}
              className="flex flex-col items-center justify-center gap-0.5 text-xs"
              c={active(path, n.to) ? "teal.8" : "dimmed"}
              fw={active(path, n.to) ? 700 : 500}
            >
              <n.icon size={22} stroke={1.8} />
              {n.short}
            </UnstyledButton>
          ))}
          <UnstyledButton onClick={toggle} className="flex flex-col items-center justify-center gap-0.5 text-xs" c="dimmed">
            <IconDots size={22} stroke={1.8} />
            More
          </UnstyledButton>
        </nav>
      </AppShell.Footer>
      <UpdatePrompt />
    </AppShell>
  );
}
