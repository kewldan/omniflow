import { requirePermissions } from "@/lib/server-session";

import { SupportDesk } from "./support-desk";

export default async function SupportPage() {
  await requirePermissions(["support.read"], "/admin/support");
  return <SupportDesk />;
}
