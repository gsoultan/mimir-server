import { useMutation } from "@connectrpc/connect-query";
import { Alert, Card, Group, Switch, Text } from "@mantine/core";
import { IconAlertCircle } from "@tabler/icons-react";
import { useQueryClient } from "@tanstack/react-query";

import { message } from "../api/client";
import { ModelService, type Architecture, type Model } from "../api/gen/mimir/v1/model_pb";
import { allModelQueries } from "../api/queries";
import { maxBytes } from "../lib/device";
import { bytes } from "../lib/format";
import { t } from "../i18n";

// OnDevice lets a model run in people's browsers, or keeps it on the
// server. The server checks the same limits.
export function OnDevice({ model: m, arch, canWrite }: { model: Model; arch?: Architecture; canWrite: boolean }) {
  const queryClient = useQueryClient();
  const update = useMutation(ModelService.method.updateModel, {
    onSuccess: () => queryClient.invalidateQueries({ queryKey: allModelQueries }),
  });
  const why = arch?.custom
    ? t("Models in your own code run on the workers that have the code, not in a browser.")
    : Number(m.sizeBytes) > maxBytes
    ? t("This model is {sizeBytes}; a browser takes models of up to {maxBytes}.", { sizeBytes: bytes(m.sizeBytes), maxBytes: bytes(maxBytes) })
    : "";
  return (
    <Card withBorder radius="lg" padding="lg">
      <Group justify="space-between" wrap="nowrap" align="flex-start">
        <div>
          <Text fw={700} mb={4}>{t("Run on people's devices")}</Text>
          <Text size="sm" c="dimmed">{t("People who can use this model can save it in their browser and ask it there, even offline. Their questions don't reach the server, so they don't count in usage.")}</Text>
        </div>
        <Switch
          size="md"
          checked={m.onDevice}
          disabled={!canWrite || (!!why && !m.onDevice) || update.isPending}
          onChange={(e) => update.mutate({ id: m.id, onDevice: e.currentTarget.checked })}
          aria-label={t("Run on people's devices")}
        />
      </Group>
      {why && !m.onDevice && <Text size="xs" c="dimmed" mt="sm">{why}</Text>}
      {m.onDevice && <Text size="xs" c="dimmed" mt="sm">{t("Downloads")}{" "}{bytes(m.sizeBytes)}{" "}{t("the first time someone asks it on their device. Retraining it offers them the new version.")}</Text>}
      {update.error && <Alert color="red" mt="sm" icon={<IconAlertCircle />}>{message(update.error)}</Alert>}
    </Card>
  );
}
