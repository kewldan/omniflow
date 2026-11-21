"""Generate Lottie animations for the sidebar icons (Lucide, 24x24, stroke 2).

One motion language for all icons:
  * 60 fps, 48 frames (0.8 s), frame 0 == last frame == the static icon,
    so the player can rest on frame 0 and play once on hover / activation.
  * Five shared moves, same timings and easings everywhere:
      pop    – press in (8f, in-out) then spring back with overshoot (22f)
      wipe   – stroke runs out (7f, ease-in) and redraws (16f, ease-out)
      bob    – lift (8f) and settle with overshoot (18f)
      shake  – damped wiggle, 5 beats of 6f
      spin   – small anticipation (6f) then turn with overshoot (26f)
  * Stagger between parts: 4 frames.
"""
import io, json, math, sys, os
from svgelements import SVG, Path, Shape, Move, Line, Close, CubicBezier, QuadraticBezier

FPS, OP, SIZE = 60, 48, 24
STAGGER = 4

LINEAR = (0.0, 0.0, 1.0, 1.0)
E_IN = (0.4, 0.0, 1.0, 1.0)
E_OUT = (0.16, 1.0, 0.3, 1.0)
E_INOUT = (0.65, 0.0, 0.35, 1.0)
E_BACK = (0.34, 1.56, 0.64, 1.0)
HOLD = "hold"


# ---------------------------------------------------------------- geometry
def parse(snippet):
    """SVG element markup -> list of Lottie bezier shapes (one per subpath)."""
    doc = SVG.parse(io.StringIO(
        '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24">%s</svg>' % snippet),
        reify=True)
    out = []
    for el in doc.elements():
        if not isinstance(el, Shape) or isinstance(el, SVG):
            continue
        p = Path(el)
        p.approximate_arcs_with_cubics()
        out.extend(path_to_shapes(p))
    return out


def path_to_shapes(p):
    shapes, cur = [], None

    def flush():
        if cur and cur["v"]:
            shapes.append(cur)

    for seg in p:
        if isinstance(seg, Move):
            flush()
            cur = {"v": [[seg.end.x, seg.end.y]], "i": [[0, 0]], "o": [[0, 0]], "c": False}
        elif isinstance(seg, (Line, Close)):
            if isinstance(seg, Close):
                if seg.start is not None and seg.end is not None and \
                        (abs(seg.start.x - seg.end.x) > 1e-6 or abs(seg.start.y - seg.end.y) > 1e-6):
                    cur["v"].append([seg.end.x, seg.end.y]); cur["i"].append([0, 0]); cur["o"].append([0, 0])
                cur["c"] = True
                continue
            cur["v"].append([seg.end.x, seg.end.y]); cur["i"].append([0, 0]); cur["o"].append([0, 0])
        elif isinstance(seg, (CubicBezier, QuadraticBezier)):
            if isinstance(seg, QuadraticBezier):
                s, c, e = seg.start, seg.control, seg.end
                c1 = (s.x + 2 / 3 * (c.x - s.x), s.y + 2 / 3 * (c.y - s.y))
                c2 = (e.x + 2 / 3 * (c.x - e.x), e.y + 2 / 3 * (c.y - e.y))
            else:
                c1 = (seg.control1.x, seg.control1.y); c2 = (seg.control2.x, seg.control2.y)
            v0 = cur["v"][-1]
            cur["o"][-1] = [c1[0] - v0[0], c1[1] - v0[1]]
            cur["v"].append([seg.end.x, seg.end.y])
            cur["i"].append([c2[0] - seg.end.x, c2[1] - seg.end.y]); cur["o"].append([0, 0])
    flush()
    # closed paths: drop a duplicated end vertex, carry its in-tangent to vertex 0
    for s in shapes:
        if s["c"] and len(s["v"]) > 1:
            a, b = s["v"][0], s["v"][-1]
            if abs(a[0] - b[0]) < 1e-4 and abs(a[1] - b[1]) < 1e-4:
                s["i"][0] = s["i"][-1]
                s["v"].pop(); s["i"].pop(); s["o"].pop()
    return [rnd(s) for s in shapes]


def rnd(s):
    r = lambda pts: [[round(x, 4), round(y, 4)] for x, y in pts]
    return {"i": r(s["i"]), "o": r(s["o"]), "v": r(s["v"]), "c": s["c"]}


