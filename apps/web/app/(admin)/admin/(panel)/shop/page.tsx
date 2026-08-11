import { requirePermissions } from "@/lib/server-session";

import { ShopBrowser } from "./shop-browser";

export default async function ShopPage() {
  await requirePermissions(["goods.read"], "/admin/shop");
  return <ShopBrowser />;
}
