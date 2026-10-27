import { IconBrandGithub, IconBrandGoogle, IconBuilding } from "@tabler/icons-react";

import { SignInKind } from "../api/gen/mimir/v1/signin_pb";

// KindIcon marks a way to sign in: Google, GitHub, or a company's own.
export function KindIcon({ kind, size = 18 }: { kind: SignInKind; size?: number }) {
  if (kind === SignInKind.GOOGLE) return <IconBrandGoogle size={size} />;
  if (kind === SignInKind.GITHUB) return <IconBrandGithub size={size} />;
  return <IconBuilding size={size} />;
}
