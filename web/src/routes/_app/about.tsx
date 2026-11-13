import { timestampDate } from "@bufbuild/protobuf/wkt";
import { Anchor, Avatar, Badge, Button, Card, CopyButton, Group, Modal, ScrollArea, SimpleGrid, Stack, Table, Text, ThemeIcon, Title } from "@mantine/core";
import { useDisclosure } from "@mantine/hooks";
import {
  IconBook, IconBrandGithub, IconBriefcase, IconBug, IconBuildingStore, IconCheck, IconCode, IconCopy, IconCpu, IconDatabase,
  IconExternalLink, IconFileZip, IconLock, IconMail, IconMathFunction, IconPackages, IconSparkles, IconUserHeart, type Icon,
} from "@tabler/icons-react";
import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import type { ReactNode } from "react";

import type { GetAboutResponse } from "../../api/gen/mimir/v1/settings_pb";
import { getAbout } from "../../api/queries";
import { Page } from "../../components/Page";
import { details, workers } from "../../lib/about";
import { duration } from "../../lib/format";
import { locale, t } from "../../i18n";

export const Route = createFileRoute("/_app/about")({
  loader: ({ context }) => context.queryClient.ensureQueryData(getAbout),
  component: About,
});

const repo = "https://github.com/gsoultan/mimir-server";
const author = "Gembit Soultan Shirazi";
const licenseEmail = "gembit.soultan@gmail.com";

// What each mimir project the server is built on does, by module path.
const partInfo: Record<string, { name: string; what: string; icon: Icon }> = {
  "github.com/gsoultan/mimir": { name: "mimir", what: t("The machine learning engine: teaches models and gets their answers."), icon: IconMathFunction },
  "github.com/gsoultan/mimir-model": { name: "mimir-model", what: t("The model file format, so a model moves between servers and devices."), icon: IconFileZip },
  "github.com/gsoultan/storm": { name: "storm", what: t("Talks to the database and keeps its tables up to date."), icon: IconDatabase },
};

