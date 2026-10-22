import { useMutation } from "@connectrpc/connect-query";
import { ActionIcon, Alert, Badge, Button, Card, Group, Menu, Modal, PasswordInput, Select, Stack, Text, TextInput } from "@mantine/core";
import { useDisclosure } from "@mantine/hooks";
import { notifications } from "@mantine/notifications";
import { IconAlertCircle, IconDots, IconShieldCheck, IconUserPlus } from "@tabler/icons-react";
import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";

import { message } from "../../api/client";
import { Role, UserService, type User } from "../../api/gen/mimir/v1/access_pb";
import { allUserQueries, listModels, listTeams, listUsers, whoAmI } from "../../api/queries";
import { AccessPicker, defaultAccess, describe, fromProto, type AccessValue } from "../../components/AccessPicker";
import { Page } from "../../components/Page";
import { ago } from "../../lib/format";

export const Route = createFileRoute("/_app/users")({
  loader: async ({ context }) => {
    const me = await context.queryClient.ensureQueryData(whoAmI);
    return Promise.all([
      context.queryClient.ensureQueryData(listUsers),
      context.queryClient.ensureQueryData(listModels),
      me.permissions.includes("teams.manage") ? context.queryClient.ensureQueryData(listTeams) : null,
    ]);
  },
  component: Users,
});

function Users() {
  const { data } = useSuspenseQuery(listUsers);
  const { data: models } = useSuspenseQuery(listModels);
  const { data: me } = useSuspenseQuery(whoAmI);
  const { queryClient } = Route.useRouteContext();
  const [adding, add] = useDisclosure();
  const [editing, setEditing] = useState<User>();
  const [removing, setRemoving] = useState<User>();
  const names = new Map(models.models.map((m) => [m.id, m.name]));
  const canManageAdmins = me.permissions.includes("users.admin");
  const maxRole = canManageAdmins ? Role.OWNER : Role.EDITOR;
  const manyTeams = me.permissions.includes("teams.manage");
  const refresh = () => queryClient.invalidateQueries({ queryKey: allUserQueries });

  const remove = useMutation(UserService.method.deleteUser, {
    onSuccess: async () => { await refresh(); notifications.show({ message: `${removing?.name} was removed.` }); setRemoving(undefined); },
  });

  return (
    <Page
      title="Users"
      intro={manyTeams ? "Who can sign in, which team they're in, and which models each person can reach." : `Who in ${me.user?.teamName ?? "your team"} can sign in, and which models each person can reach.`}
      actions={<Button leftSection={<IconUserPlus size={18} />} onClick={add.open}>Add a person</Button>}
    >
      <Stack gap="sm">
        {data.users.map((u) => {
          const mine = u.id === me.user?.id;
          const locked = (u.access?.role ?? 0) >= Role.ADMIN && !canManageAdmins;
          return (
            <Card key={u.id} withBorder radius="lg" padding="md">
              <Group justify="space-between" wrap="nowrap">
                <div className="min-w-0">
                  <Group gap="xs" wrap="nowrap">
                    <Text fw={700} truncate>{u.name}</Text>
                    {mine && <Badge variant="light" color="gray">You</Badge>}
                    {u.disabled && <Badge variant="light" color="red">Disabled</Badge>}
                    {u.twoFactor && <Badge variant="light" color="teal" leftSection={<IconShieldCheck size={12} />}>2-step</Badge>}
                    {manyTeams && <Badge variant="outline" color="gray">{u.teamName}</Badge>}
                  </Group>
                  <Text size="sm" c="dimmed" truncate>{u.email}</Text>
                  <Text size="sm" mt={4}>{u.teamId === me.user?.teamId ? describe(u.access, names) : describe(u.access)}</Text>
                  <Text size="xs" c="dimmed">{u.lastSignInTime ? `Last signed in ${ago(u.lastSignInTime)}` : "Never signed in"}</Text>
                </div>
                {!locked && (
                  <Menu position="bottom-end">
                    <Menu.Target><ActionIcon variant="subtle" color="gray" aria-label={`Options for ${u.name}`}><IconDots /></ActionIcon></Menu.Target>
                    <Menu.Dropdown>
                      <Menu.Item onClick={() => setEditing(u)}>Edit</Menu.Item>
                      {!mine && <Menu.Item color="red" onClick={() => setRemoving(u)}>Remove</Menu.Item>}
                    </Menu.Dropdown>
                  </Menu>
                )}
              </Group>
            </Card>
          );
        })}
      </Stack>

      <UserModal key={adding ? "add" : "closed"} opened={adding} onClose={add.close} maxRole={maxRole} teams={manyTeams} myTeam={me.user?.teamId ?? ""} onDone={refresh} />
      <UserModal key={editing?.id ?? "none"} opened={!!editing} onClose={() => setEditing(undefined)} maxRole={maxRole} teams={manyTeams} myTeam={me.user?.teamId ?? ""} user={editing} onDone={refresh} />

      <Modal opened={!!removing} onClose={() => setRemoving(undefined)} title={`Remove ${removing?.name}?`} centered>
        <Text mb="lg">They won't be able to sign in, and their API keys stop working.</Text>
        {remove.error && <Alert color="red" mb="md" icon={<IconAlertCircle />}>{message(remove.error)}</Alert>}
        <Group justify="flex-end">
          <Button variant="default" onClick={() => setRemoving(undefined)}>Keep them</Button>
          <Button color="red" loading={remove.isPending} onClick={() => removing && remove.mutate({ id: removing.id })}>Remove</Button>
        </Group>
      </Modal>
    </Page>
  );
}

