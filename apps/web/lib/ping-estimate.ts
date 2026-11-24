/**
 * How the browser's timing of a server becomes an estimate of the round trip
 * through the tunnel. The server sends the model (see internal/smartsub
 * latency.go): the browser only reaches the entry, and the rest — the hop to
 * the exit node and from it to common sites — comes from Remnawave's checks
 * of the nodes.
 */
export type PingModel = { transport: "tcp" | "udp"; tailMs: number };

/**
 * What Reality adds to a refused TLS handshake by passing it on, measured at
 * 3–12 ms from Moscow and Germany.
 */
const HANDSHAKE_MS = 5;

/**
 * The estimate for a measured server, or the measurement itself when the
 * server sent no model. A failed image load over TCP takes two round trips
 * (the connection and the TLS server's answer); a UDP-only server refuses the
 * TCP connection after one.
 */
export function estimatePing(
  measured: number | null | undefined,
  model: PingModel | undefined,
): { value: number | null | undefined; estimated: boolean } {
  if (measured === null || measured === undefined || !model) {
    return { estimated: false, value: measured };
  }
  const entry = model.transport === "udp" ? measured : Math.max(measured - HANDSHAKE_MS, 0) / 2;
  return { estimated: true, value: Math.round(entry + model.tailMs) };
}
