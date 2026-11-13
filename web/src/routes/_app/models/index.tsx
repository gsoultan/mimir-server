import { Card, Chip, Group, Menu, ActionIcon, SegmentedControl, SimpleGrid, Table, Text, TextInput } from "@mantine/core";
import { IconDots, IconLayoutGrid, IconList, IconListCheck, IconPlayerPlay, IconPlus, IconSearch, IconTrophy, IconUpload } from "@tabler/icons-react";
import { useQueries, useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useDeferredValue, useState } from "react";

import type { Model } from "../../../api/gen/mimir/v1/model_pb";
import { listArchitectures, listModels, modelQuality, whoAmI } from "../../../api/queries";
import { AnchorLink, ButtonLink, MenuLink } from "../../../components/links";
import { Page } from "../../../components/Page";
import { StatusPill } from "../../../components/ui";
import { ago, bytes } from "../../../lib/format";
import { answerWord, rightWord } from "../../../lib/models";
import { t } from "../../../i18n";

export const Route = createFileRoute("/_app/models/")({
  loader: ({ context }) => Promise.all([
    context.queryClient.ensureQueryData(listModels),
    context.queryClient.ensureQueryData(listArchitectures),
  ]),
  component: Models,
});

type Filter = "all" | "fine" | "look";

// The view people last picked, kept on this device.
function savedView(): "list" | "grid" {
  try { return localStorage.getItem("mimir.models.view") === "grid" ? "grid" : "list"; } catch { return "list"; }
}

