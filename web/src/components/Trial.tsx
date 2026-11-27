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
import { locale, t } from "../i18n";
import { count } from "../lib/templates";

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
  const use = useMutation(ModelService.method.useModelVersion, { onSuccess: done(t("Version {versionNumber} answers every question now.", { versionNumber: tried?.versionNumber })) });
  const stop = useMutation(ModelService.method.stopModelTrial, { onSuccess: done(t("Stopped trying it. The live version answers every question.")) });
  const trial = data?.trial ?? m.trial;
  if (!trial) return null;
  const verdict = data ? trialVerdict(data, number) : undefined;
  const right = number ? t("close") : t("right");

  const column = (s: TrialSide | undefined, live: boolean) => (
    <Card withBorder radius="md" padding="md">
      <Group gap="xs" mb={4}>
        <Text fw={600}>{t("Version {number}", { number: s?.versionNumber || "…" })}</Text>
        <Text size="sm" c="dimmed">{live ? t("answering now") : t("being tried")}</Text>
      </Group>
      <Text size="sm">{trial.shadow && !live ? t("{n} answers on the side", { n: Number(s?.answers ?? 0n).toLocaleString(locale) }) : count(Number(s?.answers ?? 0n), "answer")}</Text>
      <Text size="sm" c="dimmed">
        {s && s.feedback > 0n ? t("{n1} of 100 {right}, of {n3} checked", { n1: Math.round(s.accuracy * 100), right, n3: Number(s.feedback).toLocaleString(locale) }) : t("none checked yet")}
      </Text>
    </Card>
  );

  return (
    <Card withBorder radius="lg" padding="lg">
      <Group justify="space-between" mb={4}>
        <Text fw={700}>{t("Trying a new version")}</Text>
        {verdict && verdict.tone !== "wait" && <StatusPill tone={verdict.tone}>{verdict.tone === "ok" ? t("Looks ready") : t("Does worse")}</StatusPill>}
      </Group>
      <Text size="sm" c="dimmed" mb="sm">
        {trialHeadline(trial, tried?.versionNumber ?? 0)}{" "}{t("Started {when}.", { when: ago(trial.startTime) })}
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
          <Button size="xs" leftSection={<IconCheck size={14} />} loading={use.isPending} onClick={() => use.mutate({ modelId: m.id, versionId: trial.versionId })}>
            {t("Answer every question with version {number}", { number: tried?.versionNumber })}
          </Button>
          <Button size="xs" variant="default" leftSection={<IconPlayerStop size={14} />} loading={stop.isPending} onClick={() => stop.mutate({ modelId: m.id })}>{t("Stop trying it")}</Button>
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
      notifications.show({ color: "teal", message: t("Trying version {number} now.", { number: v?.number }) });
    },
  });
  return (
    <Modal opened={!!v} onClose={onClose} title={t("Try version {number} first", { number: v?.number })} centered>
      <Stack gap="sm">
        <Text size="sm">{t("Before version {number} answers everyone, see how it does on real questions. You can put it live or stop at any time.", { number: v?.number })}</Text>
        <SegmentedControl fullWidth value={how} onChange={setHow} data={[{ value: "share", label: t("On some questions") }, { value: "shadow", label: t("On the side") }]} />
        {how === "share" ? (
          <>
            <Text size="sm" c="dimmed">{t("It answers this share of the questions, and those apps get its answer:")}</Text>
            <SegmentedControl className="self-start" data={shareChoices} value={share} onChange={setShare} />
          </>
        ) : (
          <Text size="sm" c="dimmed">{t("It answers a copy of every question, but apps keep getting the live version's answers. You see how often the two agree, with no risk to anyone.")}</Text>
        )}
        {start.error && <Alert color="red" icon={<IconAlertCircle />}>{message(start.error)}</Alert>}
        {m.trial && <Text size="xs" c="dimmed">{t("This replaces the version being tried now.")}</Text>}
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>{t("Cancel")}</Button>
          <Button
            leftSection={<IconFlask size={16} />}
            loading={start.isPending}
            onClick={() => v && start.mutate({ modelId: m.id, versionId: v.id, share: Number(share), shadow: how === "shadow" })}
          >{t("Start trying it")}</Button>
        </Group>
      </Stack>
    </Modal>
  );
}
