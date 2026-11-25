#!/usr/bin/env python3
"""Generate the built-in 3D-style bot avatars.

The app must stay offline and single-binary, so avatars are baked into the UI
as data URIs (internal/server/web/avatars/avatars.js) plus PNG files for the
favicon and docs. To add a new face, add a row to PALETTE and rerun:

    python3 tools/avatar-src/make_avatars.py

Faces are drawn with layered radial gradients (soft top light + rim shadow) so
they read as clay/3D instead of flat emoji, and every bot gets its own colour
and expression so two bots never look alike.
"""
from PIL import Image, ImageDraw, ImageFilter
import base64, io, pathlib

SIZE = 128
SS = 4  # supersampling factor for smooth edges

OUT_DIR = pathlib.Path(__file__).parents[2] / "internal/server/web/avatars"
OUT_JS = OUT_DIR / "avatars.js"

# name, body colour, belly colour, accent (cheeks)
PALETTE = [
    ("maus",   "#9aa7b8", "#dfe6ef", "#1084fe"),
    ("clover", "#38d591", "#c9f7e3", "#0f9e69"),
    ("ember",  "#ff8a4c", "#ffd9bf", "#e2551f"),
    ("luna",   "#8b7bff", "#ddd6ff", "#5a46d6"),
    ("pip",    "#ffd166", "#fff0c2", "#d99b00"),
    ("nori",   "#3aa0ff", "#cfe8ff", "#0b62c4"),
    ("mochi",  "#ff8fb4", "#ffe0ec", "#c7386b"),
    ("rocky",  "#7d8a99", "#d5dce5", "#455261"),
    ("zest",   "#7ad151", "#e2ffd0", "#3f8c1f"),
    ("cocoa",  "#b07a55", "#f0d9c4", "#6f4426"),
    ("frost",  "#9fe8f5", "#e6fdff", "#3aa8bc"),
    ("raven",  "#4b5563", "#b9c2cf", "#1f2937"),
]

EYES = ["dots", "sleepy", "wink", "star", "happy", "round"]
MOUTHS = ["smile", "open", "smirk", "flat", "o"]


def hex2rgb(h):
    h = h.lstrip("#")
    return tuple(int(h[i:i + 2], 16) for i in (0, 2, 4))


def lighten(rgb, f):
    return tuple(min(255, int(c + (255 - c) * f)) for c in rgb)


def darken(rgb, f):
    return tuple(max(0, int(c * (1 - f))) for c in rgb)


def radial(w, center, radius, inner, outer):
    """Soft radial ball: `inner` at the centre, fading to transparent."""
    img = Image.new("L", (w, w), 0)
    d = ImageDraw.Draw(img)
    cx, cy = center
    steps = 26
    for i in range(steps, 0, -1):
        t = i / steps
        r = radius * t
        d.ellipse([cx - r, cy - r, cx + r, cy + r], fill=int(255 * (1 - t) ** 1.6))
    mask = img.filter(ImageFilter.GaussianBlur(radius * 0.18))
    layer = Image.new("RGBA", (w, w), inner + (0,))
    layer.putalpha(mask)
    return layer


def draw_face(d, cx, cy, body, eyes, mouth):
    ink = darken(body, 0.72)
    eye_y, dx, r = cy - 2, 16, 6

    if eyes == "dots":
        for s in (-1, 1):
            d.ellipse([cx + s * dx - r, eye_y - r * 1.2, cx + s * dx + r, eye_y + r * 1.2], fill=ink)
            d.ellipse([cx + s * dx - r + 2, eye_y - r + 1, cx + s * dx - r + 6, eye_y - 1], fill=(255, 255, 255, 230))
    elif eyes == "sleepy":
        for s in (-1, 1):
            d.arc([cx + s * dx - 8, eye_y - 6, cx + s * dx + 8, eye_y + 8], 200, 340, fill=ink, width=4)
    elif eyes == "wink":
        d.ellipse([cx - dx - r, eye_y - r * 1.2, cx - dx + r, eye_y + r * 1.2], fill=ink)
        d.ellipse([cx - dx - r + 2, eye_y - r + 1, cx - dx - r + 6, eye_y - 1], fill=(255, 255, 255, 230))
        d.arc([cx + dx - 8, eye_y - 4, cx + dx + 8, eye_y + 10], 200, 340, fill=ink, width=4)
    elif eyes == "star":
        for s in (-1, 1):
            p = cx + s * dx
            d.polygon([(p, eye_y - 8), (p + 3, eye_y - 2), (p + 9, eye_y), (p + 3, eye_y + 2),
                       (p, eye_y + 8), (p - 3, eye_y + 2), (p - 9, eye_y), (p - 3, eye_y - 2)], fill=ink)
    elif eyes == "happy":
        for s in (-1, 1):
            d.arc([cx + s * dx - 9, eye_y - 2, cx + s * dx + 9, eye_y + 12], 190, 350, fill=ink, width=4)
    else:  # round
        for s in (-1, 1):
            d.ellipse([cx + s * dx - 8, eye_y - 9, cx + s * dx + 8, eye_y + 9], fill=ink)
            d.ellipse([cx + s * dx - 5, eye_y - 6, cx + s * dx + 1, eye_y + 1], fill=(255, 255, 255, 235))

    my = cy + 16
    if mouth == "smile":
        d.arc([cx - 12, my - 10, cx + 12, my + 8], 20, 160, fill=ink, width=4)
    elif mouth == "open":
        d.ellipse([cx - 8, my - 4, cx + 8, my + 10], fill=ink)
        d.ellipse([cx - 4, my + 3, cx + 4, my + 9], fill=(255, 140, 160, 255))
    elif mouth == "smirk":
        d.arc([cx - 6, my - 8, cx + 16, my + 8], 20, 150, fill=ink, width=4)
    elif mouth == "o":
        d.ellipse([cx - 5, my - 4, cx + 5, my + 7], fill=ink)
    else:
        d.line([cx - 9, my + 2, cx + 9, my + 2], fill=ink, width=4)


