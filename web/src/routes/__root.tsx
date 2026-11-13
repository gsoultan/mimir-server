import { Text } from "@mantine/core";
import type { QueryClient } from "@tanstack/react-query";
import { createRootRouteWithContext, Outlet } from "@tanstack/react-router";

import { UpdatePrompt } from "../components/UpdatePrompt";
import { t } from "../i18n";

export const Route = createRootRouteWithContext<{ queryClient: QueryClient }>()({
  component: () => (
    <>
      <Outlet />
      <UpdatePrompt />
    </>
  ),
  notFoundComponent: () => <Text p="xl">{t("That page doesn't exist. Use the menu to find your way back.")}</Text>,
});
