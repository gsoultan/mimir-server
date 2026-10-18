import { useMutation } from "@connectrpc/connect-query";
import { Alert, Button, Card, SegmentedControl, SimpleGrid, Stack, Text, Textarea } from "@mantine/core";
import { IconAlertCircle } from "@tabler/icons-react";
import { useState } from "react";

import { message } from "../api/client";
import { PredictService } from "../api/gen/mimir/v1/model_pb";

// How freely the model picks its words, as a temperature.
const styles = [
  { value: "0", label: "Steady" },
  { value: "0.7", label: "Varied" },
  { value: "1.2", label: "Wild" },
];

// Writer tries a language model: it continues the text you start.
export function Writer({ modelId }: { modelId: string }) {
  const [prompt, setPrompt] = useState("");
  const [style, setStyle] = useState("0");
  const generate = useMutation(PredictService.method.generate);
  const write = () => generate.mutate({
    modelId,
    prompt,
    maxTokens: 128,
    temperature: Number(style),
    seed: BigInt(Math.floor(Math.random() * 2 ** 32)),
  });
  const g = generate.data;

  return (
    <SimpleGrid cols={{ base: 1, md: 2 }} spacing="xl">
      <Stack>
        <Card withBorder radius="lg" padding="lg">
          <Stack gap="sm">
            <Textarea
              label="Start of the text"
              description="The model carries on from where you stop."
              placeholder="Once upon a time"
              autosize
              minRows={4}
              maxRows={14}
              value={prompt}
              onChange={(e) => { generate.reset(); setPrompt(e.currentTarget.value); }}
            />
            <Stack gap={4}>
              <Text size="sm" fw={500}>Word choice</Text>
              <SegmentedControl className="self-start" data={styles} value={style} onChange={setStyle} />
            </Stack>
          </Stack>
        </Card>
        <Button size="lg" loading={generate.isPending} disabled={!prompt.trim()} onClick={write}>Write</Button>
      </Stack>
      <Stack>
        {generate.error && <Alert color="red" icon={<IconAlertCircle />}>{message(generate.error)}</Alert>}
        {g ? (
          <Card withBorder radius="lg" padding="lg" aria-live="polite">
            <Text className="whitespace-pre-wrap">
              <Text span c="dimmed">{prompt}</Text>
              <Text span fw={600}>{g.text}</Text>
            </Text>
            <Text size="xs" c="dimmed" mt="md">
              Wrote {g.generatedTokens} word pieces in {(Number(g.durationMicros) / 1e6).toFixed(1)} s{g.finished ? " and came to an end." : "."}
            </Text>
          </Card>
        ) : (
          <Card withBorder radius="lg" padding="lg" className="border-dashed">
            <Text c="dimmed">Write the start of a text, then press Write. The model adds up to a paragraph or so. Steady always writes the same thing; Varied and Wild pick less likely words.</Text>
          </Card>
        )}
      </Stack>
    </SimpleGrid>
  );
}
