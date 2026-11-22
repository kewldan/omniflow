import { requirePermissions } from "@/lib/server-session";

import { MarketingWorkspace } from "./marketing-workspace";

export default async function MarketingPage() {
  await requirePermissions(["marketing.read"], "/admin/marketing");
  return <MarketingWorkspace />;
}
