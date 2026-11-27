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
import { t } from "../i18n";

function count(n: number, one: string, many: string) {
  return `${n} ${t(n === 1 ? one : many)}`;
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
    onSuccess: async () => { await refresh(); notifications.show({ message: t("{name} was removed.", { name: removing?.name }) }); setRemoving(undefined); },
  });
  const move = useMutation(UserService.method.updateUser, {
    // Everything on screen belonged to the old team.
    onSuccess: async () => { await queryClient.invalidateQueries(); notifications.show({ color: "teal", message: t("You're now in {name}.", { name: moving?.name }) }); setMoving(undefined); },
  });

  return (
    <Stack>
      <Group justify="space-between" align="flex-end">
        <div>
          <Title order={3} fz="lg">{t("Teams")}</Title>
          <Text c="dimmed" size="sm" maw={620}>{t("Each person belongs to one team and sees only that team's models, examples, training runs and webhooks. Add people to a team from Users.")}</Text>
        </div>
        <Button leftSection={<IconUsersPlus size={18} />} onClick={() => setNaming("new")}>{t("Add a team")}</Button>
      </Group>
      {data.teams.map((team) => {
        const mine = team.id === me.user?.teamId;
        const empty = team.members === 0 && team.models === 0 && team.datasets === 0;
        return (
          <Card key={team.id} withBorder radius="lg" padding="md">
            <Group justify="space-between" wrap="nowrap">
              <div className="min-w-0">
                <Group gap="xs" wrap="nowrap">
                  <Text fw={700} truncate>{team.name}</Text>
                  {mine && <Badge variant="light" color="teal">{t("Your team")}</Badge>}
                </Group>
                <Text size="sm" c="dimmed">{count(team.members, "person", "people")} · {count(team.models, "model", "models")} · {count(team.datasets, t("set of examples"), t("sets of examples"))}</Text>
              </div>
              <Group gap="xs" wrap="nowrap">
                {!mine && <Button variant="default" size="xs" onClick={() => setMoving(team)}>{t("Move me here")}</Button>}
                <Menu position="bottom-end">
                  <Menu.Target><ActionIcon variant="subtle" color="gray" aria-label={t("Options for {name}", { name: team.name })}><IconDots /></ActionIcon></Menu.Target>
                  <Menu.Dropdown>
                    <Menu.Item onClick={() => setNaming(team)}>{t("Rename")}</Menu.Item>
                    <Menu.Item color="red" disabled={!empty} onClick={() => setRemoving(team)}>{empty ? t("Remove") : t("Remove (empty it first)")}</Menu.Item>
                  </Menu.Dropdown>
                </Menu>
              </Group>
            </Group>
          </Card>
        );
      })}

      <NameModal key={naming === "new" ? "new" : naming?.id ?? "closed"} team={naming} onClose={() => setNaming(undefined)} onDone={refresh} />

      <Modal opened={!!moving} onClose={() => setMoving(undefined)} title={t("Move yourself to {name}?", { name: moving?.name })} centered>
        <Stack>
          <Text>{t("You'll see")}{" "}{moving?.name}{t("'s models and examples instead of your current team's. You stay signed in here, but you're signed out on other devices and your API keys stop working, because they were for your old team.")}</Text>
          {move.error && <Alert color="red" icon={<IconAlertCircle />}>{message(move.error)}</Alert>}
          <Group justify="flex-end">
            <Button variant="default" onClick={() => setMoving(undefined)}>{t("Stay")}</Button>
            <Button loading={move.isPending} onClick={() => moving && me.user && move.mutate({ id: me.user.id, name: me.user.name, access: me.user.access, teamId: moving.id })}>{t("Move me")}</Button>
          </Group>
        </Stack>
      </Modal>

      <Modal opened={!!removing} onClose={() => setRemoving(undefined)} title={t("Remove {name}?", { name: removing?.name })} centered>
        <Text mb="lg">{t("Its webhooks are removed too. Its audit events stay in the log.")}</Text>
        {remove.error && <Alert color="red" mb="md" icon={<IconAlertCircle />}>{message(remove.error)}</Alert>}
        <Group justify="flex-end">
          <Button variant="default" onClick={() => setRemoving(undefined)}>{t("Keep it")}</Button>
          <Button color="red" loading={remove.isPending} onClick={() => removing && remove.mutate({ id: removing.id })}>{t("Remove")}</Button>
        </Group>
      </Modal>
    </Stack>
  );
}

function NameModal({ team, onClose, onDone }: { team?: Team | "new"; onClose: () => void; onDone: () => Promise<unknown> }) {
  const existing = team && team !== "new" ? team : undefined;
  const [name, setName] = useState(existing?.name ?? "");
  const finish = async (what: string) => { await onDone(); notifications.show({ color: "teal", message: what }); onClose(); };
  const create = useMutation(TeamService.method.createTeam, { onSuccess: () => finish(t("{n1} is ready. Add people to it from Users.", { n1: name.trim() })) });
  const update = useMutation(TeamService.method.updateTeam, { onSuccess: () => finish(t("Saved.")) });
  const error = create.error ?? update.error;
  return (
    <Modal opened={!!team} onClose={onClose} title={existing ? t("Rename {name}", { name: existing.name }) : t("Add a team")} centered>
      <form onSubmit={(e) => { e.preventDefault(); if (existing) update.mutate({ id: existing.id, name }); else create.mutate({ name }); }}>
        <Stack>
          <TextInput label={t("Team name")} placeholder={t("For example: Research")} required maxLength={100} value={name} onChange={(e) => setName(e.currentTarget.value)} data-autofocus />
          {error && <Alert color="red" icon={<IconAlertCircle />}>{message(error)}</Alert>}
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>{t("Cancel")}</Button>
            <Button type="submit" loading={create.isPending || update.isPending}>{existing ? t("Save") : t("Add team")}</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}
