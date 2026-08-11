import { requirePermissions } from "@/lib/server-session";

import { CatalogBrowser } from "./catalog-browser";

export default async function CatalogPage() {
  await requirePermissions(["catalog.read"], "/admin/catalog");
  return <CatalogBrowser />;
}
