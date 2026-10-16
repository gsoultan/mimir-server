import { Card, Stack, Text, Title } from "@mantine/core";
import type { ReactNode } from "react";

// The centred card used by the sign-in and setup screens.
export function AuthCard({ title, intro, children }: { title: string; intro?: string; children: ReactNode }) {
  return (
    <div className="flex min-h-dvh items-center justify-center p-4">
      <Stack className="w-full max-w-sm" gap="lg">
        <div className="flex items-center gap-2">
          <img src="/icon.svg" alt="" width={32} height={32} />
          <Text fw={700} ff="var(--font-display)" fz={24}>mimir</Text>
        </div>
        <Card withBorder radius="lg" padding="xl">
          <Title order={1} fz={26}>{title}</Title>
          {intro && <Text c="dimmed" mt={4}>{intro}</Text>}
          <div className="mt-6">{children}</div>
        </Card>
      </Stack>
    </div>
  );
}
