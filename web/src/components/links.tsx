import { Anchor, Button, type AnchorProps, type ButtonProps } from "@mantine/core";
import { createLink } from "@tanstack/react-router";
import type { ComponentPropsWithRef } from "react";

// Mantine components that navigate like a router Link, with typed
// params and search.
export const ButtonLink = createLink((props: ButtonProps & ComponentPropsWithRef<"a">) => <Button component="a" {...props} />);
export const AnchorLink = createLink((props: AnchorProps & ComponentPropsWithRef<"a">) => <Anchor {...props} />);
