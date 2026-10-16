import { useMutation } from "@connectrpc/connect-query";
import { Alert, Button, PasswordInput, Stack, TextInput } from "@mantine/core";
import { IconAlertCircle } from "@tabler/icons-react";
import { createFileRoute, redirect, useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { z } from "zod";

import { message } from "../api/client";
import { AuthService } from "../api/gen/mimir/v1/access_pb";
import { setupState } from "../api/queries";
import { AuthCard } from "../components/AuthCard";

export const Route = createFileRoute("/sign-in")({
  validateSearch: z.object({ next: z.string().optional() }),
  beforeLoad: async ({ context }) => {
    const { needsSetup } = await context.queryClient.fetchQuery(setupState);
    if (needsSetup) throw redirect({ to: "/setup" });
  },
  component: SignIn,
});

function SignIn() {
  const { next } = Route.useSearch();
  const navigate = useNavigate();
  const { queryClient } = Route.useRouteContext();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const signIn = useMutation(AuthService.method.signIn, {
    onSuccess: async () => {
      queryClient.clear();
      // Only follow a path on this site, never an outside address.
      const to = next && next.startsWith("/") && !next.startsWith("//") ? next : "/";
      await navigate({ href: to });
    },
  });

  return (
    <AuthCard title="Sign in" intro="Welcome back.">
      <form onSubmit={(e) => { e.preventDefault(); signIn.mutate({ email, password }); }}>
        <Stack>
          <TextInput label="Email" type="email" autoComplete="username" required value={email} onChange={(e) => setEmail(e.currentTarget.value)} data-autofocus />
          <PasswordInput label="Password" autoComplete="current-password" required value={password} onChange={(e) => setPassword(e.currentTarget.value)} />
          {signIn.error && <Alert color="red" icon={<IconAlertCircle />}>{message(signIn.error)}</Alert>}
          <Button type="submit" loading={signIn.isPending} fullWidth>Sign in</Button>
        </Stack>
      </form>
    </AuthCard>
  );
}
