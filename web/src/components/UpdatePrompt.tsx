import { Button, Group, Paper, Text } from "@mantine/core";
import { useRegisterSW } from "virtual:pwa-register/react";

// Tells people when a newer version of the app is ready, instead of
// swapping it under them mid-task.
export function UpdatePrompt() {
  const { needRefresh: [needRefresh, setNeedRefresh], updateServiceWorker } = useRegisterSW();
  if (!needRefresh) return null;
  return (
    <Paper withBorder shadow="md" p="md" className="fixed right-4 bottom-20 z-50 sm:bottom-4" role="status">
      <Group gap="sm">
        <Text size="sm">A new version of mimir is ready.</Text>
        <Button size="xs" onClick={() => updateServiceWorker(true)}>Update</Button>
        <Button size="xs" variant="subtle" onClick={() => setNeedRefresh(false)}>Later</Button>
      </Group>
    </Paper>
  );
}
