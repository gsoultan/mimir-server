import { useMutation } from "@connectrpc/connect-query";
import { Alert, Button, Collapse, Group, NumberInput, Stack, Text, UnstyledButton } from "@mantine/core";
import { useDisclosure } from "@mantine/hooks";
import { notifications } from "@mantine/notifications";
import { IconAlertCircle, IconSparkles } from "@tabler/icons-react";
import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import { message } from "../api/client";
import { DatasetService, type Dataset } from "../api/gen/mimir/v1/training_pb";
import { allDatasetQueries } from "../api/queries";
import { t } from "../i18n";

// suggestedRows is how many rows to offer to make: five times as many as
// there are, at least 1,000 and at most 100,000.
export function suggestedRows(rows: number) {
  return Math.min(100_000, Math.max(1_000, Math.round((rows * 5) / 100) * 100));
}

// MakeRows offers to make up more rows like a table's own, for when there
// are too few real ones; onMade gets the new dataset. For rows that were
// made up already, it says so instead.
export function MakeRows({ dataset, from, onMade }: { dataset: Dataset; from?: Dataset; onMade: (d: Dataset) => void }) {
  const queryClient = useQueryClient();
  const [open, { toggle }] = useDisclosure(false);
  const [rows, setRows] = useState<number | string>(suggestedRows(Number(dataset.rows)));
  const make = useMutation(DatasetService.method.makeRows, {
    onSuccess: async (res) => {
      await queryClient.invalidateQueries({ queryKey: allDatasetQueries });
      if (!res.dataset) return;
      notifications.show({ color: "teal", icon: <IconSparkles size={18} />, message: t("Made up {n1} rows like yours. The model will learn from them.", { n1: res.dataset.rows.toLocaleString() }) });
      onMade(res.dataset);
    },
  });

  if (dataset.madeFrom) {
    return (
      <Alert mt="sm" color="orange" variant="light" icon={<IconSparkles />}>
        {from
          ? t("These rows are made up, learned from {name}. Before you rely on the model, try it on real rows.", { name: from.name })
          : t("These rows are made up. Before you rely on the model, try it on real rows.")}
      </Alert>
    );
  }
  return (
    <Stack gap={4} mt="sm">
      <UnstyledButton onClick={toggle} className="self-start text-sm text-[var(--mantine-color-teal-7)] underline">
        {open ? t("Hide making more rows") : t("Too few rows? Make up more like these")}
      </UnstyledButton>
      <Collapse expanded={open}>
        <Stack gap="xs" mt="xs" maw={520}>
          <Text size="sm" c="dimmed">
            {t("It learns how the values in each column spread out and how the columns go together, then draws new rows that follow the same patterns. IDs are made new. Text cells are reused from your rows. The new rows are saved as a separate set of examples, marked as made up, and your own rows stay as they are.")}
          </Text>
          <Group align="flex-end" gap="sm">
            <NumberInput label={t("How many rows to make")} min={10} max={1_000_000} step={500} thousandSeparator="," value={rows} onChange={setRows} w={200} />
            <Button leftSection={<IconSparkles size={16} />} loading={make.isPending} disabled={!(Number(rows) >= 10)} onClick={() => make.mutate({ datasetId: dataset.id, rows: BigInt(Math.floor(Number(rows))) })}>
              {t("Make rows")}
            </Button>
          </Group>
          {make.error && <Alert color="red" icon={<IconAlertCircle />}>{message(make.error)}</Alert>}
        </Stack>
      </Collapse>
    </Stack>
  );
}
