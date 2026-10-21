import { Accordion, Alert, Button, Drawer, NumberInput, SegmentedControl, SimpleGrid, Stack, Text, Textarea } from "@mantine/core";
import { IconAlertCircle } from "@tabler/icons-react";
import { useState } from "react";

import { lengths, nearest, wordChoices, type Answer } from "../lib/chats";

// AnswerSettings edits how a language model answers: in one chat, or, for
// someone who may change the model, as its defaults.
export function AnswerSettings({ opened, onClose, name, value, defaults, canSaveDefault, saving, error, onUse, onSaveDefault }: {
  opened: boolean;
  onClose: () => void;
  name: string;
  value: Required<Omit<Answer, "seed">> & { seed?: number };
  defaults: { instructions: string; temperature: number; maxTokens: number };
  canSaveDefault: boolean;
  saving?: boolean;
  error?: string;
  onUse: (a: Answer) => void;
  onSaveDefault: (a: Answer) => void;
}) {
  const [instructions, setInstructions] = useState(value.instructions);
  const [temperature, setTemperature] = useState<number | string>(value.temperature);
  const [maxTokens, setMaxTokens] = useState<number | string>(value.maxTokens);
  const [seed, setSeed] = useState<number | string>(value.seed ?? "");
  const answer = (): Answer => ({
    instructions,
    temperature: Math.min(2, Math.max(0, Number(temperature) || 0)),
    maxTokens: Math.min(1024, Math.max(1, Math.round(Number(maxTokens) || defaults.maxTokens))),
    seed: seed === "" ? undefined : Number(seed),
  });
  return (
    <Drawer opened={opened} onClose={onClose} position="right" size={440} title={<Text fw={700} fz="lg">How {name} answers</Text>}>
      <Stack>
        <Textarea
          label="Instructions"
          description="Who it is and how to talk. It reads this before every message, and your apps can send their own."
          placeholder="e.g. You answer questions for a small tea shop. Keep it warm and short."
          autosize
          minRows={4}
          maxRows={12}
          maxLength={8000}
          value={instructions}
          onChange={(e) => setInstructions(e.currentTarget.value)}
        />
        <Stack gap={4}>
          <Text size="sm" fw={500}>Word choice</Text>
          <Text size="xs" c="dimmed">Steady gives the same answer each time. Wild is more surprising.</Text>
          <SegmentedControl className="self-start" data={wordChoices} value={nearest(wordChoices, Number(temperature))} onChange={(v) => setTemperature(Number(v))} />
        </Stack>
        <Stack gap={4}>
          <Text size="sm" fw={500}>Longest answer</Text>
          <SegmentedControl className="self-start" data={lengths} value={nearest(lengths, Number(maxTokens))} onChange={(v) => setMaxTokens(Number(v))} />
        </Stack>
        <Accordion variant="contained" radius="md">
          <Accordion.Item value="expert">
            <Accordion.Control><Text size="sm" fw={500}>Expert settings</Text></Accordion.Control>
            <Accordion.Panel>
              <SimpleGrid cols={2}>
                <NumberInput label="Temperature" min={0} max={2} step={0.1} decimalScale={2} size="sm" value={temperature} onChange={setTemperature} />
                <NumberInput label="Max tokens" min={1} max={1024} size="sm" value={maxTokens} onChange={setMaxTokens} />
                <NumberInput label="Seed" description="Same seed, same answer" placeholder="Random" min={0} size="sm" value={seed} onChange={setSeed} />
              </SimpleGrid>
            </Accordion.Panel>
          </Accordion.Item>
        </Accordion>
        {error && <Alert color="red" icon={<IconAlertCircle />}>{error}</Alert>}
        <Stack gap="xs" mt="sm">
          <Button onClick={() => onUse(answer())}>Use in this chat</Button>
          {canSaveDefault && <Button variant="default" loading={saving} onClick={() => onSaveDefault(answer())}>Make these the model's default</Button>}
        </Stack>
        <Text size="xs" c="dimmed">
          {canSaveDefault
            ? "The default is what apps and new chats get when they don't say."
            : `Its defaults: ${defaults.instructions ? "its own instructions, " : "no instructions, "}${wordChoices.find((w) => w.value === nearest(wordChoices, defaults.temperature))?.label.toLowerCase()} word choice.`}
        </Text>
      </Stack>
    </Drawer>
  );
}
