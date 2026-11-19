import { requirePermissions } from "@/lib/server-session";

import { PayoutsWorkspace } from "./payouts-workspace";

export default async function PayoutsPage() {
  await requirePermissions(["finance.read"], "/admin/payouts");
  return <PayoutsWorkspace />;
}
