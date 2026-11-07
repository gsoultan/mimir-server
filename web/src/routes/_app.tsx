import { Code, ConnectError } from "@connectrpc/connect";
import { useMutation } from "@connectrpc/connect-query";
import { Alert, AppShell, Avatar, Button, Drawer, Group, Menu, NavLink, ScrollArea, Stack, Text, UnstyledButton, useMantineColorScheme } from "@mantine/core";
import { useDisclosure, useHotkeys, useMediaQuery } from "@mantine/hooks";
import { Spotlight, spotlight, type SpotlightActionData } from "@mantine/spotlight";
import { IconAlertCircle, IconBox, IconDeviceDesktop, IconKey, IconLogout, IconMenu2, IconMoon, IconPlus, IconSearch, IconSelector, IconShieldLock, IconSun, IconWifiOff } from "@tabler/icons-react";
import { useQuery, useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, Link, Outlet, redirect, useNavigate, useRouterState } from "@tanstack/react-router";
import { useMemo } from "react";

import { message } from "../api/client";
import { AuthService } from "../api/gen/mimir/v1/access_pb";
import { listModels, setupState, whoAmI } from "../api/queries";
import { aboutItem, deviceItem, groups, isActive, nav, settingsItem, type NavItem } from "../components/nav";
import { ChangePasswordModal } from "../components/ChangePasswordModal";
import { roleLabel } from "../lib/roles";

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
  // Offline, models saved on this device still answer.
  if (!navigator.onLine) {
    return (
      <Stack align="flex-start" p="xl" className="mx-auto max-w-2xl">
        <Alert color="gray" icon={<IconWifiOff />} title="You're offline" className="w-full">
          mimir needs the server for this page. Models saved on this device still answer.
        </Alert>
        <Group>
          <Button component={Link} to="/device">Models on this device</Button>
          <Button variant="default" onClick={reset}>Try again</Button>
        </Group>
      </Stack>
    );
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


function Layout() {
  const [menuOpen, menu] = useDisclosure();
  const [changing, change] = useDisclosure();
  const phone = useMediaQuery("(max-width: 48em)", false, { getInitialValueInEffect: false });
  const path = useRouterState({ select: (s) => s.location.pathname });
  const navigate = useNavigate();
  const { data: me } = useSuspenseQuery(whoAmI);
  const { data: models } = useQuery(listModels);
  const { queryClient } = Route.useRouteContext();
  const can = (p: string) => me.permissions.includes(p);
  const shown = groups.map((g) => ({ ...g, items: g.items.filter((n) => !n.needs || can(n.needs)) })).filter((g) => g.items.length > 0);
  const canBuild = can("models.write");
  // Settings holds People too, so people who may see either get it.
  const settings: NavItem | undefined = can("audit.read") ? settingsItem
    : can("users.read") ? { ...settingsItem, to: "/users", label: "People" } : undefined;
  const signOut = useMutation(AuthService.method.signOut, {
    onSuccess: () => { queryClient.clear(); location.assign("/sign-in"); },
  });
  useHotkeys([["mod+K", () => spotlight.open()]]);

  // Search finds pages and models, and offers the common next steps.
  const actions = useMemo<SpotlightActionData[]>(() => [
    ...nav.filter((n) => !n.needs || can(n.needs)).filter((n) => n.to !== "/settings" || settings?.to === "/settings").map((n) => ({
      id: "page:" + n.to, label: n.label, group: "Pages", leftSection: <n.icon size={18} stroke={1.8} />,
      onClick: () => void navigate({ to: n.to }),
    })),
    ...(models?.models ?? []).flatMap((m) => [
      { id: "model:" + m.id, label: m.name, description: m.description || "Model", group: "Models", leftSection: <IconBox size={18} stroke={1.8} />, onClick: () => void navigate({ to: "/models/$id", params: { id: m.id } }) },
      { id: "try:" + m.id, label: `Try ${m.name}`, group: "Actions", keywords: [m.name], leftSection: <IconSearch size={18} stroke={1.8} />, onClick: () => void navigate({ to: "/try", search: { model: m.id } }) },
    ]),
  ], [me, models, navigate]);

  const account = (
    <AccountMenu
      name={me.user?.name ?? ""} email={me.user?.email ?? ""} team={me.user?.teamName ?? ""} role={me.user?.access ? roleLabel(me.user.access.role) : ""}
      onPassword={change.open} onSignOut={() => signOut.mutate({})}
    />
  );

  const links = (onPick?: () => void) => (
    <Stack gap={2}>
      {shown.flatMap((g) => g.items).map((n) => <SideLink key={n.to} item={n} active={isActive(path, n)} onClick={onPick} />)}
    </Stack>
  );

  // The phone bar holds the four things people do most and More.
  const first = groups[0]!.items;
  const bar: (NavItem | "build")[] = [first[0]!, first[1]!, ...(canBuild ? ["build" as const] : []), first[2]!];

  return (
    <AppShell
      header={{ height: 56, collapsed: !phone }}
      navbar={{ width: 248, breakpoint: "sm", collapsed: { mobile: true } }}
      footer={{ height: 72, collapsed: !phone }}
      padding={{ base: "md", sm: "xl" }}
      styles={{ header: { background: "var(--mantine-color-body)" }, navbar: { background: "var(--mantine-color-body)" } }}
      classNames={{ header: "app-header", footer: "app-footer" }}
    >
      {/* Phones only: the brand, search and the account. */}
      <AppShell.Header px="md" hiddenFrom="sm">
        <Group h="100%" justify="space-between">
          <Link to="/" className="flex items-center gap-2 no-underline" style={{ color: "inherit" }} aria-label="mimir home">
            <img src="/icon.svg" alt="" width={28} height={28} />
            <Text fw={750} ff="var(--font-display)" fz={20}>mimir</Text>
          </Link>
          <Group gap="xs">
            <UnstyledButton onClick={() => spotlight.open()} aria-label="Search" p={6} c="dimmed"><IconSearch size={22} stroke={1.8} /></UnstyledButton>
            <Menu position="bottom-end" width={240}>
              <Menu.Target>
                <UnstyledButton aria-label="Account menu"><Avatar size={32} radius="xl" color="teal">{initial(me.user?.name)}</Avatar></UnstyledButton>
              </Menu.Target>
              <AccountItems email={me.user?.email ?? ""} team={me.user?.teamName ?? ""} onPassword={change.open} onSignOut={() => signOut.mutate({})} />
            </Menu>
          </Group>
        </Group>
      </AppShell.Header>

      <AppShell.Navbar p="sm" aria-label="Main menu">
        <AppShell.Section>
          <Link to="/" className="flex items-center gap-2 px-2 pb-4 pt-1 no-underline" style={{ color: "inherit" }} aria-label="mimir home">
            <img src="/icon.svg" alt="" width={28} height={28} />
            <Text fw={750} ff="var(--font-display)" fz={21}>mimir</Text>
          </Link>
          <button type="button" className="side-search" onClick={() => spotlight.open()}>
            <IconSearch size={16} /> Search or jump to… <kbd>{isMac() ? "⌘" : "Ctrl"} K</kbd>
          </button>
        </AppShell.Section>
        <AppShell.Section grow component={ScrollArea} mt="sm">{links()}</AppShell.Section>
        <AppShell.Section pt="sm" style={{ borderTop: "1px solid var(--mimir-line)" }}>
          {settings && <SideLink item={settings} active={isActive(path, settings)} />}
          {account}
        </AppShell.Section>
      </AppShell.Navbar>

      <AppShell.Main>
        <Outlet />
      </AppShell.Main>

      <AppShell.Footer aria-label="Quick menu" hiddenFrom="sm" className="tab-bar">
        <nav className="grid h-full" style={{ gridTemplateColumns: `repeat(${bar.length + 1}, 1fr)` }}>
          {bar.map((n) => n === "build" ? (
            <UnstyledButton key="build" component={Link} to="/models/build" aria-current={path.startsWith("/models/build") || path.startsWith("/templates") ? "page" : undefined}
              className="flex flex-col items-center justify-center gap-0.5 text-[11px] font-semibold">
              <span className="plus"><IconPlus size={20} /></span>Build
            </UnstyledButton>
          ) : (
            <UnstyledButton key={n.to} component={Link} to={n.to} aria-current={isActive(path, n) ? "page" : undefined}
              className="flex flex-col items-center justify-center gap-0.5 text-[11px] font-semibold">
              <n.icon size={23} stroke={1.8} />{n.short}
            </UnstyledButton>
          ))}
          <UnstyledButton onClick={menu.toggle} className="flex flex-col items-center justify-center gap-0.5 text-[11px] font-semibold"
            aria-current={!bar.some((n) => n !== "build" && isActive(path, n)) && !path.startsWith("/models/build") ? "page" : undefined}>
            <IconMenu2 size={23} stroke={1.8} />More
          </UnstyledButton>
        </nav>
      </AppShell.Footer>

      <Drawer opened={menuOpen} onClose={menu.close} position="bottom" size="auto" radius="lg" title="Menu" hiddenFrom="sm"
        styles={{ content: { borderBottomLeftRadius: 0, borderBottomRightRadius: 0 }, body: { paddingBottom: 24 } }}>
        {links(menu.close)}
        <Stack gap={2} mt="sm" pt="sm" style={{ borderTop: "1px solid var(--mimir-line)" }}>
          {canBuild && <SideLink item={{ to: "/models/build", label: "Build a model", short: "Build", icon: IconPlus }} active={false} onClick={menu.close} />}
          {settings && <SideLink item={settings} active={isActive(path, settings)} onClick={menu.close} />}
          <SideLink item={deviceItem} active={isActive(path, deviceItem)} onClick={menu.close} />
        </Stack>
      </Drawer>

      <Spotlight
        actions={actions}
        nothingFound="Nothing matches"
        highlightQuery
        limit={12}
        scrollable
        maxHeight={420}
        searchProps={{ leftSection: <IconSearch size={18} />, placeholder: "Search pages and models" }}
        shortcut={null}
      />
      <ChangePasswordModal opened={changing} onClose={change.close} />
    </AppShell>
  );
}

function SideLink({ item: n, active, onClick }: { item: NavItem; active: boolean; onClick?: () => void }) {
  return (
    <NavLink component={Link} to={n.to} label={n.label} leftSection={<n.icon size={20} stroke={1.8} />}
      active={active} onClick={onClick} className="side-link" py={8} variant="subtle" color="gray" />
  );
}

function initial(name?: string) {
  return (name ?? "?").slice(0, 1).toUpperCase();
}

function isMac() {
  return typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform);
}

