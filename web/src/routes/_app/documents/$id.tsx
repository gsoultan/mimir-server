import { Code, ConnectError, createClient } from "@connectrpc/connect";
import { useMutation } from "@connectrpc/connect-query";
import {
  ActionIcon, Alert, Badge, Button, Card, Grid, Group, Loader, Modal, Popover, ScrollArea, Select, Stack, Text, Textarea, TextInput, Tooltip,
} from "@mantine/core";
import { Dropzone } from "@mantine/dropzone";
import { notifications } from "@mantine/notifications";
import { IconAlertCircle, IconArrowUp, IconFileText, IconMessagePlus, IconPlayerStopFilled, IconSettings, IconTrash } from "@tabler/icons-react";
import { useQueryClient, useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useEffect, useRef, useState } from "react";

import { message, transport } from "../../../api/client";
import { DocumentService, type Collection, type Passage } from "../../../api/gen/mimir/v1/document_pb";
import { allDocumentQueries, getCollection, listModels, whoAmI } from "../../../api/queries";
import { Page } from "../../../components/Page";
import { ago, bytes } from "../../../lib/format";
import { cited, documentStatus, documentTypes, maxDocumentBytes, readable, reading } from "../../../lib/documents";

const documents = createClient(DocumentService, transport);

export const Route = createFileRoute("/_app/documents/$id")({
  loader: ({ context, params }) => Promise.all([
    context.queryClient.ensureQueryData(getCollection(params.id)),
    context.queryClient.ensureQueryData(listModels),
  ]),
  errorComponent: ({ error }) => (
    <Alert color="red" icon={<IconAlertCircle />} className="mx-auto max-w-5xl">
      {message(error)} <Link to="/documents">Back to your documents</Link>
    </Alert>
  ),
  component: CollectionPage,
});

function CollectionPage() {
  const { id } = Route.useParams();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { data: me } = useSuspenseQuery(whoAmI);
  const canWrite = me.permissions.includes("models.write");
  const { data } = useSuspenseQuery({
    ...getCollection(id),
    refetchInterval: (q) => (q.state.data?.documents.some(reading) ? 1500 : false),
  });
  const c = data.collection!;
  const [editing, setEditing] = useState(false);
  const [uploading, setUploading] = useState(0);
  const refresh = () => queryClient.invalidateQueries({ queryKey: allDocumentQueries });
  const remove = useMutation(DocumentService.method.deleteCollection, {
    onSuccess: async () => {
      await navigate({ to: "/documents" });
      queryClient.removeQueries({ queryKey: getCollection(id).queryKey });
      await refresh();
      notifications.show({ message: `${c.name} was deleted.` });
    },
  });
  const removeDoc = useMutation(DocumentService.method.deleteDocument, { onSuccess: refresh });

  const add = async (files: File[]) => {
    setUploading((n) => n + files.length);
    for (const f of files) {
      try {
        await documents.addDocument({ collectionId: id, name: f.name, data: new Uint8Array(await f.arrayBuffer()) });
      } catch (err) {
        notifications.show({ color: "red", message: `${f.name}: ${message(err)}` });
      } finally {
        setUploading((n) => n - 1);
        await refresh();
      }
    }
  };

  return (
    <Page
      title={c.name}
      intro={<>{c.documents} {c.documents === 1 ? "document" : "documents"} · answered by {c.languageModelName || "a deleted model"} · apps ask it as <Text span ff="monospace" fz="inherit">{c.apiName}</Text></>}
      actions={canWrite && (
        <Group gap="xs">
          <Button variant="default" leftSection={<IconSettings size={18} />} onClick={() => setEditing(true)}>Settings</Button>
          <Button variant="subtle" color="red" leftSection={<IconTrash size={16} />} loading={remove.isPending}
            onClick={() => window.confirm(`Delete ${c.name} and its ${c.documents} documents?`) && remove.mutate({ id })}>Delete</Button>
        </Group>
      )}
    >
      {(remove.error ?? removeDoc.error) && <Alert color="red" icon={<IconAlertCircle />}>{message(remove.error ?? removeDoc.error)}</Alert>}
      <Grid gap="md">
        <Grid.Col span={{ base: 12, md: 7 }}>
          <Ask collection={c} empty={c.passages === 0n} />
        </Grid.Col>
        <Grid.Col span={{ base: 12, md: 5 }}>
          <Card withBorder radius="lg" padding="lg">
            <Text fw={700} mb="xs">Documents</Text>
            {canWrite && (
              <Dropzone mb="sm" maxSize={maxDocumentBytes} loading={uploading > 0}
                accept={{ "text/plain": documentTypes, "text/markdown": [".md"], "text/html": [".html", ".htm"], "application/pdf": [".pdf"], "application/vnd.openxmlformats-officedocument.wordprocessingml.document": [".docx"] }}
                onDrop={(files) => void add(files.filter((f) => readable(f.name)))}
                onReject={(rs) => notifications.show({ color: "red", message: `${rs.map((r) => r.file.name).join(", ")}: add PDF, Word, Markdown, web page or text files of up to 64 MB.` })}>
                <Stack align="center" gap={2} py="sm" className="pointer-events-none">
                  <IconFileText size={28} stroke={1.5} />
                  <Text fw={600} size="sm">Drop files here, or click to choose</Text>
                  <Text size="xs" c="dimmed">PDF, Word, Markdown, web pages or text, up to 64 MB each</Text>
                </Stack>
              </Dropzone>
            )}
            {data.documents.length === 0 ? (
              <Text size="sm" c="dimmed">No documents yet.</Text>
            ) : (
              <Stack gap={6}>
                {data.documents.map((d) => (
                  <Group key={d.id} justify="space-between" wrap="nowrap" gap="xs">
                    <div className="min-w-0">
                      <Text size="sm" fw={500} truncate>{d.name}</Text>
                      <Text size="xs" c={d.error ? "red" : "dimmed"} lineClamp={2}>
                        {d.error || `${documentStatus(d)} · ${bytes(d.bytes)} · added ${ago(d.createTime)}`}
                      </Text>
                    </div>
                    {reading(d)
                      ? <Loader size="xs" />
                      : canWrite && (
                        <ActionIcon variant="subtle" color="gray" aria-label={`Delete ${d.name}`} onClick={() => removeDoc.mutate({ id: d.id })}>
                          <IconTrash size={16} />
                        </ActionIcon>
                      )}
                  </Group>
                ))}
              </Stack>
            )}
          </Card>
        </Grid.Col>
      </Grid>
      <Settings collection={c} open={editing} onClose={() => setEditing(false)} />
    </Page>
  );
}

