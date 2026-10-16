import { Stack, Text, Title } from "@mantine/core";
import type { ReactNode } from "react";

export function Page({ title, intro, actions, children }: { title: string; intro?: ReactNode; actions?: ReactNode; children: ReactNode }) {
  return (
    <Stack gap="lg" className="mx-auto w-full max-w-5xl">
      <div className="flex flex-wrap items-end justify-between gap-4">
        <div>
          <Title order={1} fz={{ base: 28, sm: 34 }}>{title}</Title>
          {intro && <Text c="dimmed" mt={4}>{intro}</Text>}
        </div>
        {actions}
      </div>
      {children}
    </Stack>
  );
}
