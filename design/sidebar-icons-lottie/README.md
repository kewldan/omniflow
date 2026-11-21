# Sidebar icons — Lottie

20 Lucide sidebar icons (24×24, stroke 2), animated in one shared style.

## Spec
- 60 fps, 48 frames (0.8 s), canvas 24×24.
- **Frame 0 = last frame = the static icon.** Idle on frame 0 and play once on hover/activation: `anim.goToAndPlay(0, true)`.
- Stroke width stays 2 px even while the shape scales (the pop moves compensate).
- Strokes are black (#000). To use the text color, with the SVG renderer:
  `.sidebar-icon svg path { stroke: currentColor; }`

## Shared moves (same timings and easings in every icon)
| Move | Timing | Easing |
|---|---|---|
| pop (press) | in 8f → back 22f | in-out (0.65,0,0.35,1) → spring (0.34,1.56,0.64,1) |
| wipe (redraw) | out 7f → in 16f | ease-in (0.4,0,1,1) → ease-out (0.16,1,0.3,1) |
| bob (lift) | up 8f → settle 18f | ease-out → spring |
| shake | 5 damped beats of 6f | in-out |
| spin | wind-up 6f → turn 26f | in-out → spring |
| Stagger between parts | 4 frames | |

## Per icon
| File | Motion |
|---|---|
| 01-dashboard | tiles press in clockwise |
| 02-customers | front head bobs, second person redraws |
| 03-finance | card wobbles, stripe and number redraw |
| 04-payouts | wallet presses, flap with clasp lifts |
| 05-reports | bars drop and grow back left to right |
| 06-traffic | the pulse line runs through |
| 07-catalog | box hops, lands with a squash, tape redraws |
| 08-shop | bag presses, handle lifts |
| 09-gifts | lid pops up with a tilt, ribbon follows |
| 10-offers, 13-marketing | megaphone shakes from the grip |
| 11-support | buoy turns a quarter |
| 12-content | page presses, text lines redraw |
| 14-risk | shield shakes, ! redraws, dot pops |
| 15-operators | head bobs, cog turns |
| 16-audit | clip lifts, list items tick in |
| 17-system | racks press in turn, lights blink |
| 18-settings | knobs slide and spring back, tracks follow |
| 19-aiSettings | big spark presses and turns, small ones twinkle |
| 20-security | shield presses, check redraws |

## Usage (lottie-web)
```js
import lottie from 'lottie-web/build/player/lottie_light';
const anim = lottie.loadAnimation({ container: el, renderer: 'svg', loop: false, autoplay: false, animationData });
row.addEventListener('mouseenter', () => anim.goToAndPlay(0, true));
```
With `lottie-react` / `@lottiefiles/dotlottie-react`, keep autoplay off and play on hover the same way.

## Rebuilding
`source/lottie_gen.py` builds every file from the Lucide paths; timings and per-icon choreography sit at the top and in `icons()`.
`pip install svgelements && python lottie_gen.py ./lottie`
