import { requirePermissions } from "@/lib/server-session";

import { ButtonStats } from "./button-stats";
import { MarketingWorkspace } from "./marketing-workspace";

export default async function MarketingPage() {
  await requirePermissions(["marketing.read"], "/admin/marketing");
  return (
    <div className="flex flex-col gap-5">
      <MarketingWorkspace />
      <ButtonStats />
    </div>
  );
}