# ---------------------------------------------------------------- tracks
# A track is a list of keyframes (t, value, ease_to_next). Values are lists.
def seq(base, t0, steps):
    """Start at `base`, wait until t0, then run steps [(dur, value, ease)]."""
    kfs = [[0, base, HOLD]]
    t = t0
    if t0 > 0:
        kfs.append([t0, base, None])
    for dur, val, ease in steps:
        kfs[-1][2] = ease
        t += dur
        kfs.append([t, val, None])
    assert t <= OP, (t, steps)
    return kfs


def prop(track, dims=None):
    """Track or static value -> Lottie animatable property."""
    if not isinstance(track, list) or not track or not isinstance(track[0], list) or \
            not (len(track[0]) == 3 and isinstance(track[0][0], (int, float))):
        return {"a": 0, "k": track}
    kfs = []
    for t, v, ease in track:
        val = v if isinstance(v, list) else [v]
        k = {"t": t, "s": val}
        if ease == HOLD:
            k["h"] = 1
        elif ease is not None:
            n = len(val) if dims is None else dims
            k["o"] = {"x": [ease[0]] * n, "y": [ease[1]] * n}
            k["i"] = {"x": [ease[2]] * n, "y": [ease[3]] * n}
        kfs.append(k)
    # dedupe leading equal-time keys
    return {"a": 1, "k": kfs}


def map_track(track, fn):
    return [[t, fn(v), e] for t, v, e in track]


# ---------------------------------------------------------------- motions
def pop(t0=0, depth=86):
    return seq([100, 100, 100], t0, [(8, [depth, depth, 100], E_INOUT), (22, [100, 100, 100], E_BACK)])


def squash(t0=0):  # landing squash for a drop
    return seq([100, 100, 100], t0, [(6, [108, 90, 100], E_OUT), (18, [100, 100, 100], E_BACK)])


def dot(t0=0):
    return seq([100, 100, 100], t0, [(7, [0, 0, 100], E_IN), (18, [100, 100, 100], E_BACK)])


def bob(t0=0, dy=-2.5, dx=0.0):
    return seq([0, 0], t0, [(8, [dx, dy], E_OUT), (18, [0, 0], E_BACK)])


def shake(t0=0, amp=10.0):
    a = amp
    return seq([0], t0, [(5, [-a], E_INOUT), (6, [a * 0.8], E_INOUT), (6, [-a * 0.5], E_INOUT),
                         (6, [a * 0.25], E_INOUT), (6, [0], E_INOUT)])


def spin(t0=0, deg=90.0):
    return seq([0], t0, [(6, [-deg * 0.12], E_INOUT), (26, [deg], E_BACK)])


def wipe(t0=0):
    """Stroke leaves along its direction, then redraws from its start."""
    s = [[0, [0], HOLD], [t0, [0], E_IN], [t0 + 7, [100], HOLD], [t0 + 8, [0], None]]
    if t0 == 0:
        s = s[1:]
    e = [[0, [100], HOLD], [t0 + 8, [0], E_OUT], [t0 + 24, [100], None]]
    assert t0 + 24 <= OP
    return {"s": s, "e": e}


def scalar(base, t0, steps):
    return seq([base], t0, [(d, [v], e) for d, v, e in steps])


