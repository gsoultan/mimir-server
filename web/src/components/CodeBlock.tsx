import { ActionIcon, Code, CopyButton, Tooltip } from "@mantine/core";
import { IconCheck, IconCopy } from "@tabler/icons-react";

// CodeBlock is a block of code with a copy button in its corner.
export function CodeBlock({ code }: { code: string }) {
  return (
    <div className="relative">
      <Code block className="pr-12">{code}</Code>
      <CopyButton value={code}>
        {({ copied, copy }) => (
          <Tooltip label={copied ? "Copied" : "Copy"} withArrow position="left">
            <ActionIcon variant="default" onClick={copy} className="!absolute right-2 top-2" aria-label="Copy">
              {copied ? <IconCheck size={16} /> : <IconCopy size={16} />}
            </ActionIcon>
          </Tooltip>
        )}
      </CopyButton>
    </div>
  );
}
