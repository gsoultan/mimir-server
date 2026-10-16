import { Badge, Card, List, Text, ThemeIcon } from "@mantine/core";
import { IconCheck } from "@tabler/icons-react";

import { Page } from "./Page";

// A clear placeholder for screens that arrive in a later phase, so nobody
// wonders whether a button is broken.
export function ComingSoon({ title, intro, items }: { title: string; intro: string; items: string[] }) {
  return (
    <Page title={title} intro={intro}>
      <Card withBorder padding="lg" radius="lg">
        <Badge variant="light" mb="sm">Coming soon</Badge>
        <Text fw={600} mb="xs">What you'll be able to do here</Text>
        <List spacing="xs" icon={<ThemeIcon size={20} radius="xl" variant="light"><IconCheck size={14} /></ThemeIcon>}>
          {items.map((i) => <List.Item key={i}>{i}</List.Item>)}
        </List>
      </Card>
    </Page>
  );
}
