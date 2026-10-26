import { create } from "@bufbuild/protobuf";
import { useMutation } from "@connectrpc/connect-query";
import { ActionIcon, Alert, Badge, Button, Card, Code, CopyButton, Group, Menu, Modal, PasswordInput, SegmentedControl, Select, Stack, Switch, Text, TextInput, Title, Tooltip } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { IconAlertCircle, IconCheck, IconCopy, IconDots, IconKey, IconPlus, IconTrash } from "@tabler/icons-react";
import { useQueryClient, useSuspenseQuery } from "@tanstack/react-query";
import { useState } from "react";

import { message } from "../api/client";
import { Role } from "../api/gen/mimir/v1/access_pb";
import { SignInKind, SignInProviderSchema, SignInService, type SignInProvider, type SignInRule } from "../api/gen/mimir/v1/signin_pb";
import { allSignInQueries, listTeams, signInProviders } from "../api/queries";
import { roles } from "../lib/roles";
import { KindIcon } from "./KindIcon";

const kinds: { value: SignInKind; label: string; id: string; name: string }[] = [
  { value: SignInKind.GOOGLE, label: "Google", id: "google", name: "Google" },
  { value: SignInKind.GITHUB, label: "GitHub", id: "github", name: "GitHub" },
  { value: SignInKind.OIDC, label: "Company (OpenID Connect)", id: "company", name: "Company account" },
];

// Where each provider is told to send people back.
const callback = (id: string) => `${location.origin}/auth/sso/${id || "…"}/callback`;

// SignInProviders is the owner's list of ways to sign in besides a
// password, and who gets an account through each.
export function SignInProviders() {
  const { data } = useSuspenseQuery(signInProviders);
  const queryClient = useQueryClient();
  const [editing, setEditing] = useState<SignInProvider | SignInKind>();
  const [removing, setRemoving] = useState<SignInProvider>();
  const refresh = () => queryClient.invalidateQueries({ queryKey: allSignInQueries });
  const remove = useMutation(SignInService.method.deleteSignInProvider, {
    onSuccess: async () => { await refresh(); notifications.show({ message: `${removing?.name} was removed.` }); setRemoving(undefined); },
  });
  return (
    <Stack>
      <Group justify="space-between" align="flex-end">
        <div>
          <Title order={3} fz="lg">Sign in with another account</Title>
          <Text c="dimmed" size="sm" maw={640}>Let people sign in with Google, GitHub or your company's account, next to their password. Two-step verification still applies. People who already have an account here sign in with the same email address; someone new only gets an account when a rule below says which team they join.</Text>
        </div>
        <Menu position="bottom-end">
          <Menu.Target><Button leftSection={<IconPlus size={18} />}>Add a way to sign in</Button></Menu.Target>
          <Menu.Dropdown>
            {kinds.map((k) => <Menu.Item key={k.value} leftSection={<KindIcon kind={k.value} />} onClick={() => setEditing(k.value)}>{k.label}</Menu.Item>)}
          </Menu.Dropdown>
        </Menu>
      </Group>
      {data.providers.length === 0 && <Text c="dimmed">None yet. People sign in with their email and password.</Text>}
      {data.providers.map((p) => (
        <Card key={p.id} withBorder radius="lg" padding="md">
          <Group justify="space-between" wrap="nowrap">
            <Group gap="sm" wrap="nowrap" className="min-w-0">
              <KindIcon kind={p.kind} size={22} />
              <div className="min-w-0">
                <Group gap="xs" wrap="nowrap">
                  <Text fw={700} truncate>{p.name}</Text>
                  <Badge variant="light" color={p.enabled ? "teal" : "gray"}>{p.enabled ? "On" : "Off"}</Badge>
                </Group>
                <Text size="sm" c="dimmed">{p.rules.length === 0 ? "Only people who already have an account" : p.rules.map(ruleWords).join(" · ")}</Text>
              </div>
            </Group>
            <Group gap="xs" wrap="nowrap">
              <Button variant="default" size="xs" onClick={() => setEditing(p)}>Change</Button>
              <Menu position="bottom-end">
                <Menu.Target><ActionIcon variant="subtle" color="gray" aria-label={`Options for ${p.name}`}><IconDots /></ActionIcon></Menu.Target>
                <Menu.Dropdown><Menu.Item color="red" leftSection={<IconTrash size={16} />} onClick={() => setRemoving(p)}>Remove</Menu.Item></Menu.Dropdown>
              </Menu>
            </Group>
          </Group>
        </Card>
      ))}

      {editing !== undefined && <ProviderModal key={typeof editing === "number" ? `new-${editing}` : editing.id} start={editing} taken={data.providers.map((p) => p.id)} onClose={() => setEditing(undefined)} onDone={refresh} />}

      <Modal opened={!!removing} onClose={() => setRemoving(undefined)} title={`Remove ${removing?.name}?`} centered>
        <Text mb="lg">Its button goes from the sign-in page. Accounts made through it stay; their people sign in with a password an admin sets, or with this provider again if you add it back.</Text>
        {remove.error && <Alert color="red" mb="md" icon={<IconAlertCircle />}>{message(remove.error)}</Alert>}
        <Group justify="flex-end">
          <Button variant="default" onClick={() => setRemoving(undefined)}>Keep it</Button>
          <Button color="red" loading={remove.isPending} onClick={() => removing && remove.mutate({ id: removing.id })}>Remove</Button>
        </Group>
      </Modal>
    </Stack>
  );
}

