import { useMutation } from "@connectrpc/connect-query";
import { Alert, Button, Card, Code, CopyButton, Group, SegmentedControl, Stack, Text, Textarea } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { IconAlertCircle, IconCheck, IconCopy } from "@tabler/icons-react";
import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import { message } from "../api/client";
import { ModelService, type Model } from "../api/gen/mimir/v1/model_pb";
import { allModelQueries } from "../api/queries";
import { lengths, nearest, wordChoices } from "../lib/chats";
import { AnchorLink } from "./links";

// LanguageDefaults shows how a language model answers when a chat or an app
// doesn't say, and the name apps send for it.
export function LanguageDefaults({ model, canWrite }: { model: Model; canWrite: boolean }) {
  const l = model.language!;
  const queryClient = useQueryClient();
  const [instructions, setInstructions] = useState(l.instructions);
  const [temperature, setTemperature] = useState(l.temperature);
  const [maxTokens, setMaxTokens] = useState(l.maxTokens);
  const save = useMutation(ModelService.method.updateModel, {
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: allModelQueries });
      notifications.show({ color: "teal", message: "Saved." });
    },
  });
  const changed = instructions !== l.instructions || temperature !== l.temperature || maxTokens !== l.maxTokens;
  return (
    <>
      <Card withBorder radius="lg" padding="lg">
        <Text fw={700} mb={4}>How it answers by default</Text>
        <Text size="sm" c="dimmed" mb="sm">Apps and new chats use these unless they send their own.</Text>
        <Stack>
          <Textarea
            label="Instructions"
            description="Who it is and how to talk. It reads this before every chat."
            placeholder="e.g. You answer questions for a small tea shop. Keep it warm and short."
            autosize
            minRows={3}
            maxRows={10}
            maxLength={8000}
            value={instructions}
            onChange={(e) => setInstructions(e.currentTarget.value)}
            readOnly={!canWrite}
          />
          <Stack gap={4}>
            <Text size="sm" fw={500}>Word choice</Text>
            <SegmentedControl className="self-start" data={wordChoices} value={nearest(wordChoices, temperature)} onChange={(v) => setTemperature(Number(v))} readOnly={!canWrite} />
          </Stack>
          <Stack gap={4}>
            <Text size="sm" fw={500}>Longest answer</Text>
            <SegmentedControl className="self-start" data={lengths} value={nearest(lengths, maxTokens)} onChange={(v) => setMaxTokens(Number(v))} readOnly={!canWrite} />
          </Stack>
          {save.error && <Alert color="red" icon={<IconAlertCircle />}>{message(save.error)}</Alert>}
          {canWrite && (
            <Button
              className="self-end"
              loading={save.isPending}
              disabled={!changed}
              onClick={() => save.mutate({ id: model.id, name: model.name, description: model.description, language: { instructions, temperature, maxTokens } })}
            >
              Save
            </Button>
          )}
        </Stack>
      </Card>
      <Card withBorder radius="lg" padding="lg">
        <Text fw={700} mb={4}>Use it like OpenAI</Text>
        <Text size="sm" c="dimmed" mb="sm">Any app or library made for OpenAI works. Send this model name with a mimir key.</Text>
        <Group gap="xs" wrap="nowrap">
          <Code block className="flex-1">{l.apiName}</Code>
          <CopyButton value={l.apiName}>
            {({ copied, copy }) => <Button variant="default" onClick={copy} leftSection={copied ? <IconCheck size={16} /> : <IconCopy size={16} />}>{copied ? "Copied" : "Copy"}</Button>}
          </CopyButton>
        </Group>
        <Text size="sm" mt="sm"><AnchorLink to="/apps">Address and code examples</AnchorLink> are under Connect your apps.</Text>
      </Card>
    </>
  );
}
