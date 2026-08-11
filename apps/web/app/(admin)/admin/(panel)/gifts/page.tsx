import { requirePermissions } from "@/lib/server-session";

import { GiftRegister } from "./gift-register";

export default async function GiftsPage() {
  await requirePermissions(["gifts.read"], "/admin/gifts");
  return <GiftRegister />;
}
