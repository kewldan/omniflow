import { requirePermissions } from "@/lib/server-session";

import { CustomerOidcSettings } from "../customer-oidc";

export default async function SignInSettingsPage() {
  await requirePermissions(["settings.read"], "/admin/settings/sign-in");
  return <CustomerOidcSettings />;
}
