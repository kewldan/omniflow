/**
 * The headers that carry an operator's reason.
 *
 * A browser refuses a header holding any character outside Latin-1: a reason
 * in Russian made fetch throw before anything was sent, which the panel showed
 * as "Request failed with 0". The reason travels percent-encoded, and the
 * encoding header tells the API to decode it.
 */
export function operatorReasonHeaders(reason: string): Record<string, string> {
  return {
    "X-Operator-Reason": encodeURIComponent(reason),
    "X-Operator-Reason-Encoding": "percent",
  };
}
