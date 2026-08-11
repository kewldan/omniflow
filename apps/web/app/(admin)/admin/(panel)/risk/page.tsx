import { requirePermissions } from "@/lib/server-session";

import { RiskReview } from "./risk-review";

export default async function RiskPage() {
  await requirePermissions(["risk.read"], "/admin/risk");
  return <RiskReview />;
}