# ---------------------------------------------------------------- layers
class Part:
    def __init__(self, name, svg=None, shapes=None, anchor=(12, 12), pos=None, scale=None,
                 rot=None, opacity=None, trim=None, shape_track=None):
        self.name, self.anchor = name, anchor
        self.shapes = shapes if shapes is not None else parse(svg)
        self.pos, self.scale, self.rot, self.opacity, self.trim = pos, scale, rot, opacity, trim
        self.shape_track = shape_track  # list of (shape_index, track_of_shapes)

    parent_scale = None

    def stroke_width(self):
        """Keep the 2px stroke constant while a uniform pop scales the shape."""
        sc = self.scale or self.parent_scale
        if sc and all(abs(v[0] - v[1]) < 1e-9 for _, v, _ in sc) and min(v[0] for _, v, _ in sc) >= 40:
            return prop(map_track(sc, lambda v: [round(200.0 / v[0], 4)]), 1)
        return {"a": 0, "k": 2}

    def layer(self, ind):
        ax, ay = self.anchor
        p = map_track(self.pos, lambda v: [ax + v[0], ay + v[1], 0]) if self.pos else [ax, ay, 0]
        ks = {
            "o": prop(self.opacity if self.opacity else 100),
            "r": prop(self.rot if self.rot else 0),
            "p": prop(p, 3),
            "a": prop([ax, ay, 0]),
            "s": prop(self.scale if self.scale else [100, 100, 100], 3),
        }
        items = []
        animated = dict(self.shape_track or [])
        for idx, sh in enumerate(self.shapes):
            ksh = prop(map_track(animated[idx], lambda v: [v]), 1) if idx in animated else {"a": 0, "k": sh}
            if idx in animated:  # shape keyframes hold one shape per value
                for k in ksh["k"]:
                    k["s"] = k["s"]
            items.append({"ty": "sh", "nm": "p%d" % idx, "ks": ksh})
        if self.trim:
            items.append({"ty": "tm", "nm": "trim", "s": prop(self.trim["s"]), "e": prop(self.trim["e"]),
                          "o": {"a": 0, "k": 0}, "m": 1})
        items.append({"ty": "st", "nm": "stroke", "c": {"a": 0, "k": [0, 0, 0, 1]}, "o": {"a": 0, "k": 100},
                      "w": self.stroke_width(), "lc": 2, "lj": 2, "ml": 4})
        items.append({"ty": "tr", "p": {"a": 0, "k": [0, 0]}, "a": {"a": 0, "k": [0, 0]},
                      "s": {"a": 0, "k": [100, 100]}, "r": {"a": 0, "k": 0}, "o": {"a": 0, "k": 100},
                      "sk": {"a": 0, "k": 0}, "sa": {"a": 0, "k": 0}})
        return {"ddd": 0, "ind": ind, "ty": 4, "nm": self.name, "sr": 1, "ks": ks, "ao": 0,
                "shapes": [{"ty": "gr", "nm": self.name, "it": items}],
                "ip": 0, "op": OP, "st": 0, "bm": 0}


def comp(name, parts, parent=None):
    """parent: optional dict of whole-icon transforms (pos/scale/rot, anchor)."""
    layers = []
    ind = 1
    pind = None
    if parent:
        pind = ind
        ax, ay = parent.get("anchor", (12, 12))
        pos = parent.get("pos")
        p = map_track(pos, lambda v: [ax + v[0], ay + v[1], 0]) if pos else [ax, ay, 0]
        layers.append({"ddd": 0, "ind": ind, "ty": 3, "nm": "icon", "sr": 1, "ao": 0,
                       "ks": {"o": {"a": 0, "k": 0}, "r": prop(parent.get("rot", 0)), "p": prop(p, 3),
                              "a": prop([ax, ay, 0]), "s": prop(parent.get("scale", [100, 100, 100]), 3)},
                       "ip": 0, "op": OP, "st": 0, "bm": 0})
        ind += 1
    for part in parts:
        if parent and parent.get("scale"):
            part.parent_scale = parent["scale"]
        L = part.layer(ind)
        if pind:
            L["parent"] = pind
        layers.append(L)
        ind += 1
    return {"v": "5.12.0", "fr": FPS, "ip": 0, "op": OP, "w": SIZE, "h": SIZE, "nm": name,
            "ddd": 0, "assets": [], "layers": layers, "markers": []}


# ---------------------------------------------------------------- helpers
def line_shape(x1, y1, x2, y2):
    return {"i": [[0, 0], [0, 0]], "o": [[0, 0], [0, 0]], "v": [[x1, y1], [x2, y2]], "c": False}


SHIELD = '<path d="M20 13c0 5-3.5 7.5-7.66 8.95a1 1 0 0 1-.67-.01C7.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.24-2.72a1.17 1.17 0 0 1 1.52 0C14.51 3.81 17 5 19 5a1 1 0 0 1 1 1z"/>'
MEGA = ('<path d="M11 6a13 13 0 0 0 8.4-2.8A1 1 0 0 1 21 4v12a1 1 0 0 1-1.6.8A13 13 0 0 0 11 14H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2z"/>'
        '<path d="M6 14a12 12 0 0 0 2.4 7.2 2 2 0 0 0 3.2-2.4A8 8 0 0 1 10 14"/><path d="M8 6v8"/>')


def head_and_body(t0=0):
    return [Part("body", '<path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2"/>'),
            Part("head", '<circle cx="9" cy="7" r="4"/>', anchor=(9, 7), pos=bob(t0, -2))]


