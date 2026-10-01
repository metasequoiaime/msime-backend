import { z } from "zod";

// The admin CSP forbids eval. Zod's JIT probes `new Function` when object schemas are built, which the browser reports as a CSP violation, so jitless mode must be set before any schema module is evaluated: main.tsx imports this file first.
z.config({ jitless: true });
