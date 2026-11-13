import { ConnectError, Code, createClient } from "@connectrpc/connect";
import { Alert, Button, Card, CopyButton, FileInput, Group, Loader, Select, SimpleGrid, Stack, Switch, Text } from "@mantine/core";
import { IconAlertCircle, IconCheck, IconCopy, IconMicrophone, IconMusic, IconPlayerStopFilled } from "@tabler/icons-react";
import { useEffect, useRef, useState } from "react";

import { message, transport } from "../api/client";
import { MediaService, type TranscribeResponse } from "../api/gen/mimir/v1/media_pb";
import { clock, toSpeech } from "../lib/media";

const media = createClient(MediaService, transport);

// languages the speech models know, the most spoken first. The model
// finds the language itself when none is picked.
const languages = [
  ["en", "English"], ["zh", "Chinese"], ["es", "Spanish"], ["hi", "Hindi"], ["ar", "Arabic"], ["pt", "Portuguese"],
  ["id", "Indonesian"], ["ms", "Malay"], ["fr", "French"], ["de", "German"], ["ja", "Japanese"], ["ru", "Russian"],
  ["ko", "Korean"], ["it", "Italian"], ["tr", "Turkish"], ["vi", "Vietnamese"], ["th", "Thai"], ["nl", "Dutch"],
  ["pl", "Polish"], ["uk", "Ukrainian"], ["sv", "Swedish"], ["tl", "Tagalog"], ["jw", "Javanese"], ["su", "Sundanese"],
].map(([value, label]) => ({ value: value!, label: label! }));
const languageName = (code: string) => languages.find((l) => l.value === code)?.label ?? code;

