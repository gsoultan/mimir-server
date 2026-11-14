import { useMutation } from "@connectrpc/connect-query";
import { Alert, Badge, Button, Card, Group, Modal, Select, Stack, Text } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { IconAlertCircle, IconArrowBackUp, IconFlask } from "@tabler/icons-react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";

import { message } from "../api/client";
import { ModelService, ModelVersionSource, type Model, type ModelVersion } from "../api/gen/mimir/v1/model_pb";
import { allModelQueries, modelVersions } from "../api/queries";
import { ago } from "../lib/format";
import { score } from "../lib/training";
import { newVersionChoices } from "../lib/trial";
import { Trial, TryVersion } from "./Trial";

const from: Record<ModelVersionSource, string> = {
  [ModelVersionSource.UNSPECIFIED]: "",
  [ModelVersionSource.UPLOADED]: "Uploaded",
  [ModelVersionSource.TRAINED]: "Taught here",
  [ModelVersionSource.KEEP_LEARNING]: "Kept learning",
};

// Versions lists the weights a model has had, newest first, and lets
// people put an earlier one back, or try one on part of the questions
// first (trialable). The model keeps its id, so apps carry on.
export function Versions({ model: m, number, canWrite, trialable }: { model: Model; number: boolean; canWrite: boolean; trialable: boolean }) {
  const queryClient = useQueryClient();
  const { data, error } = useQuery(modelVersions(m.id));
  const [picked, setPicked] = useState<ModelVersion>();
  const [trying, setTrying] = useState<ModelVersion>();
  const newShare = useMutation(ModelService.method.setNewVersionShare, {
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: allModelQueries });
      notifications.show({ color: "teal", message: "Saved." });
    },
  });
  const use = useMutation(ModelService.method.useModelVersion, {
    onSuccess: async () => {
      const n = picked?.number;
      setPicked(undefined);
      await queryClient.invalidateQueries({ queryKey: allModelQueries });
      notifications.show({ color: "teal", message: `The model answers with version ${n} now.` });
    },
  });
  const versions = data?.versions ?? [];
  const live = versions.find((v) => v.live);

  return (
    <Stack gap="lg">
      {trialable && m.trial && <Trial model={m} number={number} canWrite={canWrite} />}
      <Card withBorder radius="lg" padding="lg">
        <Text fw={700} mb={4}>Versions</Text>
        <Text size="sm" c="dimmed" mb="sm">
          {versions.length > 1
            ? "Each time the model learns, the weights it had are kept, so you can go back. Apps keep using the same model id."
            : "When the model keeps learning, each pass is kept here as a version you can go back to."}
        </Text>
        {error && <Alert color="red" icon={<IconAlertCircle />}>{message(error)}</Alert>}
        <Stack gap="xs">
          {versions.map((v) => (
            <Group key={v.id} justify="space-between" wrap="nowrap" align="flex-start" className="border-t border-[var(--mantine-color-default-border)] pt-2">
              <div className="min-w-0">
                <Group gap="xs">
                  <Text fw={600}>Version {v.number}</Text>
                  {v.live && <Badge color="teal" variant="light">Answering now</Badge>}
                  {m.trial?.versionId === v.id && <Badge color="grape" variant="light">Being tried</Badge>}
                </Group>
                <Text size="sm" c="dimmed">
                  {[from[v.source], ago(v.createTime), v.accuracy > 0 && score(v.accuracy, number)].filter(Boolean).join(" · ")}
                  {v.jobId && <> · <Link to="/training/$id" params={{ id: v.jobId }}>how it learned</Link></>}
                </Text>
                {v.note && <Text size="xs" c="dimmed">{v.note}</Text>}
              </div>
              {canWrite && !v.live && (
                <Group gap="xs" wrap="nowrap" className="shrink-0">
                  {trialable && m.trial?.versionId !== v.id && (
                    <Button size="xs" variant="default" leftSection={<IconFlask size={14} />} onClick={() => setTrying(v)}>
                      Try it first
                    </Button>
                  )}
                  <Button size="xs" variant="light" leftSection={<IconArrowBackUp size={14} />} onClick={() => setPicked(v)}>
                    Use this one
                  </Button>
                </Group>
              )}
            </Group>
          ))}
        </Stack>

        {trialable && canWrite && (
          <div className="border-t border-[var(--mantine-color-default-border)] pt-3 mt-3">
            <Select
              label="When it keeps learning and does at least as well"
              description="Trying a new version on part of the questions first lets you see how it does on real ones before everyone gets it."
              data={newVersionChoices}
              value={newVersionChoices.some((c) => c.value === String(m.newVersionShare)) ? String(m.newVersionShare) : null}
              placeholder={`Try it on ${m.newVersionShare}% of questions first`}
              onChange={(v) => v !== null && newShare.mutate({ modelId: m.id, share: Number(v) })}
              allowDeselect={false}
              className="max-w-md"
            />
            {newShare.error && <Alert color="red" mt="xs" icon={<IconAlertCircle />}>{message(newShare.error)}</Alert>}
          </div>
        )}

        <TryVersion model={m} version={trying} onClose={() => setTrying(undefined)} />

        <Modal opened={!!picked} onClose={() => setPicked(undefined)} title={`Answer with version ${picked?.number}?`} centered>
          <Text mb="lg">
            From the next question on, {m.name} answers with version {picked?.number}
            {picked && picked.accuracy > 0 && ` (${score(picked.accuracy, number)})`} instead of version {live?.number}
            {live && live.accuracy > 0 && ` (${score(live.accuracy, number)})`}. Version {live?.number} stays here, so you can switch back.
          </Text>
          {use.error && <Alert color="red" mb="md" icon={<IconAlertCircle />}>{message(use.error)}</Alert>}
          <Group justify="flex-end">
            <Button variant="default" onClick={() => setPicked(undefined)}>Cancel</Button>
            <Button loading={use.isPending} onClick={() => picked && use.mutate({ modelId: m.id, versionId: picked.id })}>Use version {picked?.number}</Button>
          </Group>
        </Modal>
      </Card>
    </Stack>
  );
}
