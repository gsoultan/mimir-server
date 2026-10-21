import { ConnectError, Code, createClient } from "@connectrpc/connect";
import { useMutation } from "@connectrpc/connect-query";
import { ActionIcon, Alert, Badge, Button, Card, CopyButton, Group, Progress, ScrollArea, Stack, Text, Textarea, Tooltip, UnstyledButton } from "@mantine/core";
import { useMediaQuery } from "@mantine/hooks";
import { notifications } from "@mantine/notifications";
import { IconAdjustmentsHorizontal, IconAlertCircle, IconArrowUp, IconCheck, IconCloud, IconCopy, IconMessagePlus, IconPlayerStopFilled, IconRefresh, IconTrash } from "@tabler/icons-react";
import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState, type ReactNode } from "react";

import { message, transport } from "../api/client";
import { ModelService, PredictService, type Model } from "../api/gen/mimir/v1/model_pb";
import { allModelQueries } from "../api/queries";
import { deleteChat, loadChats, pages, saveChat, titleOf, type Answer, type ChatTurn, type SavedChat } from "../lib/chats";
import { AnswerSettings } from "./AnswerSettings";

const predict = createClient(PredictService, transport);

const newID = () => crypto.randomUUID();

// Chat talks with a language model, answer by answer as it writes. Chats
// are kept in this browser.
export function Chat({ model, canWrite, header }: { model: Model; canWrite: boolean; header: ReactNode }) {
  const phone = useMediaQuery("(max-width: 48em)") ?? false;
  const queryClient = useQueryClient();
  const defaults = {
    instructions: model.language?.instructions ?? "",
    temperature: model.language?.temperature ?? 0.7,
    maxTokens: model.language?.maxTokens ?? 256,
  };
  const [chats, setChats] = useState<SavedChat[]>(() => loadChats(model.id));
  const [chat, setChat] = useState<SavedChat>(() => ({ id: newID(), title: "New chat", updated: Date.now(), turns: [] }));
  const [draft, setDraft] = useState("");
  const [writing, setWriting] = useState(false);
  const [error, setError] = useState<string>();
  const [used, setUsed] = useState<{ tokens: number; context: number }>();
  const [speed, setSpeed] = useState<number>();
  const [settingsOpen, setSettingsOpen] = useState(false);
  const stop = useRef<AbortController | null>(null);
  const bottom = useRef<HTMLDivElement>(null);

  const answer = { ...defaults, ...stripUnset(chat.answer) };
  const saveDefault = useMutation(ModelService.method.updateModel, {
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: allModelQueries });
      notifications.show({ color: "teal", message: `${model.name}'s defaults saved.` });
      setSettingsOpen(false);
    },
  });

  useEffect(() => { bottom.current?.scrollIntoView({ block: "end" }); }, [chat.turns]);
  useEffect(() => () => stop.current?.abort(), []);

  const keep = (c: SavedChat) => {
    setChat(c);
    if (c.turns.length > 0) setChats(saveChat(model.id, c));
  };

  const ask = async (turns: ChatTurn[]) => {
    setError(undefined);
    setWriting(true);
    const ctl = new AbortController();
    stop.current = ctl;
    let text = "";
    const base: SavedChat = { ...chat, turns, title: titleOf(turns), updated: Date.now() };
    const show = (note?: string) => setChat({ ...base, turns: [...turns, { role: "assistant", content: text, note }] });
    show();
    try {
      for await (const res of predict.chat({
        modelId: model.id,
        messages: turns.map((t) => ({ role: t.role, content: t.content })),
        instructions: answer.instructions,
        temperature: answer.temperature,
        maxTokens: answer.maxTokens,
        seed: answer.seed !== undefined ? BigInt(answer.seed) : BigInt(Math.floor(Math.random() * 2 ** 32)),
      }, { signal: ctl.signal })) {
        if (res.done) {
          const secs = Number(res.durationMicros) / 1e6;
          setUsed({ tokens: res.promptTokens + res.generatedTokens, context: res.contextTokens });
          setSpeed(secs > 0 ? res.generatedTokens / secs : undefined);
          const note = `${res.generatedTokens} word pieces in ${secs.toFixed(1)} s${res.finished ? "" : ", cut off before the end"}`;
          keep({ ...base, turns: [...turns, { role: "assistant", content: text, note }] });
        } else {
          text += res.text;
          show();
        }
      }
    } catch (err) {
      if (ConnectError.from(err).code === Code.Canceled) {
        keep({ ...base, turns: [...turns, { role: "assistant", content: text, note: "Stopped" }] });
      } else {
        // Put the unanswered message back in the box to send again.
        setError(message(err));
        const asked = turns.at(-1);
        setChat({ ...base, turns: turns.slice(0, -1) });
        if (asked) setDraft((d) => d || asked.content);
      }
    } finally {
      setWriting(false);
      stop.current = null;
    }
  };

  const send = () => {
    const text = draft.trim();
    if (!text || writing) return;
    setDraft("");
    void ask([...chat.turns, { role: "user", content: text }]);
  };
  const again = () => {
    const turns = chat.turns.slice(0, -1);
    if (turns.at(-1)?.role === "user") void ask(turns);
  };
  const fresh = () => {
    stop.current?.abort();
    setChat({ id: newID(), title: "New chat", updated: Date.now(), turns: [], answer: chat.answer });
    setUsed(undefined);
    setError(undefined);
  };
  const open = (c: SavedChat) => {
    stop.current?.abort();
    setChat(c);
    setUsed(undefined);
    setError(undefined);
  };
  const remove = (c: SavedChat) => {
    setChats(deleteChat(model.id, c.id));
    if (c.id === chat.id) fresh();
  };

  const settingsButton = phone
    ? <ActionIcon variant="default" size={36} aria-label="How it answers" onClick={() => setSettingsOpen(true)}><IconAdjustmentsHorizontal size={18} /></ActionIcon>
    : <Button variant="default" size="sm" leftSection={<IconAdjustmentsHorizontal size={18} />} onClick={() => setSettingsOpen(true)}>How it answers</Button>;

  const full = used ? used.tokens / used.context : 0;
  const last = chat.turns.length - 1;

  const conversation = (
    <Card withBorder={!phone} radius="lg" padding={0} className="flex flex-col" h={phone ? "calc(100dvh - 330px)" : "min(640px, calc(100dvh - 300px))"} mih={phone ? 360 : 420}>
      <Group justify="space-between" px={phone ? "sm" : "lg"} py="sm" className="border-b" style={{ borderColor: "var(--mantine-color-default-border)" }} wrap="nowrap">
        <Group gap="xs" wrap="nowrap" className="min-w-0">
          <Text fw={700} truncate>{chat.title}</Text>
          <Badge variant="light" color="gray" leftSection={<IconCloud size={12} />} visibleFrom="lg" className="shrink-0">Runs on this server</Badge>
        </Group>
        <Group gap="xs" wrap="nowrap" className="shrink-0">
          {used && used.context > 0 && (
            <Tooltip label={`It remembers about ${pages(used.context)} pages of a chat; this one uses ${pages(used.tokens)}.`}>
              <Group gap={6} wrap="nowrap">
                <Text size="xs" c="dimmed" visibleFrom="md">Memory</Text>
                <Progress value={full * 100} w={phone ? 40 : 70} size="sm" color={full > 0.9 ? "orange" : undefined} aria-label="How much of the chat it can remember" />
              </Group>
            </Tooltip>
          )}
          {phone && settingsButton}
          {phone && <ActionIcon variant="default" size={36} aria-label="New chat" onClick={fresh}><IconMessagePlus size={18} /></ActionIcon>}
        </Group>
      </Group>
      <ScrollArea className="flex-1" px={phone ? "md" : "lg"} py="md">
        <Stack gap="lg" aria-live="polite">
          {answer.instructions.trim() && (
            <Alert variant="light" color="teal" p="xs">
              <Text size="sm"><b>Instructions:</b> {answer.instructions}</Text>
            </Alert>
          )}
          {chat.turns.length === 0 && (
            <Text c="dimmed" ta="center" mt="xl">Ask {model.name} anything. It answers word by word, and you can stop it at any time.</Text>
          )}
          {chat.turns.map((t, i) => t.role === "user" ? (
            <div key={i} className="flex justify-end">
              <div className="max-w-[80%] whitespace-pre-wrap rounded-2xl rounded-br-md px-4 py-2.5" style={{ background: "var(--mantine-color-teal-light)" }}>
                <Text>{t.content}</Text>
              </div>
            </div>
          ) : (
            <Stack key={i} gap={6} className="max-w-[88%]">
              <Text size="xs" fw={700} c="teal.8">{model.name}</Text>
              <Text className="whitespace-pre-wrap">
                {t.content}
                {writing && i === last && <span className="ml-0.5 inline-block h-[1.1em] w-[0.5em] translate-y-[3px] animate-pulse rounded-sm" style={{ background: "var(--mantine-color-teal-7)" }} />}
              </Text>
              {writing && i === last ? (
                <Text size="xs" c="dimmed">Writing…{speed ? ` about ${Math.round(speed)} word pieces a second` : ""}</Text>
              ) : (
                <Group gap={4}>
                  <CopyButton value={t.content}>
                    {({ copied, copy }) => <Button variant="subtle" color="gray" size="compact-xs" leftSection={copied ? <IconCheck size={14} /> : <IconCopy size={14} />} onClick={copy}>{copied ? "Copied" : "Copy"}</Button>}
                  </CopyButton>
                  {i === last && !writing && <Button variant="subtle" color="gray" size="compact-xs" leftSection={<IconRefresh size={14} />} onClick={again}>Try again</Button>}
                  {t.note && <Text size="xs" c="dimmed" ml={4}>{t.note}</Text>}
                </Group>
              )}
            </Stack>
          ))}
          {full > 0.9 && !writing && (
            <Text size="sm" c="orange.8">This chat is nearly as long as {model.name} can remember. Start a new chat for the best answers.</Text>
          )}
          {error && <Alert color="red" icon={<IconAlertCircle />}>{error}</Alert>}
          <div ref={bottom} />
        </Stack>
      </ScrollArea>
      <Stack gap={6} p={phone ? "sm" : "md"} pt={4}>
        <div className="flex items-end gap-2 rounded-xl border px-3 py-2" style={{ borderColor: "var(--mantine-color-default-border)" }}>
          <Textarea
            variant="unstyled"
            className="flex-1"
            autosize
            minRows={1}
            maxRows={6}
            placeholder={`Ask ${model.name} something`}
            aria-label="Your message"
            value={draft}
            onChange={(e) => setDraft(e.currentTarget.value)}
            onKeyDown={(e) => { if (e.key === "Enter" && !e.shiftKey && !phone) { e.preventDefault(); send(); } }}
          />
          {writing ? (
            <Button size="sm" variant="default" leftSection={<IconPlayerStopFilled size={14} />} mb={2} onClick={() => stop.current?.abort()}>Stop</Button>
          ) : (
            <ActionIcon size="lg" radius="xl" mb={2} aria-label="Send" disabled={!draft.trim()} onClick={send}><IconArrowUp size={18} /></ActionIcon>
          )}
        </div>
        {!phone && <Text size="xs" c="dimmed" ta="center">Models can make things up. Check anything important.</Text>}
      </Stack>
    </Card>
  );

  const settings = (
    <AnswerSettings
      key={settingsOpen ? "open" : "closed"}
      opened={settingsOpen}
      onClose={() => setSettingsOpen(false)}
      name={model.name}
      value={answer}
      defaults={defaults}
      canSaveDefault={canWrite}
      saving={saveDefault.isPending}
      error={saveDefault.error ? message(saveDefault.error) : undefined}
      onUse={(a) => { keep({ ...chat, answer: a }); setSettingsOpen(false); }}
      onSaveDefault={(a) => saveDefault.mutate({
        id: model.id,
        name: model.name,
        description: model.description,
        language: { instructions: a.instructions ?? "", temperature: a.temperature ?? defaults.temperature, maxTokens: a.maxTokens ?? defaults.maxTokens },
      })}
    />
  );

  if (phone) {
    return (
      <Stack gap="sm">
        {header}
        {conversation}
        {settings}
      </Stack>
    );
  }
  return (
    <Stack gap="lg">
      <div className="flex flex-wrap items-end justify-between gap-3">
        {header}
        {settingsButton}
      </div>
      <div className="grid grid-cols-[220px_1fr] gap-6">
        <Stack gap={6}>
          <Button variant="light" leftSection={<IconMessagePlus size={18} />} justify="flex-start" onClick={fresh}>New chat</Button>
          {chats.length > 0 && <Text size="xs" fw={700} c="dimmed" mt="sm" px={4}>YOUR CHATS</Text>}
          {chats.map((c) => (
            <Group key={c.id} gap={0} wrap="nowrap" className="group rounded-md" style={{ background: c.id === chat.id ? "var(--mantine-color-default-hover)" : undefined }}>
              <UnstyledButton className="min-w-0 flex-1 px-3 py-2" onClick={() => open(c)}>
                <Text size="sm" fw={c.id === chat.id ? 700 : 500} truncate>{c.title}</Text>
                <Text size="xs" c="dimmed">{when(c.updated)}</Text>
              </UnstyledButton>
              <Tooltip label="Delete chat">
                <ActionIcon variant="subtle" color="gray" mr={4} aria-label={`Delete ${c.title}`} className="opacity-0 group-hover:opacity-100 focus:opacity-100" onClick={() => remove(c)}><IconTrash size={14} /></ActionIcon>
              </Tooltip>
            </Group>
          ))}
          <Text size="xs" c="dimmed" px={4} mt="xs">Chats are kept for 30 days, only in this browser.</Text>
        </Stack>
        {conversation}
      </div>
      {settings}
    </Stack>
  );
}

// stripUnset drops undefined fields, so they don't hide the defaults.
function stripUnset(a: Answer | undefined): Partial<Answer> {
  return Object.fromEntries(Object.entries(a ?? {}).filter(([, v]) => v !== undefined));
}

function when(ms: number): string {
  const d = new Date(ms);
  const today = new Date();
  if (d.toDateString() === today.toDateString()) return d.toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" });
  return d.toLocaleDateString(undefined, { month: "short", day: "numeric" });
}
