import { requirePermissions } from "@/lib/server-session";

import { SmartSubscriptionSettings } from "./smart-subscription-settings";

export default async function SmartSubscriptionSettingsPage() {
  await requirePermissions(["settings.read"], "/admin/settings/smart-subscription");
  return <SmartSubscriptionSettings />;
}
