import { requirePermissions } from "@/lib/server-session";

import { CloseAllTickets } from "./close-all";
import { SupportDesk } from "./support-desk";

export default async function SupportPage() {
  await requirePermissions(["support.read"], "/admin/support");
  return (
    <div className="flex flex-col gap-3">
      <CloseAllTickets />
      <SupportDesk />
    </div>
  );
}
