import { useMutation } from "@connectrpc/connect-query";
import { ActionIcon, Alert, Badge, Button, Card, Group, Menu, Modal, Stack, Text, TextInput, Title } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { IconAlertCircle, IconDots, IconUsersPlus } from "@tabler/icons-react";
import { useQueryClient, useSuspenseQuery } from "@tanstack/react-query";
import { useState } from "react";

import { message } from "../api/client";
import { UserService } from "../api/gen/mimir/v1/access_pb";
import { TeamService, type Team } from "../api/gen/mimir/v1/team_pb";
import { allTeamQueries, listTeams, whoAmI } from "../api/queries";

function count(n: number, one: string, many: string) {
  return `${n} ${n === 1 ? one : many}`;
}

// Teams is the owner's list of teams. Each person is in one team and sees
// only that team's models, examples, training runs and webhooks.
export function Teams() {
  const { data } = useSuspenseQuery(listTeams);
  const { data: me } = useSuspenseQuery(whoAmI);
  const queryClient = useQueryClient();
  const [naming, setNaming] = useState<Team | "new">();
  const [removing, setRemoving] = useState<Team>();
  const [moving, setMoving] = useState<Team>();
  const refresh = () => queryClient.invalidateQueries({ queryKey: allTeamQueries });

  const remove = useMutation(TeamService.method.deleteTeam, {
    onSuccess: async () => { await refresh(); notifications.show({ message: `${removing?.name} was removed.` }); setRemoving(undefined); },
  });
  const move = useMutation(UserService.method.updateUser, {
    // Everything on screen belonged to the old team.
    onSuccess: async () => { await queryClient.invalidateQueries(); notifications.show({ color: "teal", message: `You're now in ${moving?.name}.` }); setMoving(undefined); },
  });

  return (
    <Stack>
      <Group justify="space-between" align="flex-end">
        <div>
          <Title order={3} fz="lg">Teams</Title>
          <Text c="dimmed" size="sm" maw={620}>Each person belongs to one team and sees only that team's models, examples, training runs and webhooks. Add people to a team from Users.</Text>
        </div>
        <Button leftSection={<IconUsersPlus size={18} />} onClick={() => setNaming("new")}>Add a team</Button>
      </Group>
      {data.teams.map((t) => {
        const mine = t.id === me.user?.teamId;
        const empty = t.members === 0 && t.models === 0 && t.datasets === 0;
        return (
          <Card key={t.id} withBorder radius="lg" padding="md">
            <Group justify="space-between" wrap="nowrap">
              <div className="min-w-0">
                <Group gap="xs" wrap="nowrap">
                  <Text fw={700} truncate>{t.name}</Text>
                  {mine && <Badge variant="light" color="teal">Your team</Badge>}
                </Group>
                <Text size="sm" c="dimmed">{count(t.members, "person", "people")} · {count(t.models, "model", "models")} · {count(t.datasets, "set of examples", "sets of examples")}</Text>
              </div>
              <Group gap="xs" wrap="nowrap">
                {!mine && <Button variant="default" size="xs" onClick={() => setMoving(t)}>Move me here</Button>}
                <Menu position="bottom-end">
                  <Menu.Target><ActionIcon variant="subtle" color="gray" aria-label={`Options for ${t.name}`}><IconDots /></ActionIcon></Menu.Target>
                  <Menu.Dropdown>
                    <Menu.Item onClick={() => setNaming(t)}>Rename</Menu.Item>
                    <Menu.Item color="red" disabled={!empty} onClick={() => setRemoving(t)}>{empty ? "Remove" : "Remove (empty it first)"}</Menu.Item>
                  </Menu.Dropdown>
                </Menu>
              </Group>
            </Group>
          </Card>
        );
      })}

      <NameModal key={naming === "new" ? "new" : naming?.id ?? "closed"} team={naming} onClose={() => setNaming(undefined)} onDone={refresh} />

      <Modal opened={!!moving} onClose={() => setMoving(undefined)} title={`Move yourself to ${moving?.name}?`} centered>
        <Stack>
          <Text>You'll see {moving?.name}'s models and examples instead of your current team's. You stay signed in here, but you're signed out on other devices and your API keys stop working, because they were for your old team.</Text>
          {move.error && <Alert color="red" icon={<IconAlertCircle />}>{message(move.error)}</Alert>}
          <Group justify="flex-end">
            <Button variant="default" onClick={() => setMoving(undefined)}>Stay</Button>
            <Button loading={move.isPending} onClick={() => moving && me.user && move.mutate({ id: me.user.id, name: me.user.name, access: me.user.access, teamId: moving.id })}>Move me</Button>
          </Group>
        </Stack>
      </Modal>

      <Modal opened={!!removing} onClose={() => setRemoving(undefined)} title={`Remove ${removing?.name}?`} centered>
        <Text mb="lg">Its webhooks are removed too. Its audit events stay in the log.</Text>
        {remove.error && <Alert color="red" mb="md" icon={<IconAlertCircle />}>{message(remove.error)}</Alert>}
        <Group justify="flex-end">
          <Button variant="default" onClick={() => setRemoving(undefined)}>Keep it</Button>
          <Button color="red" loading={remove.isPending} onClick={() => removing && remove.mutate({ id: removing.id })}>Remove</Button>
        </Group>
      </Modal>
    </Stack>
  );
}

function NameModal({ team, onClose, onDone }: { team?: Team | "new"; onClose: () => void; onDone: () => Promise<unknown> }) {
  const existing = team && team !== "new" ? team : undefined;
  const [name, setName] = useState(existing?.name ?? "");
  const finish = async (what: string) => { await onDone(); notifications.show({ color: "teal", message: what }); onClose(); };
  const create = useMutation(TeamService.method.createTeam, { onSuccess: () => finish(`${name.trim()} is ready. Add people to it from Users.`) });
  const update = useMutation(TeamService.method.updateTeam, { onSuccess: () => finish("Saved.") });
  const error = create.error ?? update.error;
  return (
    <Modal opened={!!team} onClose={onClose} title={existing ? `Rename ${existing.name}` : "Add a team"} centered>
      <form onSubmit={(e) => { e.preventDefault(); if (existing) update.mutate({ id: existing.id, name }); else create.mutate({ name }); }}>
        <Stack>
          <TextInput label="Team name" placeholder="For example: Research" required maxLength={100} value={name} onChange={(e) => setName(e.currentTarget.value)} data-autofocus />
          {error && <Alert color="red" icon={<IconAlertCircle />}>{message(error)}</Alert>}
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>Cancel</Button>
            <Button type="submit" loading={create.isPending || update.isPending}>{existing ? "Save" : "Add team"}</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}