function ruleWords(r: SignInRule) {
  const who = r.domain ? `@${r.domain}` : `group ${r.group}`;
  const role = roles.find((x) => x.value === r.role)?.label.toLowerCase() ?? "viewer";
  return `${who} → ${role} in ${r.teamName || "a removed team"}`;
}

type Rule = { by: "domain" | "group"; value: string; teamId: string; role: Role };

function ProviderModal({ start, taken, onClose, onDone }: { start: SignInProvider | SignInKind; taken: string[]; onClose: () => void; onDone: () => Promise<unknown> }) {
  const existing = typeof start === "number" ? undefined : start;
  const kind = existing?.kind ?? (start as SignInKind);
  const preset = kinds.find((k) => k.value === kind)!;
  const { data: teams } = useSuspenseQuery(listTeams);
  const freeId = () => { let id = preset.id; for (let n = 2; taken.includes(id); n++) id = `${preset.id}-${n}`; return id; };
  const [p, setP] = useState(() => ({
    id: existing?.id ?? freeId(), name: existing?.name ?? preset.name, enabled: existing?.enabled ?? true,
    issuer: existing?.issuer ?? "", clientId: existing?.clientId ?? "", clientSecret: "",
    groupsClaim: existing?.groupsClaim ?? "", trustEmails: existing?.trustEmails ?? false,
  }));
  const [rules, setRules] = useState<Rule[]>(() => (existing?.rules ?? []).map((r) => ({ by: r.group ? "group" : "domain", value: r.group || r.domain, teamId: r.teamId, role: r.role })));
  const save = useMutation(SignInService.method.saveSignInProvider, {
    onSuccess: async () => { await onDone(); notifications.show({ color: "teal", message: `${p.name.trim()} saved.` }); onClose(); },
  });
  const submit = () => save.mutate({
    provider: create(SignInProviderSchema, {
      ...p, kind,
      rules: rules.map((r) => ({ domain: r.by === "domain" ? r.value : "", group: r.by === "group" ? r.value : "", teamId: r.teamId, role: r.role })),
    }),
  });
  const set = (patch: Partial<typeof p>) => setP({ ...p, ...patch });
  const setRule = (i: number, patch: Partial<Rule>) => setRules(rules.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  const groupWord = kind === SignInKind.GITHUB ? "organization" : "group";
  const teamOptions = teams.teams.map((t) => ({ value: t.id, label: t.name }));
  const roleOptions = roles.filter((r) => r.value !== Role.OWNER).map((r) => ({ value: String(r.value), label: r.label }));

  return (
    <Modal opened onClose={onClose} title={existing ? `Change ${existing.name}` : `Add ${preset.label}`} size="lg" centered>
      <form onSubmit={(e) => { e.preventDefault(); submit(); }}>
        <Stack>
          <Steps kind={kind} id={p.id} />
          <Group grow align="flex-start">
            <TextInput label="Button label" description={`The sign-in page shows "Continue with ${p.name || "…"}".`} required maxLength={100} value={p.name} onChange={(e) => set({ name: e.currentTarget.value })} />
            <TextInput label="Short id" description="Part of the address above; it can't change later." required disabled={!!existing} value={p.id} onChange={(e) => set({ id: e.currentTarget.value.toLowerCase() })} />
          </Group>
          {kind === SignInKind.OIDC && (
            <TextInput label="Issuer address" description="From your provider, such as https://login.microsoftonline.com/your-tenant/v2.0 or https://your-org.okta.com." placeholder="https://login.example.com" required value={p.issuer} onChange={(e) => set({ issuer: e.currentTarget.value })} />
          )}
          <Group grow align="flex-start">
            <TextInput label="Client id" required autoComplete="off" value={p.clientId} onChange={(e) => set({ clientId: e.currentTarget.value })} />
            <PasswordInput label="Client secret" autoComplete="new-password" required={!existing?.clientSecretSet} placeholder={existing?.clientSecretSet ? "Saved; leave empty to keep it" : ""} value={p.clientSecret} onChange={(e) => set({ clientSecret: e.currentTarget.value })} />
          </Group>
          {kind === SignInKind.OIDC && <>
            <TextInput label="Groups claim" description="The ID token claim that lists someone's groups, if your provider sends one." placeholder="groups" value={p.groupsClaim} onChange={(e) => set({ groupsClaim: e.currentTarget.value })} />
            <Switch label="Trust its email addresses" description="Turn this on when the provider only has your own people and doesn't say whether it checked an address. Otherwise people need a checked address to sign in." checked={p.trustEmails} onChange={(e) => set({ trustEmails: e.currentTarget.checked })} />
          </>}

          <div>
            <Text fw={600} size="sm">Who gets a new account</Text>
            <Text size="sm" c="dimmed" mb="xs">The first rule that fits decides. Without a rule that fits, only people who already have an account here can sign in.{kind === SignInKind.GOOGLE || kind === SignInKind.GITHUB ? ` Anyone can make a ${preset.label} account, so match your own domain${kind === SignInKind.GITHUB ? " or organization" : ""}.` : ""}</Text>
            <Stack gap="xs">
              {rules.map((r, i) => (
                <Group key={i} gap="xs" wrap="nowrap" align="flex-start">
                  <SegmentedControl size="xs" mt={4} value={r.by} onChange={(v) => setRule(i, { by: v as Rule["by"] })} data={[{ value: "domain", label: "Domain" }, { value: "group", label: kind === SignInKind.GITHUB ? "Org" : "Group" }]} />
                  <TextInput className="flex-1" size="xs" aria-label={r.by === "domain" ? "Email domain" : groupWord} placeholder={r.by === "domain" ? "example.com" : kind === SignInKind.GITHUB ? "your-org" : "engineering"} required value={r.value} onChange={(e) => setRule(i, { value: e.currentTarget.value })} />
                  <Select size="xs" w={140} aria-label="Team" placeholder="Team" required data={teamOptions} value={r.teamId || null} onChange={(v) => setRule(i, { teamId: v ?? "" })} allowDeselect={false} />
                  <Select size="xs" w={110} aria-label="Role" data={roleOptions} value={String(r.role)} onChange={(v) => setRule(i, { role: Number(v) as Role })} allowDeselect={false} />
                  <ActionIcon variant="subtle" color="gray" mt={2} aria-label="Remove rule" onClick={() => setRules(rules.filter((_, j) => j !== i))}><IconTrash size={16} /></ActionIcon>
                </Group>
              ))}
              <Button variant="default" size="xs" className="self-start" leftSection={<IconPlus size={14} />} onClick={() => setRules([...rules, { by: "domain", value: "", teamId: teamOptions[0]?.value ?? "", role: Role.VIEWER }])}>Add a rule</Button>
            </Stack>
          </div>

          <Switch label="Show it on the sign-in page" checked={p.enabled} onChange={(e) => set({ enabled: e.currentTarget.checked })} />
          {save.error && <Alert color="red" icon={<IconAlertCircle />}>{message(save.error)}</Alert>}
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>Cancel</Button>
            <Button type="submit" loading={save.isPending}>{existing ? "Save" : "Add"}</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}

// Steps says what to do at the provider first, with the address to give.
function Steps({ kind, id }: { kind: SignInKind; id: string }) {
  const where = {
    [SignInKind.GOOGLE]: <>In the Google Cloud console, open APIs &amp; Services, Credentials, and make an OAuth client ID for a web application. Add this as an authorized redirect URI:</>,
    [SignInKind.GITHUB]: <>On GitHub, open Settings, Developer settings, OAuth Apps, and register a new app. Use this as its authorization callback URL:</>,
    [SignInKind.OIDC]: <>In your identity provider (Microsoft Entra ID, Okta, Keycloak, Authentik…), register a web app with the openid, email and profile scopes, and this redirect URI:</>,
    [SignInKind.UNSPECIFIED]: null,
  }[kind];
  const url = callback(id);
  return (
    <Alert variant="light" color="blue" icon={<IconKey />}>
      <Text size="sm">{where}</Text>
      <Group gap={4} mt={6} wrap="nowrap">
        <Code className="break-all">{url}</Code>
        <CopyButton value={url}>
          {({ copied, copy }) => (
            <Tooltip label={copied ? "Copied" : "Copy"}><ActionIcon variant="subtle" onClick={copy} aria-label="Copy the address">{copied ? <IconCheck size={16} /> : <IconCopy size={16} />}</ActionIcon></Tooltip>
          )}
        </CopyButton>
      </Group>
      <Text size="sm" mt={6}>Then paste the client id and secret it gives you here.</Text>
    </Alert>
  );
}
