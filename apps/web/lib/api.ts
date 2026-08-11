/**
 * Browser-side fetch wrapper for the operator panel API.
 *
 * Every request carries the session cookie, and every unsafe method carries the
 * CSRF token from the current session. The token is held in a module-level cell
 * rather than a React state so that a mutation fired from anywhere in the tree
 * can reach it without prop-drilling a provider through every form.
 */

const PANEL_BASE = process.env.NEXT_PUBLIC_API_URL ?? "";

let csrfToken = "";

/** Records the token returned by the session endpoint or a sign-in response. */
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

export async function apiFetch<T>(path: string, init: RequestInit = {}): Promise<T> {
  const method = (init.method ?? "GET").toUpperCase();
  const headers = new Headers(init.headers);
  if (init.body && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }
  if (UNSAFE_METHODS.has(method) && csrfToken) {
    headers.set("X-CSRF-Token", csrfToken);
  }

  const response = await fetch(`${PANEL_BASE}${path}`, {
    ...init,
    headers,
    // The session lives in an HttpOnly cookie, so it is never readable from JS.
    credentials: "include",
  });

  // The server rotates the token as sessions rotate; picking it up from the
  // response header keeps a long-lived tab from failing its next mutation.
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

/** SWR fetcher. */
export const fetcher = <T>(path: string) => apiFetch<T>(path);

/**
 * Builds a query string from panel filter state, dropping empty values so an
 * unset filter never appears in the URL or the request.
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
