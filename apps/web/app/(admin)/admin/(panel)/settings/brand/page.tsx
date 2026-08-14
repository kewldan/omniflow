import { requirePermissions } from "@/lib/server-session";

import { InstallationSettings } from "../installation-settings";
import { SettingsGroupHeader } from "../settings-group-header";

export default async function BrandSettingsPage() {
  await requirePermissions(["settings.read"], "/admin/settings/brand");
  return (
    <div className="flex flex-col gap-5">
      <SettingsGroupHeader group="brand" />
      <InstallationSettings group="brand" />
    </div>
  );
}
