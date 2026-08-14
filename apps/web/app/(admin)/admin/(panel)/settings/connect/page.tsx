import { requirePermissions } from "@/lib/server-session";

import { ConnectCatalogueScreen } from "./connect-catalogue";

export default async function ConnectSettingsPage() {
  await requirePermissions(["settings.read"], "/admin/settings/connect");
  return <ConnectCatalogueScreen />;
}
