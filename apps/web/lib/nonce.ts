import "server-only";

import { headers } from "next/headers";

/**
 * The script nonce the proxy minted for this request, for the few inline
 * scripts this application writes itself (the theme bootstrap and the
 * counters). Next.js stamps its own scripts without being asked.
 */
export async function readNonce(): Promise<string | undefined> {
  return (await headers()).get("x-nonce") ?? undefined;
}
