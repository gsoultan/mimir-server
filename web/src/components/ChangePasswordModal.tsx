import { useMutation } from "@connectrpc/connect-query";
import { Alert, Button, Group, Modal, PasswordInput, Stack } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { IconAlertCircle } from "@tabler/icons-react";
import { useState } from "react";

import { message } from "../api/client";
import { AuthService } from "../api/gen/mimir/v1/access_pb";

export function ChangePasswordModal({ opened, onClose }: { opened: boolean; onClose: () => void }) {
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const change = useMutation(AuthService.method.changePassword, {
    onSuccess: () => {
      notifications.show({ color: "teal", message: "Password changed. Other devices were signed out." });
      setCurrent(""); setNext(""); onClose();
    },
  });
  return (
    <Modal opened={opened} onClose={onClose} title="Change password" centered>
      <form onSubmit={(e) => { e.preventDefault(); change.mutate({ currentPassword: current, newPassword: next }); }}>
        <Stack>
          <PasswordInput label="Current password" autoComplete="current-password" required value={current} onChange={(e) => setCurrent(e.currentTarget.value)} data-autofocus />
          <PasswordInput label="New password" description="At least 10 characters." autoComplete="new-password" required minLength={10} value={next} onChange={(e) => setNext(e.currentTarget.value)} />
          {change.error && <Alert color="red" icon={<IconAlertCircle />}>{message(change.error)}</Alert>}
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>Cancel</Button>
            <Button type="submit" loading={change.isPending}>Change password</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}
