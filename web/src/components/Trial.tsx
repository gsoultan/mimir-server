import { useMutation } from "@connectrpc/connect-query";
import { Alert, Button, Card, Group, Modal, SegmentedControl, SimpleGrid, Stack, Text } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { IconAlertCircle, IconCheck, IconFlask, IconPlayerStop } from "@tabler/icons-react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import { message } from "../api/client";
import { ModelService, type Model, type ModelVersion, type TrialSide } from "../api/gen/mimir/v1/model_pb";
import { allModelQueries, modelTrial } from "../api/queries";
import { ago } from "../lib/format";
import { shareChoices, trialHeadline, trialVerdict } from "../lib/trial";
import { StatusPill } from "./ui";

// Trial shows how a version tried on part of a model's questions does
// beside the live one, and lets people put it live or stop trying it.
export function Trial({ model: m, number, canWrite }: { model: Model; number: boolean; canWrite: boolean }) {
  const queryClient = useQueryClient();
  const { data, error } = useQuery({ ...modelTrial(m.id), refetchInterval: 15_000 });
  const done = (text: string) => async () => {
    await queryClient.invalidateQueries({ queryKey: allModelQueries });
    notifications.show({ color: "teal", message: text });
  };
  const tried = data?.tried;
  const use = useMutation(ModelService.method.useModelVersion, { onSuccess: done(`Version ${tried?.versionNumber} answers every question now.`) });
  const stop = useMutation(ModelService.method.stopModelTrial, { onSuccess: done("Stopped trying it. The live version answers every question.") });
  const t = data?.trial ?? m.trial;
  if (!t) return null;
  const verdict = data ? trialVerdict(data, number) : undefined;
  const right = number ? "close" : "right";

  const column = (s: TrialSide | undefined, live: boolean) => (
    <Card withBorder radius="md" padding="md">
      <Group gap="xs" mb={4}>
        <Text fw={600}>Version {s?.versionNumber || "…"}</Text>
        <Text size="sm" c="dimmed">{live ? "answering now" : "being tried"}</Text>
      </Group>
      <Text size="sm">{Number(s?.answers ?? 0n).toLocaleString()} answers{t.shadow && !live ? " on the side" : ""}</Text>
      <Text size="sm" c="dimmed">
        {s && s.feedback > 0n ? `${Math.round(s.accuracy * 100)} of 100 ${right}, of ${Number(s.feedback).toLocaleString()} checked` : "none checked yet"}
      </Text>
    </Card>
  );

  return (
    <Card withBorder radius="lg" padding="lg">
      <Group justify="space-between" mb={4}>
        <Text fw={700}>Trying a new version</Text>
        {verdict && verdict.tone !== "wait" && <StatusPill tone={verdict.tone}>{verdict.tone === "ok" ? "Looks ready" : "Does worse"}</StatusPill>}
      </Group>
      <Text size="sm" c="dimmed" mb="sm">
        {trialHeadline(t, tried?.versionNumber ?? 0)} Started {ago(t.startTime)}.
      </Text>
      {error && <Alert color="red" mb="sm" icon={<IconAlertCircle />}>{message(error)}</Alert>}
      <SimpleGrid cols={{ base: 1, sm: 2 }} mb="sm">
        {column(data?.live, true)}
        {column(tried, false)}
      </SimpleGrid>
      {verdict?.text && <Text size="sm" mb="sm">{verdict.text}</Text>}
      {(use.error || stop.error) && <Alert color="red" mb="sm" icon={<IconAlertCircle />}>{message((use.error ?? stop.error)!)}</Alert>}
      {canWrite && (
        <Group gap="xs">
          <Button size="xs" leftSection={<IconCheck size={14} />} loading={use.isPending} onClick={() => use.mutate({ modelId: m.id, versionId: t.versionId })}>
            Answer every question with version {tried?.versionNumber}
          </Button>
          <Button size="xs" variant="default" leftSection={<IconPlayerStop size={14} />} loading={stop.isPending} onClick={() => stop.mutate({ modelId: m.id })}>
            Stop trying it
          </Button>
        </Group>
      )}
    </Card>
  );
}

// TryVersion asks how to try a version before it goes live: on a share of
// the questions, or on a copy of every one with people still getting the
// live version's answers.
export function TryVersion({ model: m, version: v, onClose }: { model: Model; version?: ModelVersion; onClose: () => void }) {
  const queryClient = useQueryClient();
  const [how, setHow] = useState("share");
  const [share, setShare] = useState("10");
  const start = useMutation(ModelService.method.startModelTrial, {
    onSuccess: async () => {
      onClose();
      await queryClient.invalidateQueries({ queryKey: allModelQueries });
      notifications.show({ color: "teal", message: `Trying version ${v?.number} now.` });
    },
  });
  return (
    <Modal opened={!!v} onClose={onClose} title={`Try version ${v?.number} first`} centered>
      <Stack gap="sm">
        <Text size="sm">
          Before version {v?.number} answers everyone, see how it does on real questions. You can put it live or stop at any time.
        </Text>
        <SegmentedControl fullWidth value={how} onChange={setHow} data={[{ value: "share", label: "On some questions" }, { value: "shadow", label: "On the side" }]} />
        {how === "share" ? (
          <>
            <Text size="sm" c="dimmed">It answers this share of the questions, and those apps get its answer:</Text>
            <SegmentedControl className="self-start" data={shareChoices} value={share} onChange={setShare} />
          </>
        ) : (
          <Text size="sm" c="dimmed">
            It answers a copy of every question, but apps keep getting the live version's answers. You see how often the two agree, with no risk to anyone.
          </Text>
        )}
        {start.error && <Alert color="red" icon={<IconAlertCircle />}>{message(start.error)}</Alert>}
        {m.trial && <Text size="xs" c="dimmed">This replaces the version being tried now.</Text>}
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>Cancel</Button>
          <Button
            leftSection={<IconFlask size={16} />}
            loading={start.isPending}
            onClick={() => v && start.mutate({ modelId: m.id, versionId: v.id, share: Number(share), shadow: how === "shadow" })}
          >
            Start trying it
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}
