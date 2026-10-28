/**
 * Shapes and rules shared by the operators screen.
 *
 * The role catalogue comes from `GET /v1/panel/rbac/catalog`, which reads the
 * compiled-in `internal/rbac` tables, so the matrix on screen is the one the
 * API enforces. `assignable` mirrors `rbac.Grant.CanAssign` only to grey out
 * choices the server would refuse; the server decides.
 */

export const ROLES = [
  "owner",
  "administrator",
  "support",
  "finance",
  "marketing",
  "auditor",
] as const;

export type Role = (typeof ROLES)[number];

export type Operator = {
  id: string;
  email: string;
  displayName: string;
  status: "active" | "suspended" | "disabled";
  roles: Role[];
  totpEnabled: boolean;
  lastLoginAt?: string;
};

export type Invitation = {
  id: string;
  email: string;
  roles: Role[];
  createdAt: string;
  expiresAt: string;
  invitedBy: string;
  invitedByName: string;
};

export type RbacCatalog = {
  permissions: string[];
  roles: { role: Role; permissions: string[] }[];
};

/**
 * Whether the signed-in operator could hand this role to somebody else: every
 * permission it carries must be one they hold, and owner is reserved to owners.
 */
export function assignable(
  role: Role,
  catalog: RbacCatalog | undefined,
  can: (permission: string) => boolean,
  actorRoles: string[],
): boolean {
  if (role === "owner" && !actorRoles.includes("owner")) {
    return false;
  }
  const entry = catalog?.roles.find((candidate) => candidate.role === role);
  return entry ? entry.permissions.every((permission) => can(permission)) : false;
}

/** Sorts a role set into catalogue order, so the same set always reads the same. */
export function orderedRoles(roles: readonly string[]): Role[] {
  return ROLES.filter((role) => roles.includes(role));
}

/** Builds the link the invitee opens, on the origin the inviter is using. */
export function invitationLink(token: string): string {
  const origin = typeof window === "undefined" ? "" : window.location.origin;
  return `${origin}/admin/invite/${encodeURIComponent(token)}`;
}
