import { createFileRoute } from "@tanstack/react-router";

import { ComingSoon } from "../components/ComingSoon";

export const Route = createFileRoute("/users")({
  component: () => (
    <ComingSoon
      title="Users"
      intro="Decide who can use and change which models."
      items={[
        "Invite people by email",
        "Give each person a role: Viewer, Editor, Admin or Owner",
        "Let someone reach all models, or only the ones you pick",
        "Make your own roles when the built-in ones don't fit",
      ]}
    />
  ),
});
