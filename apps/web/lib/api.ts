/**
 * The panel's API transport.
 *
 * The implementation lives in `@omniflow/api-client` beside the generated
 * client, so both surfaces of the workspace share one place that knows about
 * credentials, CSRF, and RFC 9457 problem responses.
 *
 * Why the panel calls this rather than the generated SWR hooks: Orval 8.24's
 * `client: "swr"` generator emits bare `fetch` calls and does not honour a
 * mutator, so a generated hook sends no session cookie and no CSRF token — every
 * mutation through one would be rejected, and every read would be anonymous.
 * The wrapper below targets exactly the same generated contract, so request and
 * response shapes stay checked against `api/openapi.yaml`; only the transport
 * differs. Switching to the generated hooks is a matter of the generator gaining
 * mutator support for this client, not of rewriting the panel.
 */
export {
  ApiError,
  apiRequest as apiFetch,
  fetcher,
  getCsrfToken,
  type ProblemDetail,
  setCsrfToken,
  toQuery,
} from "@omniflow/api-client";
