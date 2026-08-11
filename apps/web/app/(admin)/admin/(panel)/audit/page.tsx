import { requirePermissions } from "@/lib/server-session";

import { AuditBrowser } from "./audit-browser";

/**
 * The audit route.
 *
 * Permission is checked here, on the server, before any markup is produced. The
 * Go API enforces the same permission on the request the browser then makes, so
 * this is defence in depth rather than the control itself.
 */
export default async function AuditPage() {
  await requirePermissions(["audit.read"], "/admin/audit");
  return <AuditBrowser />;
}