// Listen tries a speech model: it writes down what is said in a
// recording, or translates it into English.
export function Listen({ modelId }: { modelId: string }) {
  const [file, setFile] = useState<File | null>(null);
  const [heard, setHeard] = useState<string>();
  const [language, setLanguage] = useState<string | null>(null);
  const [translate, setTranslate] = useState(false);
  const [recording, setRecording] = useState<{ since: number; stop: () => void }>();
  const [now, setNow] = useState(0);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<TranscribeResponse>();
  const [error, setError] = useState<string>();
  const cancel = useRef<AbortController | null>(null);
  useEffect(() => () => cancel.current?.abort(), []);
  useEffect(() => {
    if (!file) return setHeard(undefined);
    const url = URL.createObjectURL(file);
    setHeard(url);
    return () => URL.revokeObjectURL(url);
  }, [file]);
  useEffect(() => {
    if (!recording) return;
    const t = setInterval(() => setNow(Date.now()), 500);
    return () => clearInterval(t);
  }, [recording]);

  const pick = (f: File | null) => { setResult(undefined); setError(undefined); setFile(f); };

  const record = async () => {
    setError(undefined);
    let stream: MediaStream;
    try {
      stream = await navigator.mediaDevices.getUserMedia({ audio: true });
    } catch {
      setError("The browser didn't let this page use the microphone. Allow it in the address bar, or choose a recording instead.");
      return;
    }
    const rec = new MediaRecorder(stream);
    const parts: Blob[] = [];
    rec.ondataavailable = (e) => parts.push(e.data);
    rec.onstop = () => {
      stream.getTracks().forEach((t) => t.stop());
      setRecording(undefined);
      const type = rec.mimeType || "audio/webm";
      pick(new File(parts, `Recording.${type.includes("mp4") ? "m4a" : type.includes("ogg") ? "ogg" : "webm"}`, { type }));
    };
    rec.start();
    setNow(Date.now());
    setRecording({ since: Date.now(), stop: () => rec.stop() });
  };

  const transcribe = async () => {
    if (!file) return;
    setError(undefined);
    setResult(undefined);
    setBusy(true);
    const ctl = new AbortController();
    cancel.current = ctl;
    try {
      let rec;
      try {
        rec = await toSpeech(file);
      } catch {
        throw new Error("This browser can't play that file. Try a WAV, MP3 or M4A recording.");
      }
      const res = await media.transcribe({ modelId, recording: rec.wav, language: language ?? "", translate }, { signal: ctl.signal });
      setResult(res);
    } catch (err) {
      if (ConnectError.from(err).code !== Code.Canceled) setError(err instanceof Error && !(err instanceof ConnectError) ? err.message : message(err));
    } finally {
      cancel.current = null;
      setBusy(false);
    }
  };

  return (
    <SimpleGrid cols={{ base: 1, md: 2 }} spacing="xl">
      <Stack>
        <Card withBorder radius="lg" padding="lg">
          <Stack gap="sm">
            <FileInput
              label="Recording"
              description="Speech in any sound or video file the browser plays, such as WAV, MP3 or M4A, up to 2 hours."
              accept="audio/*,video/*"
              leftSection={<IconMusic size={18} />}
              placeholder="Choose a recording"
              value={file}
              onChange={pick}
              disabled={busy || !!recording}
              clearable
            />
            {recording ? (
              <Button variant="light" color="red" className="self-start" leftSection={<IconPlayerStopFilled size={16} />} onClick={recording.stop}>
                Stop recording ({clock((now - recording.since) / 1000)})
              </Button>
            ) : (
              <Button variant="default" className="self-start" leftSection={<IconMicrophone size={16} />} onClick={() => void record()} disabled={busy}>
                Record from the microphone
              </Button>
            )}
            {heard && <audio controls src={heard} className="w-full" aria-label="The recording you picked" />}
            <Select
              label="Language spoken"
              description="Leave empty and the model works it out."
              placeholder="Find it out"
              data={languages}
              value={language}
              onChange={(v) => { setResult(undefined); setLanguage(v); }}
              searchable
              clearable
            />
            <Switch
              label="Translate into English"
              description="Write down the English meaning instead of the words as spoken."
              checked={translate}
              onChange={(e) => { setResult(undefined); setTranslate(e.currentTarget.checked); }}
            />
          </Stack>
        </Card>
        {busy
          ? <Button size="lg" variant="default" leftSection={<IconPlayerStopFilled size={16} />} onClick={() => cancel.current?.abort()}>Stop</Button>
          : <Button size="lg" disabled={!file || !!recording} onClick={() => void transcribe()}>Write it down</Button>}
      </Stack>
      <Stack>
        {error && <Alert color="red" icon={<IconAlertCircle />}>{error}</Alert>}
        {busy ? (
          <Card withBorder radius="lg" padding="lg" aria-live="polite">
            <Group gap="sm"><Loader size="sm" /><Text>Listening… a long recording takes a while.</Text></Group>
          </Card>
        ) : result ? (
          <Card withBorder radius="lg" padding="lg" aria-live="polite">
            {result.parts.length === 0 ? (
              <Text c="dimmed">The model heard no speech in this recording.</Text>
            ) : (
              <Stack gap="sm">
                {result.parts.map((p, i) => (
                  <Group key={i} gap="sm" wrap="nowrap" align="flex-start">
                    <Text size="xs" c="dimmed" className="w-12 shrink-0 pt-1 tabular-nums">{clock(p.start)}</Text>
                    <Text className="whitespace-pre-wrap">{p.text}</Text>
                  </Group>
                ))}
              </Stack>
            )}
            <Group justify="space-between" mt="md" gap="xs">
              <Text size="xs" c="dimmed">
                {`${result.language ? `${languageName(result.language)}. ` : ""}Heard ${clock(result.seconds)} in ${(Number(result.durationMicros) / 1e6).toFixed(1)} s.`}
              </Text>
              {result.text && (
                <CopyButton value={result.text}>
                  {({ copied, copy }) => <Button variant="subtle" color="gray" size="compact-xs" leftSection={copied ? <IconCheck size={14} /> : <IconCopy size={14} />} onClick={copy}>{copied ? "Copied" : "Copy"}</Button>}
                </CopyButton>
              )}
            </Group>
          </Card>
        ) : (
          <Card withBorder radius="lg" padding="lg" className="border-dashed">
            <Text c="dimmed">Choose a recording or record one, then press Write it down. The model writes what it hears, with the time each part starts.</Text>
          </Card>
        )}
      </Stack>
    </SimpleGrid>
  );
}
