"use client";

import { useTranslations } from "next-intl";

/**
 * How hard the typed password would be to guess, and what would help.
 *
 * It estimates rather than checking boxes. A character-class checklist —
 * "one upper, one digit, one symbol" — rewards `Password1!`, which is among the
 * first thousand guesses any real attack makes, and penalises a long passphrase
 * that is genuinely hard. So the score is an entropy estimate over the alphabet
 * the password actually uses, reduced by the patterns that make an alphabet a
 * lie: repeats, sequences, and a single dictionary-shaped word.
 *
 * The result never blocks anything. The API's rule is the minimum length and it
 * is enforced there; this is advice, and advice that refuses is a rule wearing
 * the wrong clothes.
 */
export type PasswordAssessment = {
  /** 0–4: too guessable, weak, fair, strong, excellent. */
  score: 0 | 1 | 2 | 3 | 4;
  /** Estimated bits of entropy, after the penalties below. */
  bits: number;
  /** Message keys under `admin.security.password.advice`, most useful first. */
  advice: string[];
};

/** Alphabet sizes, added together for each class the password draws on. */
const ALPHABETS: { test: RegExp; size: number }[] = [
  { test: /[a-z]/, size: 26 },
  { test: /[A-Z]/, size: 26 },
  { test: /[0-9]/, size: 10 },
  // Everything else, counted once and conservatively: a password using two
  // symbols does not get credit for the whole of Unicode.
  { test: /[^a-zA-Z0-9]/, size: 33 },
];

/** Sequences an attacker tries early, in both directions. */
const SEQUENCES = ["abcdefghijklmnopqrstuvwxyz", "0123456789", "qwertyuiop", "asdfghjkl"];

export function assessPassword(password: string): PasswordAssessment {
  if (password.length === 0) {
    return { advice: [], bits: 0, score: 0 };
  }

  const alphabet = ALPHABETS.reduce(
    (total, entry) => (entry.test.test(password) ? total + entry.size : total),
    0,
  );
  let bits = password.length * Math.log2(Math.max(alphabet, 2));
  const advice: string[] = [];

  // A password made of a few distinct characters has far less entropy than its
  // length suggests: "aaaaaaaaaaaa" is twelve characters and one choice.
  const distinct = new Set(password).size;
  if (distinct < password.length / 2) {
    bits *= distinct / (password.length / 2);
    advice.push("repeats");
  }

  const lowered = password.toLowerCase();
  const runs = SEQUENCES.some((sequence) => {
    const reversed = [...sequence].reverse().join("");
    for (let start = 0; start + 4 <= lowered.length; start += 1) {
      const slice = lowered.slice(start, start + 4);
      if (sequence.includes(slice) || reversed.includes(slice)) {
        return true;
      }
    }
    return false;
  });
  if (runs) {
    bits -= 12;
    advice.push("sequence");
  }

  // One unbroken run of letters is a word, and a word is a dictionary entry
  // rather than a random string of its length.
  if (/^[a-zA-Z]+$/.test(password)) {
    bits -= 10;
    advice.push("oneWord");
  }

  if (password.length < 16) {
    advice.push("longer");
  }

  bits = Math.max(0, Math.round(bits));
  // The bands are the ones that matter operationally: below 40 bits is within
  // reach of an offline attack on a stolen hash, and past 80 the password stops
  // being the weakest thing about the account.
  const score = bits < 28 ? 0 : bits < 40 ? 1 : bits < 60 ? 2 : bits < 80 ? 3 : 4;
  return { advice: advice.slice(0, 2), bits, score: score as PasswordAssessment["score"] };
}

const BAR_COLOURS = [
  "bg-destructive",
  "bg-destructive",
  "bg-warning",
  "bg-success",
  "bg-success",
] as const;

/** The meter, its verdict, and the one or two things that would improve it. */
export function PasswordStrength({ password }: { password: string }) {
  const translate = useTranslations("admin.security.password");
  const { advice, bits, score } = assessPassword(password);

  if (password.length === 0) {
    return null;
  }

  return (
    <div className="flex flex-col gap-1.5">
      {/* Four segments rather than a continuous bar: a bar that creeps invites
          the reader to optimise the last pixel, and the bands are what the
          advice is written against. */}
      <div aria-hidden className="flex gap-1">
        {[0, 1, 2, 3].map((segment) => (
          <span
            className={`h-1 flex-1 rounded-full ${
              segment < Math.max(score, 1) ? BAR_COLOURS[score] : "bg-border"
            }`}
            key={segment}
          />
        ))}
      </div>
      {/* The verdict is text as well as colour: the meter must read for
          somebody who cannot tell the red bar from the green one. */}
      <p className="text-muted-foreground text-xs">
        {translate(`strength.${score}`)} · {translate("strength.bits", { bits })}
      </p>
      {advice.length > 0 ? (
        <ul className="flex flex-col gap-0.5 text-muted-foreground text-xs">
          {advice.map((key) => (
            <li key={key}>{translate(`advice.${key}`)}</li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}
