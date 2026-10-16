import { createFileRoute } from "@tanstack/react-router";

import { ComingSoon } from "../components/ComingSoon";

export const Route = createFileRoute("/settings")({
  component: () => (
    <ComingSoon
      title="Settings"
      intro="How this server stores data and keeps you informed."
      items={[
        "Choose and update the database (built-in SQLite now, PostgreSQL later)",
        "Get alerts by email or Telegram when something needs you",
        "Decide how long to keep logs",
        "Read the audit log: who changed what, and when",
      ]}
    />
  ),
});