function Models() {
  const { data } = useSuspenseQuery(listModels);
  const { data: archs } = useSuspenseQuery(listArchitectures);
  const { data: me } = useSuspenseQuery(whoAmI);
  const canWrite = me.permissions.includes("models.write");
  const [query, setQuery] = useState("");
  const [filter, setFilter] = useState<Filter>("all");
  const [view, setView] = useState<"list" | "grid">(savedView);
  const q = useDeferredValue(query).trim().toLowerCase();
  const archOf = (m: Model) => archs.architectures.find((a) => a.name === m.architecture);
  const quality = useQueries({ queries: data.models.map((m) => ({ ...modelQuality(m.id), staleTime: 60_000 })) });
  const dropped = (i: number) => quality[i]?.data?.dropped ?? false;
  const looks = data.models.filter((_, i) => dropped(i)).length;

  const shown = data.models
    .map((m, i) => ({ m, i }))
    .filter(({ m, i }) => (filter === "all" || (filter === "look") === dropped(i)) && (!q || m.name.toLowerCase().includes(q) || m.description.toLowerCase().includes(q)));
  const pick = (v: "list" | "grid") => { setView(v); try { localStorage.setItem("mimir.models.view", v); } catch { /* private window */ } };

  return (
    <Page
      title={t("Models")}
      intro={t("Everything this server can answer with.")}
      actions={canWrite &&
        <Group gap="sm">
          <ButtonLink variant="subtle" leftSection={<IconTrophy size={18} />} to="/benchmarks">{t("Benchmarks")}</ButtonLink>
          <ButtonLink variant="default" leftSection={<IconUpload size={18} />} to="/models/new">{t("Upload a file")}</ButtonLink>
          <ButtonLink to="/models/build" leftSection={<IconPlus size={18} />}>{t("Build a model")}</ButtonLink>
        </Group>
      }
    >
      {data.models.length === 0 ? (
        <Card withBorder padding="xl" className="text-center">
          <Text fw={650}>{t("No models yet")}</Text>
          <Text c="dimmed" mb="md">{canWrite ? t("Build one and it shows up here. We walk you through it.") : t("Ask an editor or admin to share one with you.")}</Text>
          {canWrite && <Group justify="center"><ButtonLink to="/models/build" leftSection={<IconPlus size={18} />}>{t("Build a model")}</ButtonLink></Group>}
        </Card>
      ) : (
        <>
          <Group justify="space-between" gap="sm">
            <Group gap="sm" className="w-full sm:w-auto">
              <TextInput
                placeholder={t("Find a model")}
                leftSection={<IconSearch size={18} />}
                value={query}
                onChange={(e) => setQuery(e.currentTarget.value)}
                aria-label={t("Find a model")}
                className="w-full sm:w-72"
              />
              <Chip.Group value={filter} onChange={(v) => setFilter(v as Filter)}>
                <Group gap={6}>
                  <Chip value="all" variant="filled" size="sm">{t("All")}{" "}{data.models.length}</Chip>
                  <Chip value="fine" variant="filled" size="sm">{t("Doing fine")}{" "}{data.models.length - looks}</Chip>
                  <Chip value="look" variant="filled" size="sm" disabled={looks === 0}>{t("Needs a look")}{" "}{looks}</Chip>
                </Group>
              </Chip.Group>
            </Group>
            <SegmentedControl visibleFrom="sm" size="xs" value={view} onChange={(v) => pick(v as "list" | "grid")} aria-label={t("Show as")} data={[
              { value: "list", label: <IconList size={16} aria-label={t("List")} /> },
              { value: "grid", label: <IconLayoutGrid size={16} aria-label={t("Cards")} /> },
            ]} />
          </Group>

          {shown.length === 0 ? (
            <Text c="dimmed">{t("No model matches")}{query ? ` "${query}"` : t(" this filter")}.</Text>
          ) : view === "grid" ? (
            <SimpleGrid cols={{ base: 1, sm: 2, md: 3 }}>
              {shown.map(({ m, i }) => (
                <Card key={m.id} withBorder padding="lg" className="flex flex-col">
                  <Group justify="space-between" wrap="nowrap" mb={4}>
                    <AnchorLink fw={650} truncate to="/models/$id" params={{ id: m.id }} c="inherit">{m.name}</AnchorLink>
                    <StatusPill tone={dropped(i) ? "warn" : "ok"}>{dropped(i) ? t("Needs a look") : t("Ready")}</StatusPill>
                  </Group>
                  <Text size="sm" c="dimmed">{answerWord(archOf(m), m)} · {bytes(m.sizeBytes)}</Text>
                  {m.description && <Text size="sm" mt="xs" lineClamp={2}>{m.description}</Text>}
                  <Text size="xs" c="dimmed" mt="xs">{t("Added")}{" "}{ago(m.createTime)}</Text>
                  <Group mt="md" gap="xs" className="mt-auto pt-3">
                    <ButtonLink size="xs" leftSection={<IconPlayerPlay size={14} />} to="/try" search={{ model: m.id }}>{t("Try it")}</ButtonLink>
                    <ButtonLink size="xs" variant="default" to="/models/$id" params={{ id: m.id }}>{t("Details")}</ButtonLink>
                  </Group>
                </Card>
              ))}
            </SimpleGrid>
          ) : (
            <Card withBorder padding={0}>
              <Table highlightOnHover>
                <Table.Thead>
                  <Table.Tr>
                    <Table.Th>{t("Name")}</Table.Th>
                    <Table.Th visibleFrom="sm">{t("Answers")}</Table.Th>
                    <Table.Th visibleFrom="md">{t("Right")}</Table.Th>
                    <Table.Th>{t("Status")}</Table.Th>
                    <Table.Th visibleFrom="md">{t("Changed")}</Table.Th>
                    <Table.Th w={48} />
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {shown.map(({ m, i }) => {
                    const arch = archOf(m);
                    return (
                      <Table.Tr key={m.id}>
                        <Table.Td>
                          <AnchorLink to="/models/$id" params={{ id: m.id }} c="inherit" fw={650}>{m.name}</AnchorLink>
                          <Text size="xs" c="dimmed" lineClamp={1}>{m.description || bytes(m.sizeBytes)}</Text>
                        </Table.Td>
                        <Table.Td visibleFrom="sm" c="dimmed">{answerWord(arch, m)}</Table.Td>
                        <Table.Td visibleFrom="md">{rightWord(m, arch) || <Text c="dimmed" span>–</Text>}</Table.Td>
                        <Table.Td><StatusPill tone={dropped(i) ? "warn" : "ok"}>{dropped(i) ? t("Needs a look") : t("Ready")}</StatusPill></Table.Td>
                        <Table.Td visibleFrom="md" c="dimmed">{ago(m.updateTime ?? m.createTime)}</Table.Td>
                        <Table.Td>
                          <Menu position="bottom-end" withinPortal>
                            <Menu.Target>
                              <ActionIcon variant="subtle" color="gray" aria-label={t("More for {name}", { name: m.name })}><IconDots size={18} /></ActionIcon>
                            </Menu.Target>
                            <Menu.Dropdown>
                              <MenuLink to="/try" search={{ model: m.id }} leftSection={<IconPlayerPlay size={16} />}>{t("Try it")}</MenuLink>
                              {canWrite && <MenuLink to="/scoring" search={{ model: m.id }} leftSection={<IconListCheck size={16} />}>{t("Score a list")}</MenuLink>}
                              <MenuLink to="/models/$id" params={{ id: m.id }}>{t("Details")}</MenuLink>
                            </Menu.Dropdown>
                          </Menu>
                        </Table.Td>
                      </Table.Tr>
                    );
                  })}
                </Table.Tbody>
              </Table>
            </Card>
          )}
        </>
      )}
    </Page>
  );
}
