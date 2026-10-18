import { Code, ConnectError } from "@connectrpc/connect";
import { useMutation } from "@connectrpc/connect-query";
import { Alert, AppShell, Avatar, Burger, Button, Group, Menu, NavLink, Stack, Text, UnstyledButton } from "@mantine/core";
import { useDisclosure, useMediaQuery } from "@mantine/hooks";
import { IconAlertCircle, IconChevronDown, IconDots, IconKey, IconLogout, IconShieldLock } from "@tabler/icons-react";
import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, Link, Outlet, redirect, useRouterState } from "@tanstack/react-router";

import { message } from "../api/client";
import { AuthService } from "../api/gen/mimir/v1/access_pb";
import { setupState, whoAmI } from "../api/queries";
import { nav } from "../components/nav";
import { ChangePasswordModal } from "../components/ChangePasswordModal";

// Every screen under _app needs a signed-in person. The guard runs before
// any page loads, so an expired session lands on sign-in, not an error.
export const Route = createFileRoute("/_app")({
  beforeLoad: async ({ context, location }) => {
    try {
      const me = await context.queryClient.fetchQuery({ ...whoAmI, staleTime: 30_000 });
      // Someone who must set up two-step verification sees only that page
      // until it's done.
      if (me.mustSetUpTwoFactor && !location.pathname.startsWith("/security")) throw redirect({ to: "/security" });
      return { me };
    } catch (err) {
      if (ConnectError.from(err).code !== Code.Unauthenticated) throw err;
      const { needsSetup } = await context.queryClient.fetchQuery(setupState);
      if (needsSetup) throw redirect({ to: "/setup" });
      throw redirect({ to: "/sign-in", search: { next: location.href } });
    }
  },
  component: Layout,
  errorComponent: ({ error, reset }) => <ErrorPage error={error} reset={reset} />,
});

// A page's loader failed: most often a session that ended, or a screen the
// person may not open. Say so in plain words instead of a blank page.
function ErrorPage({ error, reset }: { error: unknown; reset: () => void }) {
  const code = ConnectError.from(error).code;
  if (code === Code.FailedPrecondition && message(error).includes("two-step")) {
    location.assign("/security");
    return null;
  }
  if (code === Code.Unauthenticated) {
    location.assign("/sign-in?next=" + encodeURIComponent(location.pathname + location.search));
    return null;
  }
  return (
    <Stack align="flex-start" p="xl" className="mx-auto max-w-2xl">
      <Alert color={code === Code.PermissionDenied ? "yellow" : "red"} icon={<IconAlertCircle />} title={code === Code.PermissionDenied ? "This page isn't for your account" : "Something went wrong"} className="w-full">
        {message(error)}
      </Alert>
      <Group>
        <Button component={Link} to="/" onClick={reset}>Go home</Button>
        <Button variant="default" onClick={reset}>Try again</Button>
      </Group>
    </Stack>
  );
}

function active(path: string, to: string) {
  return to === "/" ? path === "/" : path === to || path.startsWith(to + "/");
}

function Layout() {
  const [opened, { toggle, close }] = useDisclosure();
  const [changing, change] = useDisclosure();
  const phone = useMediaQuery("(max-width: 48em)") ?? false;
  const path = useRouterState({ select: (s) => s.location.pathname });
  const { data: me } = useSuspenseQuery(whoAmI);
  const { queryClient } = Route.useRouteContext();
  const can = (p: string) => me.permissions.includes(p);
  const items = nav.filter((n) => !n.needs || can(n.needs));
  const signOut = useMutation(AuthService.method.signOut, {
    onSuccess: () => { queryClient.clear(); location.assign("/sign-in"); },
  });

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
          <Menu position="bottom-end" width={220}>
            <Menu.Target>
              <UnstyledButton className="flex items-center gap-2 rounded-md px-2 py-1" aria-label="Account menu">
                <Avatar size={30} radius="xl" color="teal">{me.user?.name.slice(0, 1).toUpperCase()}</Avatar>
                <Text size="sm" fw={600} visibleFrom="sm">{me.user?.name}</Text>
                <IconChevronDown size={16} />
              </UnstyledButton>
            </Menu.Target>
            <Menu.Dropdown>
              <Menu.Label>{me.user?.email}</Menu.Label>
              {me.user?.teamName && <Menu.Label>Team: {me.user.teamName}</Menu.Label>}
              <Menu.Item leftSection={<IconKey size={16} />} onClick={change.open}>Change password</Menu.Item>
              <Menu.Item component={Link} to="/security" leftSection={<IconShieldLock size={16} />}>Security</Menu.Item>
              <Menu.Item leftSection={<IconLogout size={16} />} onClick={() => signOut.mutate({})}>Sign out</Menu.Item>
            </Menu.Dropdown>
          </Menu>
        </Group>
      </AppShell.Header>

      <AppShell.Navbar p="sm" aria-label="Main menu">
        {items.map((n) => (
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

      {/* Phones get a bottom bar with the three things people do most. */}
      <AppShell.Footer aria-label="Quick menu">
        <nav className="grid h-full grid-cols-4">
          {items.slice(0, 3).map((n) => (
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
      <ChangePasswordModal opened={changing} onClose={change.close} />
    </AppShell>
  );
}