def make_avatar(body, belly, accent, eyes, mouth):
    W = SIZE * SS
    img = Image.new("RGBA", (W, W), (0, 0, 0, 0))
    b, bl, ac = hex2rgb(body), hex2rgb(belly), hex2rgb(accent)

    # ground shadow
    sh = Image.new("RGBA", (W, W), (0, 0, 0, 0))
    ImageDraw.Draw(sh).ellipse([W * .18, W * .80, W * .82, W * .95], fill=(0, 0, 0, 90))
    img.alpha_composite(sh.filter(ImageFilter.GaussianBlur(W * .03)))

    # ears behind the head
    d = ImageDraw.Draw(img)
    ear_r = W * 0.175
    for s in (-1, 1):
        cx, cy = W / 2 + s * W * 0.285, W * 0.24
        d.ellipse([cx - ear_r, cy - ear_r, cx + ear_r, cy + ear_r], fill=darken(b, .12) + (255,))
        ir = ear_r * .55
        d.ellipse([cx - ir, cy - ir * .9, cx + ir, cy + ir * .9], fill=bl + (255,))

    # head with soft lighting, clipped to the head shape
    head = [W * .13, W * .13, W * .87, W * .90]
    d.ellipse(head, fill=b + (255,))
    clip = Image.new("L", (W, W), 0)
    ImageDraw.Draw(clip).ellipse(head, fill=255)

    light = radial(W, (W * .38, W * .32), W * .45, lighten(b, .55), b)
    img.paste(light, (0, 0), Image.composite(light.getchannel("A"), Image.new("L", (W, W), 0), clip))

    rim = radial(W, (W * .5, W * .99), W * .48, darken(b, .28), b)
    img.paste(rim, (0, 0), Image.composite(rim.getchannel("A"), Image.new("L", (W, W), 0), clip))

    # muzzle + cheeks + specular
    d = ImageDraw.Draw(img)
    d.ellipse([W * .35, W * .55, W * .65, W * .82], fill=bl + (120,))

    ch = Image.new("RGBA", (W, W), (0, 0, 0, 0))
    cd = ImageDraw.Draw(ch)
    for s in (-1, 1):
        cd.ellipse([W / 2 + s * W * .25 - W * .07, W * .58 - W * .045,
                    W / 2 + s * W * .25 + W * .07, W * .58 + W * .045], fill=ac + (90,))
    img.alpha_composite(ch.filter(ImageFilter.GaussianBlur(W * .012)))

    spec = Image.new("RGBA", (W, W), (0, 0, 0, 0))
    ImageDraw.Draw(spec).ellipse([W * .28, W * .21, W * .44, W * .31], fill=(255, 255, 255, 90))
    img.alpha_composite(spec.filter(ImageFilter.GaussianBlur(W * .02)))

    out = img.resize((SIZE, SIZE), Image.LANCZOS)
    # Faces are drawn last, in final-pixel coordinates: drawing them on the
    # supersampled canvas made every feature 4x too small.
    draw_face(ImageDraw.Draw(out), SIZE / 2, SIZE * 0.50, b, eyes, mouth)

    buf = io.BytesIO()
    out.save(buf, "PNG", optimize=True)
    return out, buf.getvalue()


def main():
    OUT_DIR.mkdir(parents=True, exist_ok=True)
    entries = []
    for i, (name, body, belly, accent) in enumerate(PALETTE):
        eyes, mouth = EYES[i % len(EYES)], MOUTHS[i % len(MOUTHS)]
        img, png = make_avatar(body, belly, accent, eyes, mouth)
        img.save(OUT_DIR / f"avatar-{name}.png")
        entries.append((name, eyes, mouth, base64.b64encode(png).decode()))

    js = ["// GENERATED by tools/avatar-src/make_avatars.py — jangan diedit manual.",
          "// Avatar 3D bawaan sebagai data URI, supaya aplikasi tetap offline & satu binary.",
          "window.OMB_AVATARS = ["]
    for name, eyes, mouth, b64 in entries:
        js.append('  {id:"%s", eyes:"%s", mouth:"%s", src:"data:image/png;base64,%s"},' % (name, eyes, mouth, b64))
    js.append("];")
    OUT_JS.write_text("\n".join(js) + "\n")

    total = sum(len(e[3]) for e in entries)
    print(f"wrote {len(entries)} avatars → {OUT_JS} ({total // 1024} KB base64)")
    for name, eyes, mouth, b64 in entries:
        print(f"  {name:8s} eyes={eyes:7s} mouth={mouth:6s} {len(b64) // 1024:3d} KB")


if __name__ == "__main__":
    main()
