import { requirePermissions } from "@/lib/server-session";

import { BackupControls } from "../backup-controls";
import { InstallationSettings } from "../installation-settings";
import { SettingsGroupHeader } from "../settings-group-header";

export default async function OperationsSettingsPage() {
  await requirePermissions(["settings.read"], "/admin/settings/operations");
  return (
    <div className="flex flex-col gap-5">
      <SettingsGroupHeader group="operations" />
      <InstallationSettings group="operations" />
      <BackupControls />
    </div>
  );
}
