import { z } from "zod";

// The page's Content-Security-Policy forbids eval, so tell zod not to try.
// This module is imported first, before any route defines a schema.
z.config({ jitless: true });
