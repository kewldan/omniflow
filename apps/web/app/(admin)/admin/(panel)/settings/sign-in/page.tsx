import { getTranslations } from "next-intl/server";

import { PageHeader } from "@/components/admin/resource-table";
import { requirePermissions } from "@/lib/server-session";

import { CustomerOidcSettings } from "../customer-oidc";

export default async function SignInSettingsPage() {
  await requirePermissions(["settings.read"], "/admin/settings/sign-in");
  const translate = await getTranslations("admin.settingsIndex.groups.signIn");
  // Every other settings page opens with its own heading; this one started at
  // a card, so it had no <h1> and nothing that matched the hub's label.
  return (
    <div className="flex flex-col gap-5">
      <PageHeader description={translate("description")} title={translate("title")} />
      <CustomerOidcSettings />
    </div>
  );
}
