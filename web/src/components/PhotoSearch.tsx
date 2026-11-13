import { ConnectError, Code, createClient } from "@connectrpc/connect";
import { ActionIcon, Alert, Badge, Button, Card, Group, Loader, Progress, SegmentedControl, SimpleGrid, Stack, TagsInput, Text, TextInput } from "@mantine/core";
import { Dropzone } from "@mantine/dropzone";
import { IconAlertCircle, IconPlayerStopFilled, IconX } from "@tabler/icons-react";
import { useEffect, useRef, useState } from "react";

import { message, transport } from "../api/client";
import { MediaService } from "../api/gen/mimir/v1/media_pb";
import { shrinkPhoto } from "../lib/media";

const media = createClient(MediaService, transport);

// The most photos and labels the server takes at once.
const maxPhotos = 64;
const maxLabels = 64;

type Photo = { name: string; bytes: Uint8Array; url: string };
type Result = { mode: "find" | "sort"; texts: string[]; similarity: number[][]; chance: number[][]; seconds: number };

// PhotoSearch tries a photo and words model: it finds the photos that
// fit a description, or sorts photos into labels.
export function PhotoSearch({ modelId }: { modelId: string }) {
  const [photos, setPhotos] = useState<Photo[]>([]);
  const [mode, setMode] = useState<"find" | "sort">("find");
  const [query, setQuery] = useState("");
  const [labels, setLabels] = useState<string[]>([]);
  const [reading, setReading] = useState(false);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<Result>();
  const [error, setError] = useState<string>();
  const cancel = useRef<AbortController | null>(null);
  const shown = useRef<string[]>([]);
  shown.current = photos.map((p) => p.url);
  useEffect(() => () => { cancel.current?.abort(); shown.current.forEach((u) => URL.revokeObjectURL(u)); }, []);

  const add = async (files: File[]) => {
    setError(undefined);
    setResult(undefined);
    setReading(true);
    const room = maxPhotos - photos.length;
    const added: Photo[] = [];
    let unread = 0;
    for (const f of files.slice(0, room)) {
      try {
        added.push({ name: f.name, ...(await shrinkPhoto(f)) });
      } catch {
        unread++;
      }
    }
    setPhotos((p) => [...p, ...added]);
    setReading(false);
    if (files.length > room) setError(`Up to ${maxPhotos} photos at once, so ${files.length - room} were left out.`);
    else if (unread > 0) setError(`${unread === 1 ? "One file isn't a photo" : `${unread} files aren't photos`} this browser can read.`);
  };
  const remove = (i: number) => {
    setResult(undefined);
    setPhotos((p) => { URL.revokeObjectURL(p[i]!.url); return p.filter((_, j) => j !== i); });
  };

  const texts = mode === "find" ? (query.trim() ? [query.trim()] : []) : labels;
  const ready = photos.length > 0 && (mode === "find" ? texts.length === 1 : texts.length >= 2);

  const match = async () => {
    setError(undefined);
    setResult(undefined);
    setBusy(true);
    const ctl = new AbortController();
    cancel.current = ctl;
    try {
      const res = await media.matchPhotos({ modelId, photos: photos.map((p) => p.bytes), texts }, { signal: ctl.signal });
      setResult({
        mode,
        texts,
        similarity: res.photos.map((p) => p.similarity),
        chance: res.photos.map((p) => p.chance),
        seconds: Number(res.durationMicros) / 1e6,
      });
    } catch (err) {
      if (ConnectError.from(err).code !== Code.Canceled) setError(message(err));
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
            <Dropzone
              onDrop={(f) => void add(f)}
              onReject={() => setError("Pick JPG, PNG, GIF or WebP photos.")}
              accept={["image/jpeg", "image/png", "image/gif", "image/webp"]}
              disabled={busy || reading || photos.length >= maxPhotos}
              loading={reading}
              radius="md"
            >
              <Stack align="center" gap={2} py="md" className="pointer-events-none">
                <Text fw={600}>Drop photos here</Text>
                <Text size="sm" c="dimmed">or click to choose them, up to {maxPhotos}</Text>
              </Stack>
            </Dropzone>
            {photos.length > 0 && (
              <Group gap="xs">
                {photos.map((p, i) => (
                  <div key={p.url} className="relative">
                    <img src={p.url} alt={p.name} className="h-16 w-16 rounded-md object-cover" />
                    <ActionIcon size="xs" radius="xl" variant="default" className="!absolute -right-1.5 -top-1.5" aria-label={`Remove ${p.name}`} onClick={() => remove(i)} disabled={busy}>
                      <IconX size={12} />
                    </ActionIcon>
                  </div>
                ))}
              </Group>
            )}
            <Stack gap={4}>
              <Text size="sm" fw={500}>What to do</Text>
              <SegmentedControl
                className="self-start"
                data={[{ value: "find", label: "Find photos" }, { value: "sort", label: "Sort into labels" }]}
                value={mode}
                onChange={(v) => { setResult(undefined); setMode(v === "sort" ? "sort" : "find"); }}
              />
            </Stack>
            {mode === "find" ? (
              <TextInput
                label="Describe the photo"
                description="In English, the way you would caption it."
                placeholder="a dog playing on the beach"
                maxLength={300}
                value={query}
                onChange={(e) => { setResult(undefined); setQuery(e.currentTarget.value); }}
              />
            ) : (
              <TagsInput
                label="Labels"
                description="Two or more, in English. Press Enter after each."
                placeholder="a cat, a dog, a car"
                maxTags={maxLabels}
                value={labels}
                onChange={(v) => { setResult(undefined); setLabels(v.map((l) => l.slice(0, 300))); }}
                clearable
              />
            )}
          </Stack>
        </Card>
        {busy
          ? <Button size="lg" variant="default" leftSection={<IconPlayerStopFilled size={16} />} onClick={() => cancel.current?.abort()}>Stop</Button>
          : <Button size="lg" disabled={!ready} onClick={() => void match()}>{mode === "find" ? "Find" : "Sort"}</Button>}
      </Stack>
      <Stack>
        {error && <Alert color="red" icon={<IconAlertCircle />}>{error}</Alert>}
        {busy ? (
          <Card withBorder radius="lg" padding="lg" aria-live="polite">
            <Group gap="sm"><Loader size="sm" /><Text>Looking at the photos…</Text></Group>
          </Card>
        ) : result && result.similarity.length === photos.length ? (
          <Card withBorder radius="lg" padding="lg" aria-live="polite">
            {result.mode === "find" ? <Found result={result} photos={photos} /> : <Sorted result={result} photos={photos} />}
            <Text size="xs" c="dimmed" mt="md">Looked at {photos.length} photo{photos.length === 1 ? "" : "s"} in {result.seconds.toFixed(1)} s.</Text>
          </Card>
        ) : (
          <Card withBorder radius="lg" padding="lg" className="border-dashed">
            <Text c="dimmed">Add some photos. Then describe the one you're looking for to see them best match first, or give labels to see which label fits each photo.</Text>
          </Card>
        )}
      </Stack>
    </SimpleGrid>
  );
}

// Found lists the photos best match first, each with how well it fits
// next to the others.
function Found({ result, photos }: { result: Result; photos: Photo[] }) {
  const scores = result.similarity.map((s) => s[0] ?? 0);
  const lo = Math.min(...scores), hi = Math.max(...scores);
  const order = scores.map((_, i) => i).sort((a, b) => scores[b]! - scores[a]!);
  return (
    <Stack gap="sm">
      <Text fw={650}>Best match first for “{result.texts[0]}”</Text>
      {order.map((i, rank) => (
        <Group key={photos[i]!.url} gap="sm" wrap="nowrap">
          <img src={photos[i]!.url} alt={photos[i]!.name} className="h-16 w-16 shrink-0 rounded-md object-cover" />
          <Stack gap={4} className="min-w-0 flex-1">
            <Group gap="xs" wrap="nowrap">
              <Text size="sm" truncate>{photos[i]!.name}</Text>
              {rank === 0 && <Badge size="sm" variant="light" color="teal">Best match</Badge>}
            </Group>
            <Progress value={hi > lo ? 10 + (90 * (scores[i]! - lo)) / (hi - lo) : 100} size="sm" color="teal" aria-label="How well it fits" />
          </Stack>
        </Group>
      ))}
    </Stack>
  );
}

// Sorted gives each photo the label that fits it best, with its chance.
function Sorted({ result, photos }: { result: Result; photos: Photo[] }) {
  return (
    <SimpleGrid cols={{ base: 2, sm: 3 }} spacing="sm">
      {photos.map((p, i) => {
        const chance = result.chance[i] ?? [];
        const best = chance.reduce((b, c, j) => (c > (chance[b] ?? 0) ? j : b), 0);
        return (
          <Stack key={p.url} gap={4}>
            <img src={p.url} alt={p.name} className="aspect-square w-full rounded-md object-cover" />
            <Text size="sm" fw={600} lineClamp={2}>{result.texts[best]}</Text>
            <Text size="xs" c="dimmed">{Math.round((chance[best] ?? 0) * 100)}% sure</Text>
          </Stack>
        );
      })}
    </SimpleGrid>
  );
}
