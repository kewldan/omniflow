import { requirePermissions } from "@/lib/server-session";

import { FinanceBrowser } from "./finance-browser";

export default async function FinancePage() {
  await requirePermissions(["finance.read"], "/admin/finance");
  return <FinanceBrowser />;
}
