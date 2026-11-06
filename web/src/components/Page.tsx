import { Anchor, Breadcrumbs, Stack, Text, Title } from "@mantine/core";
import { IconChevronRight } from "@tabler/icons-react";
import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";

export type Crumb = { label: string; to?: string };

// Page is the frame of every screen: where you are, the title, one line on
// what the page is for, and the page's actions on the right (full width
// under the title on a phone).
export function Page({ title, intro, actions, crumbs, badge, children }: { title: string; intro?: ReactNode; actions?: ReactNode; crumbs?: Crumb[]; badge?: ReactNode; children: ReactNode }) {
  return (
    <Stack gap="lg" className="mx-auto w-full max-w-[1080px]">
      <div>
        {crumbs && crumbs.length > 0 && (
          <Breadcrumbs separator={<IconChevronRight size={14} />} separatorMargin={6} mb="xs" visibleFrom="sm" fz="sm">
            {crumbs.map((c) => c.to
              ? <Anchor key={c.label} component={Link} to={c.to} c="dimmed" size="sm">{c.label}</Anchor>
              : <Text key={c.label} c="dimmed" size="sm">{c.label}</Text>)}
          </Breadcrumbs>
        )}
        <div className="flex flex-col gap-4 sm:flex-row sm:flex-wrap sm:items-end sm:justify-between">
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-3">
              <Title order={1} fz={{ base: 26, sm: 30 }}>{title}</Title>
              {badge}
            </div>
            {intro && <Text c="dimmed" mt={6}>{intro}</Text>}
          </div>
          {actions && <div className="page-actions">{actions}</div>}
        </div>
      </div>
      {children}
    </Stack>
  );
}
