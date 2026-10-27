import { useMutation } from "@connectrpc/connect-query";
import { Alert, Anchor, Button, Divider, PasswordInput, PinInput, Stack, Text, TextInput } from "@mantine/core";
import { IconAlertCircle } from "@tabler/icons-react";
import { createFileRoute, redirect, useNavigate } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { z } from "zod";

import { message } from "../api/client";
import { AuthService } from "../api/gen/mimir/v1/access_pb";
import { setupState, signInOptions } from "../api/queries";
import { AuthCard } from "../components/AuthCard";
import { KindIcon } from "../components/KindIcon";

export const Route = createFileRoute("/sign-in")({
  // error and step come back from signing in with another account: why it
  // didn't work, or that the account's code is still needed.
  validateSearch: z.object({ next: z.string().optional(), error: z.string().optional(), step: z.enum(["code"]).optional() }),
  beforeLoad: async ({ context }) => {
    const { needsSetup } = await context.queryClient.fetchQuery(setupState);
    if (needsSetup) throw redirect({ to: "/setup" });
  },
  component: SignIn,
});

function SignIn() {
  const { next, error, step: arrived } = Route.useSearch();
  const navigate = useNavigate();
  const { queryClient } = Route.useRouteContext();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [step, setStep] = useState<"password" | "code">(arrived ?? "password");
  const { data: options } = useQuery(signInOptions);
  const done = async () => {
    queryClient.clear();
    // Only follow a path on this site, never an outside address.
    const to = next && next.startsWith("/") && !next.startsWith("//") ? next : "/";
    await navigate({ href: to });
  };
  const signIn = useMutation(AuthService.method.signIn, {
    onSuccess: (res) => (res.secondFactorRequired ? setStep("code") : done()),
  });

  if (step === "code") return <CodeStep onDone={done} onBack={() => { setStep("password"); void navigate({ to: "/sign-in", search: { next }, replace: true }); }} />;

  const others = options?.options ?? [];

  return (
    <AuthCard title="Sign in" intro="Welcome back.">
      <form onSubmit={(e) => { e.preventDefault(); signIn.mutate({ email, password }); }}>
        <Stack>
          <TextInput label="Email" type="email" autoComplete="username" required value={email} onChange={(e) => setEmail(e.currentTarget.value)} data-autofocus />
          <PasswordInput label="Password" autoComplete="current-password" required value={password} onChange={(e) => setPassword(e.currentTarget.value)} />
          {signIn.error ? <Alert color="red" icon={<IconAlertCircle />}>{message(signIn.error)}</Alert> : error && <Alert color="red" icon={<IconAlertCircle />}>{error}</Alert>}
          <Button type="submit" loading={signIn.isPending} fullWidth>Sign in</Button>
          {others.length > 0 && <>
            <Divider label="or" labelPosition="center" />
            {others.map((o) => (
              // A plain link: the browser leaves for the provider and comes back signed in.
              <Button key={o.id} component="a" variant="default" fullWidth leftSection={<KindIcon kind={o.kind} />}
                href={`/auth/sso/${encodeURIComponent(o.id)}/start${next ? `?next=${encodeURIComponent(next)}` : ""}`}>
                Continue with {o.name}
              </Button>
            ))}
          </>}
        </Stack>
      </form>
    </AuthCard>
  );
}

// The second step: six digits from the authenticator app, or one of the
// recovery codes written down when it was set up.
function CodeStep({ onDone, onBack }: { onDone: () => Promise<void>; onBack: () => void }) {
  const [code, setCode] = useState("");
  const [recovery, setRecovery] = useState(false);
  const verify = useMutation(AuthService.method.verifySecondFactor, { onSuccess: onDone });
  const submit = () => verify.mutate({ code });
  return (
    <AuthCard title="Enter your code" intro={recovery ? "Type one of the recovery codes you saved." : "Open your authenticator app and type the six digits it shows for mimir."}>
      <form onSubmit={(e) => { e.preventDefault(); submit(); }}>
        <Stack>
          {recovery ? (
            <TextInput label="Recovery code" placeholder="xxxx-xxxx-xxxx" autoComplete="one-time-code" required value={code} onChange={(e) => setCode(e.currentTarget.value)} data-autofocus />
          ) : (
            <PinInput length={6} type="number" oneTimeCode size="lg" autoFocus value={code} onChange={setCode} onComplete={(c) => verify.mutate({ code: c })} aria-label="Six-digit code" className="justify-center" />
          )}
          {verify.error && <Alert color="red" icon={<IconAlertCircle />}>{message(verify.error)}</Alert>}
          <Button type="submit" loading={verify.isPending} fullWidth>Continue</Button>
          <Text size="sm" c="dimmed" ta="center">
            {recovery ? (
              <Anchor component="button" type="button" size="sm" onClick={() => { setRecovery(false); setCode(""); }}>Use the app instead</Anchor>
            ) : (
              <Anchor component="button" type="button" size="sm" onClick={() => { setRecovery(true); setCode(""); }}>Lost your phone? Use a recovery code</Anchor>
            )}
            {" · "}
            <Anchor component="button" type="button" size="sm" onClick={onBack}>Start over</Anchor>
          </Text>
        </Stack>
      </form>
    </AuthCard>
  );
}