# ---------------------------------------------------------------- icons
def icons():
    I = {}

    # 1 Dashboard — tiles press in one after another, clockwise
    tiles = [('<rect width="7" height="9" x="3" y="3" rx="1"/>', (6.5, 7.5)),
             ('<rect width="7" height="5" x="14" y="3" rx="1"/>', (17.5, 5.5)),
             ('<rect width="7" height="9" x="14" y="12" rx="1"/>', (17.5, 16.5)),
             ('<rect width="7" height="5" x="3" y="16" rx="1"/>', (6.5, 18.5))]
    I["01-dashboard"] = comp("dashboard", [Part("tile%d" % n, s, anchor=a, scale=pop(n * STAGGER, 72))
                                           for n, (s, a) in enumerate(tiles)])

    # 2 Customers — front person nods, second person redraws
    I["02-customers"] = comp("customers", head_and_body(0) + [
        Part("second", '<path d="M16 3.128a4 4 0 0 1 0 7.744"/><path d="M22 21v-2a4 4 0 0 0-3-3.87"/>',
             trim=wipe(STAGGER * 2))])

    # 3 Finance — card tilts, stripe and number redraw
    I["03-finance"] = comp("finance", [
        Part("card", '<rect width="20" height="14" x="2" y="5" rx="2"/>'),
        Part("stripe", '<line x1="2" x2="22" y1="10" y2="10"/>', trim=wipe(STAGGER)),
        Part("number", '<path d="M6 14h2"/>', trim=wipe(STAGGER * 2)),
    ], parent={"anchor": (12, 19), "rot": shake(0, 6)})

    # 4 Payouts — wallet pops, flap with clasp lifts open
    I["04-payouts"] = comp("payouts", [
        Part("flap", '<path d="M19 7V4a1 1 0 0 0-1-1H5a2 2 0 0 0 0 4h15a1 1 0 0 1 1 1v4h-3a2 2 0 0 0 0 4h3a1 1 0 0 0 1-1v-2a1 1 0 0 0-1-1"/>',
             anchor=(12, 9), pos=bob(STAGGER, -1.5, 0.5)),
        Part("body", '<path d="M3 5v14a2 2 0 0 0 2 2h15a1 1 0 0 0 1-1v-4"/>'),
    ], parent={"anchor": (12, 21), "scale": pop(0)})

    # 5 Sales — bars drop and grow back, left to right
    bars = []
    for n, (x, top) in enumerate([(8, 14), (13, 5), (18, 9)]):
        tr = scalar(top, n * STAGGER, [(8, 17, E_INOUT), (22, top, E_BACK)])
        bars.append(Part("bar%d" % n, shapes=[line_shape(x, 17, x, top)],
                         shape_track=[(0, map_track(tr, lambda v, x=x: line_shape(x, 17, x, v[0])))]))
    I["05-reports"] = comp("reports", [Part("axis", '<path d="M3 3v16a2 2 0 0 0 2 2h16"/>')] + bars)

    # 6 Traffic — the pulse runs through
    I["06-traffic"] = comp("traffic", [Part("pulse",
        '<path d="M2 12h2.49a2 2 0 0 0 1.92-1.46l2.35-8.36a.25.25 0 0 1 .48 0l5.52 19.64a.25.25 0 0 0 .48 0l2.35-8.36A2 2 0 0 1 19.52 12H22"/>',
        trim=wipe(0))])

    # 7 Catalog — box hops and lands, tape redraws
    I["07-catalog"] = comp("catalog", [
        Part("box", '<path d="M11 21.73a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73z"/>'
                    '<path d="M12 22V12"/><polyline points="3.29 7 12 12 20.71 7"/>'),
        Part("tape", '<path d="m7.5 4.27 9 5.15"/>', trim=wipe(STAGGER * 2)),
    ], parent={"anchor": (12, 22), "pos": bob(0, -3), "scale": squash(14)})

    # 8 Shop — bag pops, handle lifts
    I["08-shop"] = comp("shop", [
        Part("bag", '<path d="M3.103 6.034h17.794"/><path d="M3.4 5.467a2 2 0 0 0-.4 1.2V20a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2V6.667a2 2 0 0 0-.4-1.2l-2-2.667A2 2 0 0 0 17 2H7a2 2 0 0 0-1.6.8z"/>'),
        Part("handle", '<path d="M16 10a4 4 0 0 1-8 0"/>', anchor=(12, 12), pos=bob(STAGGER, -1.5)),
    ], parent={"anchor": (12, 22), "scale": pop(0)})

    # 9 Gifts — lid pops up with a tilt, ribbon follows
    lid_dy = scalar(0, 0, [(8, -3, E_OUT), (18, 0, E_BACK)])
    ribbon = Part("ribbon", shapes=[line_shape(12, 7, 12, 21)],
                  shape_track=[(0, map_track(lid_dy, lambda v: line_shape(12, 7 + v[0], 12, 21)))])
    I["09-gifts"] = comp("gifts", [
        Part("box", '<path d="M20 11v8a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2v-8"/>'),
        ribbon,
        Part("lid", '<rect x="3" y="7" width="18" height="4" rx="1"/><path d="M7.5 7a1 1 0 0 1 0-5A4.8 8 0 0 1 12 7a4.8 8 0 0 1 4.5-5 1 1 0 0 1 0 5"/>',
             anchor=(12, 9), pos=map_track(lid_dy, lambda v: [0, v[0]]), rot=shake(0, 8)),
    ])

    # 10 / 13 Offers, Marketing — megaphone shakes from the grip
    for key in ("10-offers", "13-marketing"):
        I[key] = comp(key[3:], [Part("megaphone", MEGA)],
                      parent={"anchor": (8, 14), "rot": shake(0, 12), "scale": pop(0, 92)})

    # 11 Support — buoy turns a quarter (4-fold symmetric, ends identical)
    I["11-support"] = comp("support", [Part("buoy",
        '<circle cx="12" cy="12" r="10"/><path d="m4.93 4.93 4.24 4.24"/><path d="m14.83 9.17 4.24-4.24"/>'
        '<path d="m14.83 14.83 4.24 4.24"/><path d="m9.17 14.83-4.24 4.24"/><circle cx="12" cy="12" r="4"/>',
        anchor=(12, 12), rot=spin(0, 90))])

    # 12 Content — page pops, lines retype left to right
    I["12-content"] = comp("content", [
        Part("page", '<path d="M6 22a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h8a2.4 2.4 0 0 1 1.704.706l3.588 3.588A2.4 2.4 0 0 1 20 8v12a2 2 0 0 1-2 2z"/>'
                     '<path d="M14 2v5a1 1 0 0 0 1 1h5"/>', anchor=(12, 22), scale=pop(0, 92)),
    ] + [Part("line%d" % n, '<path d="M8 %dH%d"/>' % (y, x2), trim=wipe(STAGGER * (n + 1)))
         for n, (y, x2) in enumerate([(9, 10), (13, 16), (17, 16)])])

    # 14 Risk — shield shakes, mark redraws, dot pops
    I["14-risk"] = comp("risk", [
        Part("shield", SHIELD),
        Part("mark", '<path d="M12 8v4"/>', trim=wipe(STAGGER * 2)),
        Part("dot", '<path d="M12 16h.01"/>', anchor=(12, 16), scale=dot(STAGGER * 4)),
    ], parent={"anchor": (12, 22), "rot": shake(0, 8)})

    # 15 Operators — person nods, cog turns 1/8 x2 (8-fold symmetric)
    cog = ('<path d="m14.305 16.53.923-.382"/><path d="m15.228 13.852-.923-.383"/><path d="m16.852 12.228-.383-.923"/>'
           '<path d="m16.852 17.772-.383.924"/><path d="m19.148 12.228.383-.923"/><path d="m19.53 18.696-.382-.924"/>'
           '<path d="m20.772 13.852.924-.383"/><path d="m20.772 16.148.924.383"/><circle cx="18" cy="15" r="3"/>')
    I["15-operators"] = comp("operators", [
        Part("body", '<path d="M10 15H6a4 4 0 0 0-4 4v2"/>'),
        Part("head", '<circle cx="9" cy="7" r="4"/>', anchor=(9, 7), pos=bob(STAGGER, -2)),
        Part("cog", cog, anchor=(18, 15), rot=spin(0, 90)),
    ])

    # 16 Audit — clip lifts, entries tick in
    I["16-audit"] = comp("audit", [
        Part("board", '<path d="M16 4h2a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h2"/>'),
        Part("clip", '<rect width="8" height="4" x="8" y="2" rx="1" ry="1"/>', anchor=(12, 4), pos=bob(0, -1.5)),
        Part("dot1", '<path d="M8 11h.01"/>', anchor=(8, 11), scale=dot(STAGGER)),
        Part("line1", '<path d="M12 11h4"/>', trim=wipe(STAGGER * 2)),
        Part("dot2", '<path d="M8 16h.01"/>', anchor=(8, 16), scale=dot(STAGGER * 3)),
        Part("line2", '<path d="M12 16h4"/>', trim=wipe(STAGGER * 4)),
    ])

    # 17 System — racks press in turn, status lights blink
    I["17-system"] = comp("system", [
        Part("rack1", '<rect width="20" height="8" x="2" y="2" rx="2" ry="2"/>', anchor=(12, 6), scale=pop(0, 90)),
        Part("rack2", '<rect width="20" height="8" x="2" y="14" rx="2" ry="2"/>', anchor=(12, 18), scale=pop(STAGGER, 90)),
        Part("led1", '<line x1="6" x2="6.01" y1="6" y2="6"/>', anchor=(6, 6), scale=dot(STAGGER * 2)),
        Part("led2", '<line x1="6" x2="6.01" y1="18" y2="18"/>', anchor=(6, 18), scale=dot(STAGGER * 3)),
    ])

    # 18 Settings — each knob slides and springs back, tracks follow
    parts = []
    # (y, knob x, gap-left end offset from knob, knob half-length, dx, left start, right end)
    rows = [(5, 14, 4, 2, -4, 3, 21), (12, 8, 0, 2, 5, 3, 21), (19, 16, 4, 2, -5, 3, 21)]
    for n, (y, k, gap, half, dx, x0, x1) in enumerate(rows):
        tr = scalar(0, n * STAGGER, [(9, dx, E_INOUT), (20, 0, E_BACK)])
        # left track ends at (k - gap) for rows 1/3 and at k for row 2; right starts at k (+4 on row 2)
        lend = (k - gap) if gap else k
        rstart = k if gap else k + 4
        parts.append(Part("left%d" % n, shapes=[line_shape(lend, y, x0, y)],
                          shape_track=[(0, map_track(tr, lambda v, l=lend, y=y, x0=x0: line_shape(l + v[0], y, x0, y)))]))
        parts.append(Part("right%d" % n, shapes=[line_shape(x1, y, rstart, y)],
                          shape_track=[(0, map_track(tr, lambda v, r=rstart, y=y, x1=x1: line_shape(x1, y, r + v[0], y)))]))
        kx = k if gap else k  # knob x
        parts.append(Part("knob%d" % n, shapes=[line_shape(kx, y - half, kx, y + half)], anchor=(kx, y),
                          pos=map_track(tr, lambda v: [v[0], 0])))
    I["18-settings"] = comp("settings", parts)

    # 19 AI & MCP — big spark pops with a turn, small sparks twinkle
    I["19-aiSettings"] = comp("aiSettings", [
        Part("spark", '<path d="M11.017 2.814a1 1 0 0 1 1.966 0l1.051 5.558a2 2 0 0 0 1.594 1.594l5.558 1.051a1 1 0 0 1 0 1.966l-5.558 1.051a2 2 0 0 0-1.594 1.594l-1.051 5.558a1 1 0 0 1-1.966 0l-1.051-5.558a2 2 0 0 0-1.594-1.594l-5.558-1.051a1 1 0 0 1 0-1.966l5.558-1.051a2 2 0 0 0 1.594-1.594z"/>',
             anchor=(12, 12), scale=pop(0, 72), rot=spin(0, 90)),
        Part("plus", '<path d="M20 2v4"/><path d="M22 4h-4"/>', anchor=(20, 4), scale=dot(STAGGER * 2),
             rot=scalar(0, STAGGER * 2, [(7, 0, HOLD), (18, 90, E_BACK)])),
        Part("orb", '<circle cx="4" cy="20" r="2"/>', anchor=(4, 20), scale=dot(STAGGER * 4)),
    ])

    # 20 Security — shield pops, check redraws
    I["20-security"] = comp("security", [
        Part("shield", SHIELD, anchor=(12, 22), scale=pop(0, 90)),
        Part("check", '<path d="m9 12 2 2 4-4"/>', trim=wipe(STAGGER * 2)),
    ])
    return I


if __name__ == "__main__":
    out = sys.argv[1]
    os.makedirs(out, exist_ok=True)
    for key, data in icons().items():
        with open(os.path.join(out, key + ".json"), "w") as f:
            json.dump(data, f, separators=(",", ":"))
    print("ok", len(icons()))
