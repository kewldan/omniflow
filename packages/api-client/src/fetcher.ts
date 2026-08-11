/**
 * The single transport for every call to the Omniflow API.
 *
 * It lives beside the generated client so both halves of the workspace share one
 * place that knows about credentials, CSRF, and RFC 9457 problem responses.
 *
 * It is not wired into the generated client. Orval 8.24's `client: "swr"`
 * generator emits bare `fetch` calls and ignores `override.mutator`, so a
 * generated hook sends neither the session cookie nor the CSRF token. Callers
 * therefore use `apiRequest` directly against the same generated contract:
 * request and response shapes stay checked against `api/openapi.yaml`, and only
 * the transport differs. When the generator gains mutator support for this
 * client, pointing it here is the whole change.
 */

/**
 * Requests are same-origin by default, which is what the generated client also
 * assumes: it builds relative paths like `/v1/panel/...`. A deployment that
 * serves the API from another origin calls `setBaseUrl` once at startup rather
 * than having an environment variable read from inside a shared package.
 */
let baseUrl = "";

export function setBaseUrl(url: string) {
  baseUrl = url.replace(/\/$/, "");
}

/** Held in a module cell so a mutation fired anywhere can reach it. */
let csrfToken = "";

export function setCsrfToken(token: string) {
  csrfToken = token;
}

export function getCsrfToken() {
  return csrfToken;
}

/** RFC 9457 problem response, narrowed to the fields the panel renders. */
export type ProblemDetail = {
  type: string;
  title: string;
  status: number;
  detail?: string;
  request_id?: string;
};

export class ApiError extends Error {
  readonly status: number;
  readonly problem: ProblemDetail | null;
  /** Stable machine-readable code, derived from the problem type URI. */
  readonly code: string;

  constructor(status: number, problem: ProblemDetail | null) {
    super(problem?.detail ?? problem?.title ?? `Request failed with ${status}`);
    this.name = "ApiError";
    this.status = status;
    this.problem = problem;
    this.code = problem?.type?.split("/").pop() ?? "unknown";
  }
}

const UNSAFE_METHODS = new Set(["POST", "PUT", "PATCH", "DELETE"]);

/**
 * Performs a request and returns the parsed body.
 *
 * Throws ApiError on a non-2xx response so callers and SWR see a rejected
 * promise rather than having to inspect a status by hand.
 */
export async function apiRequest<T>(path: string, init: RequestInit = {}): Promise<T> {
  const method = (init.method ?? "GET").toUpperCase();
  const headers = new Headers(init.headers);
  if (init.body && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }
  if (UNSAFE_METHODS.has(method) && csrfToken) {
    headers.set("X-CSRF-Token", csrfToken);
  }

  const url = path.startsWith("http") ? path : `${baseUrl}${path}`;
  const response = await fetch(url, {
    ...init,
    headers,
    // The session lives in an HttpOnly cookie, so it is never readable from JS.
    credentials: "include",
  });

  // The server rotates the token as sessions rotate; picking it up here keeps a
  // long-lived tab from failing its next mutation.
  const rotated = response.headers.get("X-CSRF-Token");
  if (rotated) {
    csrfToken = rotated;
  }

  if (response.status === 204) {
    return undefined as T;
  }

  const text = await response.text();
  const payload = text ? JSON.parse(text) : null;

  if (!response.ok) {
    throw new ApiError(response.status, payload as ProblemDetail | null);
  }
  return payload as T;
}

/** SWR fetcher for keys that are plain paths. */
export const fetcher = <T>(path: string) => apiRequest<T>(path);

/**
 * Builds a query string, dropping empty values so an unset filter never appears
 * in the URL or the request.
 */
export function toQuery(params: Record<string, string | number | undefined | null>): string {
  const query = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== null && value !== "") {
      query.set(key, String(value));
    }
  }
  const encoded = query.toString();
  return encoded ? `?${encoded}` : "";
}
