import { requirePermissions } from "@/lib/server-session";

import { DiagnosticsCard } from "./diagnostics-card";
import { SystemDiagnostics } from "./system-diagnostics";

export default async function SystemPage() {
  await requirePermissions(["system.read"], "/admin/system");
  return (
    <div className="flex flex-col gap-5">
      <SystemDiagnostics />
      {/* The support bundle sits with the rest of "what is this installation
          doing", which is what its own permission has always said: the route is
          behind system.read, not settings.read, and it used to render on a page
          an operator holding only the former could not open. */}
      <DiagnosticsCard />
    </div>
  );
}