function About() {
  const { data: about } = useSuspenseQuery(getAbout);
  const [listing, list] = useDisclosure();
  return (
    <Page
      title={t("About mimir")}
      intro={t("What this server is, which version it runs, and who made it.")}
      actions={
        <CopyButton value={details(about)} timeout={2000}>
          {({ copied, copy }) => (
            <Button variant="default" onClick={copy} leftSection={copied ? <IconCheck size={18} /> : <IconCopy size={18} />}>
              {copied ? t("Copied") : t("Copy version details")}
            </Button>
          )}
        </CopyButton>
      }
    >
      <Version about={about} />

      <div className="grid gap-5 md:grid-cols-[1.4fr_1fr]">
        <Card withBorder radius="lg" padding="lg">
          <Title order={2} fz={18}>{t("What mimir is")}</Title>
          <Text c="dimmed" mt={8}>{t("mimir-server lets you build, teach and run your own models, then ask them questions from this page, from your apps, or on your phone. Everything runs on your own server: your data and your questions stay with you.")}</Text>
          <Stack gap="sm" mt="md">
            <Fact icon={IconCode} title={t("Written in Go")} text={t("One program, no Python, no extra services to install.")} />
            <Fact icon={IconCpu} title={t("Runs on CPU or GPU")} text={t("On this machine, on workers you add, or on rented GPUs.")} />
            <Fact icon={IconLock} title={t("Your data stays here")} text={t("Nothing is sent anywhere unless you connect it.")} />
          </Stack>
        </Card>
        <Card withBorder radius="lg" padding="lg">
          <Title order={2} fz={18}>{t("Made by")}</Title>
          <Group gap="sm" mt="md" wrap="nowrap">
            <Avatar size={48} radius="xl" color="teal">GS</Avatar>
            <div>
              <Text fw={700} fz={16}>{author}</Text>
              <Text size="sm" c="dimmed">{t("Author and maintainer")}</Text>
            </div>
          </Group>
          <Anchor href="https://github.com/gsoultan" target="_blank" rel="noreferrer" fw={650} mt="md" className="inline-flex items-center gap-2">
            <IconBrandGithub size={18} />github.com/gsoultan</Anchor>
          <Text size="sm" c="dimmed" mt="md" pt="sm" style={{ borderTop: "1px solid var(--mimir-line)" }}>{t("Copyright © 2026")}{" "}{author}{t(". Free for noncommercial use and small companies. Large companies, and anyone selling it as a product or service, need written permission from the author.")}</Text>
        </Card>
      </div>

      {about.parts.length > 0 && (
        <Section title={t("Built on")}>
          <Card withBorder radius="lg" padding={0}>
            <div className="row-list">
              {about.parts.map((p) => {
                const info = partInfo[p.path];
                return (
                  <Row key={p.path} icon={info?.icon ?? IconPackages}
                    title={<Group gap={8}><span>{info?.name ?? p.path}</span><Text span ff="monospace" size="xs" c="dimmed">{p.version}</Text></Group>}
                    text={info?.what}
                    action={<Button component="a" href={`https://${p.path}`} target="_blank" rel="noreferrer" size="xs" variant="subtle" color="gray" leftSection={<IconExternalLink size={16} />}>{t("Source")}</Button>}
                  />
                );
              })}
            </div>
          </Card>
        </Section>
      )}

      <Section title={t("License")} intro={t("You can read all of the code. Whether you can use it free depends on who you are.")}>
        <Card withBorder radius="lg" padding={0}>
          <div className="row-list">
            <Row icon={IconUserHeart} brand title={t("Free for people and nonprofits")}
              text={t("Personal projects, study, research and hobby work, plus charities, schools and public bodies. PolyForm Noncommercial 1.0.0.")}
              action={<ReadIt href="https://polyformproject.org/licenses/noncommercial/1.0.0" />} />
            <Row icon={IconBuildingStore} brand title={t("Free for small companies")}
              text={t("Fewer than 100 people and under $1M revenue last year. PolyForm Small Business 1.0.0.")}
              action={<ReadIt href="https://polyformproject.org/licenses/small-business/1.0.0" />} />
            <Row icon={IconBriefcase} title={t("Permission needed for everyone else")}
              text={t("Large companies, and anyone selling it as a product or service, may use it only with permission from {licenseEmail}.", { licenseEmail })}
              action={<Button component="a" href={`mailto:${licenseEmail}?subject=${encodeURIComponent("Permission to use mimir-server")}`} size="xs" variant="default" leftSection={<IconMail size={16} />}>{t("Ask for permission")}</Button>} />
            <Row icon={IconPackages} title={t("Other software it uses")}
              text={t("{n} open-source packages, each with its own license.", { n: about.dependencies.length })}
              action={<Button size="xs" variant="default" onClick={list.open} disabled={about.dependencies.length === 0}>{t("See the list")}</Button>} />
          </div>
        </Card>
      </Section>

      <div className="grid grid-cols-2 gap-4 pt-5 sm:flex sm:flex-wrap sm:gap-6" style={{ borderTop: "1px solid var(--mimir-line)" }}>
        <FooterLink href={repo} icon={IconCode}>{t("Source code")}</FooterLink>
        <FooterLink href={`${repo}/tree/main/docs`} icon={IconBook}>{t("Documentation")}</FooterLink>
        <FooterLink href={`${repo}/blob/main/CHANGELOG.md`} icon={IconSparkles}>{t("What's new")}</FooterLink>
        <FooterLink href={`${repo}/issues/new`} icon={IconBug}>{t("Report a problem")}</FooterLink>
      </div>

      <Modal opened={listing} onClose={list.close} title={t("Other software it uses")} size="lg">
        <Text size="sm" c="dimmed" mb="sm">{t("Every package compiled into this server, at the version it uses. Each has its own license on its project page.")}</Text>
        <ScrollArea.Autosize mah={420}>
          <Table striped>
            <Table.Tbody>
              {about.dependencies.map((d) => (
                <Table.Tr key={d.path}>
                  <Table.Td><Anchor href={`https://pkg.go.dev/${d.path}`} target="_blank" rel="noreferrer" size="sm">{d.path}</Anchor></Table.Td>
                  <Table.Td><Text ff="monospace" size="xs" c="dimmed">{d.version}</Text></Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </ScrollArea.Autosize>
      </Modal>
    </Page>
  );
}

// The server panel: name, version, and how this copy was built and runs.
function Version({ about }: { about: GetAboutResponse }) {
  const built = about.built ? timestampDate(about.built) : undefined;
  return (
    <Card withBorder radius="lg" padding="lg">
      <Group gap="md" align="flex-start" wrap="nowrap">
        <img src="/icon.svg" alt="" width={56} height={56} />
        <div className="min-w-0">
          <Group gap={8}>
            <Title order={2} fz={26}>{t("mimir-server")}</Title>
            {about.version === "dev" && <Badge color="gray" variant="light">{t("Development build")}</Badge>}
          </Group>
          <Text c="dimmed" mt={4}>{t("Version")}{" "}{about.version}{built && t(" · built {date}", { date: built.toLocaleDateString(locale, { day: "numeric", month: "long", year: "numeric" }) })}
          </Text>
        </div>
      </Group>
      <SimpleGrid cols={{ base: 2, sm: 3 }} spacing="lg" verticalSpacing="md" mt="lg" pt="lg" style={{ borderTop: "1px solid var(--mimir-line)" }}>
        <Fieldv label={t("Running for")} value={about.started ? duration(Math.max(0, (Date.now() - timestampDate(about.started).getTime()) / 1000)) : "—"} />
        <Fieldv label={t("Commit")} value={about.commit ? `${about.commit.slice(0, 7)}${about.modified ? t(" (changed)") : ""}` : "—"} mono />
        <Fieldv label={t("Built with")} value={about.goVersion ? `Go ${about.goVersion}` : "—"} />
        <Fieldv label={t("Database")} value={`PostgreSQL · schema ${about.schemaVersion}`} />
        <Fieldv label={t("Workers")} value={workers(about)} />
        <Fieldv label={t("Address")} value={window.location.host} />
      </SimpleGrid>
    </Card>
  );
}

function Section({ title, intro, children }: { title: string; intro?: string; children: ReactNode }) {
  return (
    <div>
      <Title order={2} fz={18} mb={intro ? 4 : 12}>{title}</Title>
      {intro && <Text size="sm" c="dimmed" mb={12}>{intro}</Text>}
      {children}
    </div>
  );
}

function Fact({ icon: I, title, text }: { icon: Icon; title: string; text: string }) {
  return (
    <Group gap="sm" align="flex-start" wrap="nowrap">
      <ThemeIcon variant="light" color="teal" size={30} radius="md"><I size={17} /></ThemeIcon>
      <div><Text fw={650}>{title}</Text><Text size="sm" c="dimmed">{text}</Text></div>
    </Group>
  );
}

function Row({ icon: I, title, text, action, brand }: { icon: Icon; title: ReactNode; text?: string; action?: ReactNode; brand?: boolean }) {
  return (
    <div className="flex flex-wrap items-center gap-x-3.5 gap-y-2 px-[18px] py-3.5 sm:flex-nowrap">
      <ThemeIcon variant="light" color={brand ? "teal" : "gray"} size={36} radius="md"><I size={20} /></ThemeIcon>
      <div className="min-w-0 flex-1 basis-[calc(100%-64px)] sm:basis-auto">
        <Text fw={650} component="div">{title}</Text>
        {text && <Text size="sm" c="dimmed">{text}</Text>}
      </div>
      {action && <div className="ml-[50px] sm:ml-0">{action}</div>}
    </div>
  );
}

function ReadIt({ href }: { href: string }) {
  return <Button component="a" href={href} target="_blank" rel="noreferrer" size="xs" variant="default">{t("Read it")}</Button>;
}

function Fieldv({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="min-w-0">
      <Text size="xs" c="dimmed">{label}</Text>
      <Text fw={600} ff={mono ? "monospace" : undefined} size={mono ? "sm" : undefined} style={{ overflowWrap: "anywhere" }}>{value}</Text>
    </div>
  );
}

function FooterLink({ href, icon: I, children }: { href: string; icon: Icon; children: ReactNode }) {
  return (
    <Anchor href={href} target="_blank" rel="noreferrer" fw={650} className="inline-flex items-center gap-2">
      <I size={18} />{children}
    </Anchor>
  );
}
