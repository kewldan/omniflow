import { ArrowDownRight, ArrowUpRight, Minus } from "lucide-react";
import type { ReactNode } from "react";

import { Badge, type BadgeProps } from "./badge";

/**
 * Which way is good for a measure.
 *
 * `higher-is-better` for sales and sign-ups, `lower-is-better` for failures and
 * queues, `neutral` for a measure whose movement is information rather than a
 * verdict — more renewals due is neither good nor bad, it is next week's work.
 */
export type DeltaPolarity = "higher-is-better" | "lower-is-better" | "neutral";

export type DeltaBadgeProps = Omit<BadgeProps, "variant" | "children"> & {
  /** The signed change. Only its sign and zero-ness are read here. */
  delta: number;
  polarity?: DeltaPolarity;
  /** The change as the reader should see it, already formatted: "12%", "+3". */
  children: ReactNode;
  /**
   * What the change is measured against, for a screen reader: "compared with
   * the previous 30 days". The arrow says the direction to a sighted reader;
   * this says it with the reference point.
   */
  description?: string;
};

/**
 * A period-over-period change as a status chip.
 *
 * The colour is the verdict, not the direction: a rise in failed payments is
 * an up-arrow in the danger tone. The arrow always carries the direction and
 * the text always carries the size, so nothing here is read from colour alone.
 */
export function DeltaBadge({
  children,
  delta,
  description,
  polarity = "neutral",
  ...props
}: DeltaBadgeProps) {
  const Icon = delta > 0 ? ArrowUpRight : delta < 0 ? ArrowDownRight : Minus;
  return (
    <Badge
      className="tabular-nums"
      data-slot="delta-badge"
      variant={deltaTone(delta, polarity)}
      {...props}
    >
      <Icon aria-hidden />
      {children}
      {description ? <span className="sr-only">{description}</span> : null}
    </Badge>
  );
}

/** The badge tone for a change, exported so a tile can colour other marks to match. */
export function deltaTone(
  delta: number,
  polarity: DeltaPolarity,
): "success" | "danger" | "neutral" {
  if (delta === 0 || polarity === "neutral") {
    return "neutral";
  }
  return delta > 0 === (polarity === "higher-is-better") ? "success" : "danger";
}
