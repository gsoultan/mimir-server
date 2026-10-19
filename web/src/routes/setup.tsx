import { useMutation } from "@connectrpc/connect-query";
import { Alert, Button, PasswordInput, Stack, TextInput } from "@mantine/core";
import { IconAlertCircle } from "@tabler/icons-react";
import { createFileRoute, redirect, useNavigate } from "@tanstack/react-router";
import { useState } from "react";

import { message } from "../api/client";
import { AuthService } from "../api/gen/mimir/v1/access_pb";
import { setupState } from "../api/queries";
import { AuthCard } from "../components/AuthCard";

export const Route = createFileRoute("/setup")({
  beforeLoad: async ({ context }) => {
    const { needsSetup } = await context.queryClient.fetchQuery(setupState);
    if (!needsSetup) throw redirect({ to: "/sign-in" });
  },
  component: Setup,
});

function Setup() {
  const navigate = useNavigate();
  const { queryClient } = Route.useRouteContext();
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [setupToken, setSetupToken] = useState("");
  const setup = useMutation(AuthService.method.setup, {
    onSuccess: async () => { queryClient.clear(); await navigate({ to: "/" }); },
  });

  return (
    <AuthCard title="Welcome to mimir" intro="This server has no accounts yet. Make yours; you'll be its owner.">
      <form onSubmit={(e) => { e.preventDefault(); setup.mutate({ name, email, password, setupToken }); }}>
        <Stack>
          <TextInput label="Setup token" description="The server prints it in its log when it starts with no accounts (or it's $MIMIR_SETUP_TOKEN)." required autoComplete="off" spellCheck={false} value={setupToken} onChange={(e) => setSetupToken(e.currentTarget.value)} data-autofocus />
          <TextInput label="Your name" required maxLength={100} value={name} onChange={(e) => setName(e.currentTarget.value)} />
          <TextInput label="Email" type="email" autoComplete="username" required value={email} onChange={(e) => setEmail(e.currentTarget.value)} />
          <PasswordInput label="Password" description="At least 10 characters." autoComplete="new-password" required minLength={10} value={password} onChange={(e) => setPassword(e.currentTarget.value)} />
          {setup.error && <Alert color="red" icon={<IconAlertCircle />}>{message(setup.error)}</Alert>}
          <Button type="submit" loading={setup.isPending} fullWidth>Create my account</Button>
        </Stack>
      </form>
    </AuthCard>
  );
}