// The account at the bottom of the sidebar: who you are and your own settings.
function AccountMenu({ name, email, team, role, onPassword, onSignOut }: { name: string; email: string; team: string; role: string; onPassword: () => void; onSignOut: () => void }) {
  const who = [role, team].filter(Boolean).join(" · ");
  return (
    <Menu position="top-start" width={240}>
      <Menu.Target>
        <UnstyledButton className="flex w-full items-center gap-3 rounded-md p-2 hover:bg-[var(--mantine-color-default-hover)]" aria-label="Account menu">
          <Avatar size={32} radius="xl" color="teal">{initial(name)}</Avatar>
          <div className="min-w-0 flex-1">
            <Text size="sm" fw={650} truncate>{name}</Text>
            {who && <Text size="xs" c="dimmed" truncate>{who}</Text>}
          </div>
          <IconSelector size={16} color="var(--mimir-faint)" />
        </UnstyledButton>
      </Menu.Target>
      <AccountItems email={email} team={team} onPassword={onPassword} onSignOut={onSignOut} />
    </Menu>
  );
}

function AccountItems({ email, team, onPassword, onSignOut }: { email: string; team: string; onPassword: () => void; onSignOut: () => void }) {
  const { colorScheme, setColorScheme } = useMantineColorScheme();
  const look = [
    { value: "light" as const, label: "Light", icon: IconSun },
    { value: "dark" as const, label: "Dark", icon: IconMoon },
    { value: "auto" as const, label: "Match device", icon: IconDeviceDesktop },
  ];
  return (
    <Menu.Dropdown>
      <Menu.Label>{email}</Menu.Label>
      {team && <Menu.Label>Team: {team}</Menu.Label>}
      <Menu.Item component={Link} to="/device" leftSection={<deviceItem.icon size={16} />}>On this device</Menu.Item>
      <Menu.Item component={Link} to="/security" leftSection={<IconShieldLock size={16} />}>Security</Menu.Item>
      <Menu.Item leftSection={<IconKey size={16} />} onClick={onPassword}>Change password</Menu.Item>
      <Menu.Divider />
      <Menu.Label>Look</Menu.Label>
      {look.map((l) => (
        <Menu.Item key={l.value} leftSection={<l.icon size={16} />} onClick={() => setColorScheme(l.value)}
          rightSection={colorScheme === l.value ? <Text size="xs" c="teal" fw={700}>On</Text> : null}>{l.label}</Menu.Item>
      ))}
      <Menu.Divider />
      <Menu.Item component={Link} to="/about" leftSection={<aboutItem.icon size={16} />}>About mimir</Menu.Item>
      <Menu.Divider />
      <Menu.Item leftSection={<IconLogout size={16} />} onClick={onSignOut}>Sign out</Menu.Item>
    </Menu.Dropdown>
  );
}