type Turn = { role: "user" | "assistant"; content: string; sources?: Passage[]; note?: string };

// Ask is a chat with the collection, kept until the page is left.
function Ask({ collection, empty }: { collection: Collection; empty: boolean }) {
  const [turns, setTurns] = useState<Turn[]>([]);
  const [draft, setDraft] = useState("");
  const [writing, setWriting] = useState(false);
  const [error, setError] = useState<string>();
  const stop = useRef<AbortController | null>(null);
  const bottom = useRef<HTMLDivElement>(null);
  useEffect(() => { bottom.current?.scrollIntoView({ block: "end" }); }, [turns]);
  useEffect(() => () => stop.current?.abort(), []);

  const send = async () => {
    const q = draft.trim();
    if (!q || writing) return;
    const asked: Turn[] = [...turns, { role: "user", content: q }];
    setDraft("");
    setError(undefined);
    setWriting(true);
    const ctl = new AbortController();
    stop.current = ctl;
    let answer: Turn = { role: "assistant", content: "" };
    setTurns([...asked, answer]);
    try {
      for await (const res of documents.ask({
        collectionId: collection.id,
        messages: asked.map((t) => ({ role: t.role, content: t.content })),
      }, { signal: ctl.signal })) {
        answer = { ...answer, content: answer.content + res.text, sources: res.sources.length > 0 ? res.sources : answer.sources };
        if (res.done && !res.finished) answer.note = "Cut off before the end";
        setTurns([...asked, answer]);
      }
    } catch (err) {
      if (ConnectError.from(err).code === Code.Canceled) {
        setTurns([...asked, { ...answer, note: "Stopped" }]);
      } else {
        setError(message(err));
        setTurns(turns);
        setDraft(q);
      }
    } finally {
      setWriting(false);
      stop.current = null;
    }
  };

  return (
    <Card withBorder radius="lg" padding={0} className="flex flex-col" h="min(640px, calc(100dvh - 260px))" mih={420}>
      <Group justify="space-between" px="lg" py="sm" className="border-b" style={{ borderColor: "var(--mantine-color-default-border)" }}>
        <Text fw={700}>Ask</Text>
        {turns.length > 0 && (
          <Button variant="subtle" size="xs" leftSection={<IconMessagePlus size={16} />} onClick={() => { stop.current?.abort(); setTurns([]); setError(undefined); }}>New chat</Button>
        )}
      </Group>
      <ScrollArea className="flex-1" px="lg" py="md">
        {turns.length === 0 && (
          <Text c="dimmed" ta="center" mt="xl">
            {empty ? "Add documents, then ask about them here." : "Ask anything your documents answer. Each answer shows the passages it came from."}
          </Text>
        )}
        <Stack gap="md">
          {turns.map((t, i) => t.role === "user"
            ? <Card key={i} radius="lg" padding="sm" bg="var(--mantine-color-default-hover)" ml="auto" maw="85%"><Text style={{ whiteSpace: "pre-wrap" }}>{t.content}</Text></Card>
            : <AnswerView key={i} turn={t} writing={writing && i === turns.length - 1} />)}
        </Stack>
        <div ref={bottom} />
      </ScrollArea>
      <Stack gap="xs" p="md" className="border-t" style={{ borderColor: "var(--mantine-color-default-border)" }}>
        {error && <Alert color="red" icon={<IconAlertCircle />} py="xs">{error}</Alert>}
        <Group gap="xs" wrap="nowrap" align="flex-end">
          <Textarea className="flex-1" placeholder="Ask a question" autosize minRows={1} maxRows={6} value={draft} disabled={empty}
            onChange={(e) => setDraft(e.currentTarget.value)}
            onKeyDown={(e) => { if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); void send(); } }} />
          {writing
            ? <ActionIcon size={36} variant="default" aria-label="Stop" onClick={() => stop.current?.abort()}><IconPlayerStopFilled size={16} /></ActionIcon>
            : <ActionIcon size={36} aria-label="Send" disabled={!draft.trim() || empty} onClick={() => void send()}><IconArrowUp size={18} /></ActionIcon>}
        </Group>
      </Stack>
    </Card>
  );
}

