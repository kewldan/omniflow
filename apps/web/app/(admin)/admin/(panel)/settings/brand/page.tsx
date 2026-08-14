import { requirePermissions } from "@/lib/server-session";

import { InstallationSettings } from "../installation-settings";
import { SettingsGroupHeader } from "../settings-group-header";
import { ThemeEditor } from "../theme-editor";

export default async function BrandSettingsPage() {
  await requirePermissions(["settings.read"], "/admin/settings/brand");
  return (
    <div className="flex flex-col gap-5">
      <SettingsGroupHeader group="brand" />
      <InstallationSettings group="brand" />
      {/*
       * The palette sits below the name and the contacts because that is the
       * order an operator sets them in: what the service is called, then what
       * it looks like. It is a screen of its own rather than more fields on the
       * generic renderer, because a colour is not a text field and a logo is
       * not a setting document.
       */}
      <ThemeEditor />
    </div>
  );
}
