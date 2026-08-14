import { requirePermissions } from "@/lib/server-session";

import { SettingsIndex } from "./settings-index";

export default async function SettingsPage() {
  await requirePermissions(["settings.read"], "/admin/settings");
  return <SettingsIndex />;
}
