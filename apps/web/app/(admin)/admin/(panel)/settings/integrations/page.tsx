import { requirePermissions } from "@/lib/server-session";

import { BotCustomization } from "../bot-customization";
import { InstallationSettings } from "../installation-settings";
import { SettingsGroupHeader } from "../settings-group-header";

export default async function IntegrationsSettingsPage() {
  await requirePermissions(["settings.read"], "/admin/settings/integrations");
  return (
    <div className="flex flex-col gap-5">
      <SettingsGroupHeader group="integrations" />
      <InstallationSettings group="integrations" />
      <BotCustomization />
    </div>
  );
}
