import { requirePermissions } from "@/lib/server-session";

import { SystemDiagnostics } from "./system-diagnostics";

export default async function SystemPage() {
  await requirePermissions(["system.read"], "/admin/system");
  return <SystemDiagnostics />;
}
