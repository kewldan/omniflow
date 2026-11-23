"use client";

import { PartnerProgramme } from "@/components/account/account/partner-programme";
import { ReferralSwitch } from "@/components/account/account/referral-switch";

/**
 * The partner programme, apart from inviting a friend.
 *
 * It was a section under the invite screen, reachable only by scrolling past
 * the code, the totals, and the rules of a different programme. A customer who
 * runs a channel comes for this and nothing else, so it has its own address and
 * the switch at the top of both places.
 */
export default function PartnerPage() {
  return (
    <div className="animate-step-in space-y-5">
      <ReferralSwitch />
      <PartnerProgramme />
    </div>
  );
}
