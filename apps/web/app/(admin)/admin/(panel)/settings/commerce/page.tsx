import { requirePermissions } from "@/lib/server-session";

import { CommerceSettingsForm } from "../commerce-settings";

export default async function CommerceSettingsPage() {
  await requirePermissions(["settings.read"], "/admin/settings/commerce");
  return <CommerceSettingsForm />;
}
