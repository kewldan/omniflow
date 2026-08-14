import { requirePermissions } from "@/lib/server-session";

import { SettingsGroupHeader } from "../settings-group-header";
import { AnalyticsSettings } from "./analytics-settings";

export default async function AnalyticsSettingsPage() {
  await requirePermissions(["settings.read"], "/admin/settings/analytics");
  return (
    <div className="flex flex-col gap-5">
      <SettingsGroupHeader group="analytics" />
      <AnalyticsSettings />
    </div>
  );
}
