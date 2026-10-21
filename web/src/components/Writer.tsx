import { ConnectError, Code, createClient } from "@connectrpc/connect";
import { Alert, Button, Card, SegmentedControl, SimpleGrid, Stack, Text, Textarea } from "@mantine/core";
import { IconAlertCircle, IconPlayerStopFilled } from "@tabler/icons-react";
import { useEffect, useRef, useState } from "react";

import { message, transport } from "../api/client";
import { PredictService } from "../api/gen/mimir/v1/model_pb";
import { wordChoices } from "../lib/chats";

const predict = createClient(PredictService, transport);

type Result = { text: string; done: boolean; tokens: number; seconds: number; finished: boolean };

// Writer tries a language model: it continues the text you start, word by
// word as the model writes.
export function Writer({ modelId }: { modelId: string }) {
  const [prompt, setPrompt] = useState("");
  const [style, setStyle] = useState("0");
  const [result, setResult] = useState<Result>();
  const [error, setError] = useState<string>();
  const stop = useRef<AbortController | null>(null);
  useEffect(() => () => stop.current?.abort(), []);
  const writing = !!result && !result.done;

  const write = async () => {
    setError(undefined);
    const ctl = new AbortController();
    stop.current = ctl;
    let r: Result = { text: "", done: false, tokens: 0, seconds: 0, finished: false };
    setResult(r);
    try {
      for await (const res of predict.generateStream({
        modelId,
        prompt,
        maxTokens: 128,
        temperature: Number(style),
        seed: BigInt(Math.floor(Math.random() * 2 ** 32)),
      }, { signal: ctl.signal })) {
        r = res.done
          ? { ...r, done: true, tokens: res.generatedTokens, seconds: Number(res.durationMicros) / 1e6, finished: res.finished }
          : { ...r, text: r.text + res.text };
        setResult(r);
      }
    } catch (err) {
      if (ConnectError.from(err).code === Code.Canceled) {
        setResult({ ...r, done: true });
      } else {
        setResult(undefined);
        setError(message(err));
      }
    } finally {
      stop.current = null;
    }
  };

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
              onChange={(e) => { if (!writing) setResult(undefined); setPrompt(e.currentTarget.value); }}
            />
            <Stack gap={4}>
              <Text size="sm" fw={500}>Word choice</Text>
              <SegmentedControl className="self-start" data={wordChoices} value={style} onChange={setStyle} />
            </Stack>
          </Stack>
        </Card>
        {writing
          ? <Button size="lg" variant="default" leftSection={<IconPlayerStopFilled size={16} />} onClick={() => stop.current?.abort()}>Stop</Button>
          : <Button size="lg" disabled={!prompt.trim()} onClick={() => void write()}>Write</Button>}
      </Stack>
      <Stack>
        {error && <Alert color="red" icon={<IconAlertCircle />}>{error}</Alert>}
        {result ? (
          <Card withBorder radius="lg" padding="lg" aria-live="polite">
            <Text className="whitespace-pre-wrap">
              <Text span c="dimmed">{prompt}</Text>
              <Text span fw={600}>{result.text}</Text>
              {writing && <span className="ml-0.5 inline-block h-[1.1em] w-[0.5em] translate-y-[3px] animate-pulse rounded-sm" style={{ background: "var(--mantine-color-teal-7)" }} />}
            </Text>
            <Text size="xs" c="dimmed" mt="md">
              {writing
                ? "Writing…"
                : result.tokens > 0
                ? `Wrote ${result.tokens} word pieces in ${result.seconds.toFixed(1)} s${result.finished ? " and came to an end." : "."}`
                : "Stopped."}
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