function UserModal({ opened, onClose, maxRole, teams, myTeam, user, onDone }: { opened: boolean; onClose: () => void; maxRole: Role; teams: boolean; myTeam: string; user?: User; onDone: () => Promise<unknown> }) {
  const [team, setTeam] = useState(user?.teamId ?? myTeam);
  const [name, setName] = useState(user?.name ?? "");
  const [email, setEmail] = useState(user?.email ?? "");
  const [password, setPassword] = useState("");
  const [disabled, setDisabled] = useState(user?.disabled ?? false);
  const [resetTwoFactor, setResetTwoFactor] = useState(false);
  const [access, setAccess] = useState<AccessValue>(user ? fromProto(user.access) : defaultAccess);
  const finish = async (what: string) => { await onDone(); notifications.show({ color: "teal", message: what }); onClose(); };
  const create = useMutation(UserService.method.createUser, { onSuccess: () => finish(`${name} can now sign in.`) });
  const update = useMutation(UserService.method.updateUser, { onSuccess: () => finish("Saved.") });
  const pending = create.isPending || update.isPending;
  const error = create.error ?? update.error;

  // Models listed here are the caller's team's; someone in another team
  // gets all of that team's models, or picks are made from inside it.
  const otherTeam = team !== myTeam;
  const sent = otherTeam ? { ...access, allModels: true, modelIds: [] } : access;
  const moved = !!user && team !== user.teamId;
  function submit() {
    if (user) update.mutate({ id: user.id, name, access: sent, disabled, newPassword: password, resetTwoFactor, teamId: team });
    else create.mutate({ name, email, password, access: sent, teamId: team });
  }

  return (
    <Modal opened={opened} onClose={onClose} title={user ? `Edit ${user.name}` : "Add a person"} centered size="lg">
      <form onSubmit={(e) => { e.preventDefault(); submit(); }}>
        <Stack>
          <TextInput label="Name" required maxLength={100} value={name} onChange={(e) => setName(e.currentTarget.value)} data-autofocus />
          <TextInput label="Email" type="email" required disabled={!!user} value={email} onChange={(e) => setEmail(e.currentTarget.value)} />
          <PasswordInput
            label={user ? "New password" : "First password"}
            description={user ? "Leave empty to keep their password. Setting one signs them out everywhere and revokes their API keys." : "Tell it to them; they can change it after signing in. At least 10 characters."}
            autoComplete="new-password"
            required={!user}
            minLength={10}
            value={password}
            onChange={(e) => setPassword(e.currentTarget.value)}
          />
          {teams && <TeamSelect value={team} onChange={setTeam} />}
          {moved && <Alert color="yellow">Moving {user?.name} signs them out everywhere and revokes their API keys, since those were for their old team.</Alert>}
          <AccessPicker value={sent} onChange={setAccess} maxRole={maxRole} subject={name.trim() || "this person"} otherTeam={otherTeam} />
          {user && (
            <Group gap="xs">
              <Button variant={disabled ? "light" : "subtle"} color={disabled ? "teal" : "red"} onClick={() => setDisabled(!disabled)}>
                {disabled ? "Enable this account" : "Disable this account"}
              </Button>
              {user.twoFactor && (
                <Button variant={resetTwoFactor ? "light" : "subtle"} color={resetTwoFactor ? "yellow" : "gray"} onClick={() => setResetTwoFactor(!resetTwoFactor)}>
                  {resetTwoFactor ? "Will reset two-step verification" : "Reset two-step verification"}
                </Button>
              )}
            </Group>
          )}
          {resetTwoFactor && <Text size="xs" c="dimmed">For when they lost their phone. They'll sign in with their password alone and can set it up again.</Text>}
          {error && <Alert color="red" icon={<IconAlertCircle />}>{message(error)}</Alert>}
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>Cancel</Button>
            <Button type="submit" loading={pending}>{user ? "Save" : "Add person"}</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}

function TeamSelect({ value, onChange }: { value: string; onChange: (id: string) => void }) {
  const { data } = useSuspenseQuery(listTeams);
  return (
    <Select
      label="Team"
      description="They'll see only this team's models and examples."
      data={data.teams.map((t) => ({ value: t.id, label: t.name }))}
      value={value}
      onChange={(v) => v && onChange(v)}
      allowDeselect={false}
    />
  );
}
