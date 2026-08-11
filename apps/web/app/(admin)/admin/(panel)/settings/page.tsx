import { requirePermissions } from "@/lib/server-session";

import { CommerceSettingsForm } from "./commerce-settings";

export default async function SettingsPage() {
  await requirePermissions(["settings.read"], "/admin/settings");
  return <CommerceSettingsForm />;
}
