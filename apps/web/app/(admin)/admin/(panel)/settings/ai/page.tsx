import { requirePermissions } from "@/lib/server-session";

import { AiSettings } from "./ai-settings";

export default async function AiSettingsPage() {
  await requirePermissions(["settings.read"], "/admin/settings/ai");
  return <AiSettings />;
}
