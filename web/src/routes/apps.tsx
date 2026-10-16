import { createFileRoute } from "@tanstack/react-router";

import { ComingSoon } from "../components/ComingSoon";

export const Route = createFileRoute("/apps")({
  component: () => (
    <ComingSoon
      title="Connect your apps"
      intro="Let your other apps ask your models questions."
      items={[
        "Create API keys, each limited to the models it needs",
        "Copy ready-made code for curl, Go, Python and JavaScript",
        "Add webhooks that tell your apps when a model changes or training finishes",
        "See every webhook delivery and send one again",
      ]}
    />
  ),
});