function AnswerView({ turn, writing }: { turn: Turn; writing: boolean }) {
  const sources = turn.sources ?? [];
  return (
    <div>
      {turn.content === "" && writing
        ? <Group gap="xs"><Loader size="xs" type="dots" /><Text size="sm" c="dimmed">Looking through your documents</Text></Group>
        : (
          <Text style={{ whiteSpace: "pre-wrap" }}>
            {cited(turn.content, sources.length).map((p, k) => "text" in p
              ? <span key={k}>{p.text}</span>
              : <Source key={k} n={p.source} passage={sources[p.source - 1]} />)}
          </Text>
        )}
      {turn.note && <Text size="xs" c="dimmed" mt={4}>{turn.note}</Text>}
      {sources.length > 0 && (
        <Group gap={6} mt="xs">
          <Text size="xs" c="dimmed">From</Text>
          {sources.map((p, k) => <Source key={p.id} n={k + 1} passage={p} label={`${k + 1}. ${p.documentName}`} />)}
        </Group>
      )}
    </div>
  );
}

// Source is a citation that shows its passage when clicked.
function Source({ n, passage, label }: { n: number; passage?: Passage; label?: string }) {
  if (!passage) return <span>[{n}]</span>;
  return (
    <Popover width={420} position="top" withArrow shadow="md">
      <Popover.Target>
        <Tooltip label={passage.documentName} disabled={!!label}>
          <Badge component="button" variant="light" size="sm" radius="sm" mx={2} style={{ cursor: "pointer", textTransform: "none" }}>{label ?? n}</Badge>
        </Tooltip>
      </Popover.Target>
      <Popover.Dropdown>
        <Text size="xs" c="dimmed" mb={4}>{passage.documentName}, passage {passage.number}</Text>
        <ScrollArea.Autosize mah={260}><Text size="sm" style={{ whiteSpace: "pre-wrap" }}>{passage.text}</Text></ScrollArea.Autosize>
      </Popover.Dropdown>
    </Popover>
  );
}

function Settings({ collection: c, open, onClose }: { collection: Collection; open: boolean; onClose: () => void }) {
  const queryClient = useQueryClient();
  const { data: models } = useSuspenseQuery(listModels);
  const language = models.models.filter((m) => m.language).map((m) => ({ value: m.id, label: m.name }));
  const [name, setName] = useState(c.name);
  const [model, setModel] = useState<string | null>(c.languageModelId || null);
  const [instructions, setInstructions] = useState(c.instructions);
  const save = useMutation(DocumentService.method.updateCollection, {
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: allDocumentQueries });
      notifications.show({ message: "Saved." });
      onClose();
    },
  });
  return (
    <Modal opened={open} onClose={onClose} title="Settings" radius="lg" size="lg" centered>
      <Stack gap="md">
        {save.error && <Alert color="red" icon={<IconAlertCircle />}>{message(save.error)}</Alert>}
        <TextInput label="Name" maxLength={100} value={name} onChange={(e) => setName(e.currentTarget.value)} />
        <Select label="Model that answers" data={language} value={model} onChange={setModel} allowDeselect={false} searchable />
        <TextInput label="Model that reads the documents" value={c.embeddingModelName} disabled
          description="Fixed: the passages were read by it. Make a new collection to use another." />
        <Textarea label="Instructions" autosize minRows={2} maxLength={8000} value={instructions} onChange={(e) => setInstructions(e.currentTarget.value)} />
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>Cancel</Button>
          <Button loading={save.isPending} disabled={!name.trim() || !model}
            onClick={() => save.mutate({ id: c.id, name: name.trim(), languageModelId: model!, instructions })}>Save</Button>
        </Group>
      </Stack>
    </Modal>
  );
}
