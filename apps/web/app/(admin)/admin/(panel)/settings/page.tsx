import { requirePermissions } from "@/lib/server-session";

import { CommerceSettingsForm } from "./commerce-settings";
import { InstallationSettings } from "./installation-settings";

export default async function SettingsPage() {
  await requirePermissions(["settings.read"], "/admin/settings");
  return (
    <div className="flex flex-col gap-5">
      <CommerceSettingsForm />
      <InstallationSettings />
    </div>
  );
}
