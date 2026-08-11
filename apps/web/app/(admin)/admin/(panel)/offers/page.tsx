import { requirePermissions } from "@/lib/server-session";

import { OfferRegister } from "./offer-register";

export default async function OffersPage() {
  await requirePermissions(["marketing.read"], "/admin/offers");
  return <OfferRegister />;
}
