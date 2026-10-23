"""Render packaging/icons/endpoint-agent.svg into the agent's .ico.

    python packaging/icons/make_agent_icon.py

Needs Pillow. The drawing below is the SVG's geometry on its 64-unit grid,
rendered at 8x and downsampled so the edges are antialiased. Sizes up to 24px
use a simplified mark (thicker monitor, plain green dot): the check and the
stand are sub-pixel at that size and turn into noise in the Control Panel list.

The output is committed; rerun this only when the design changes.
"""
from pathlib import Path

from PIL import Image, ImageDraw

BLUE = (0x1F, 0x5F, 0xD6, 255)
GREEN = (0x22, 0xA0, 0x6B, 255)
WHITE = (255, 255, 255, 255)
SS = 8  # supersampling factor
OUT = Path(__file__).resolve().parents[2] / "agent" / "windows" / "assets" / "endpoint-agent.ico"


def render(size: int) -> Image.Image:
    px = size * SS
    u = px / 64  # one SVG unit in pixels
    img = Image.new("RGBA", (px, px), (0, 0, 0, 0))
    d = ImageDraw.Draw(img)
    s = lambda v: round(v * u)  # noqa: E731

    d.rounded_rectangle([s(2), s(2), s(62), s(62)], radius=s(14), fill=BLUE)
    if size <= 24:
        d.rounded_rectangle([s(12), s(15), s(52), s(42)], radius=s(3), outline=WHITE, width=s(7))
        d.ellipse([s(47 - 13), s(45 - 13), s(47 + 13), s(45 + 13)], fill=GREEN)
    else:
        d.rounded_rectangle([s(13), s(16), s(51), s(41)], radius=s(3), outline=WHITE, width=s(4))
        d.line([s(26), s(49), s(38), s(49)], fill=WHITE, width=s(4))
        d.line([s(32), s(41), s(32), s(49)], fill=WHITE, width=s(4))
        for x, y in ((26, 49), (38, 49)):
            d.ellipse([s(x - 2), s(y - 2), s(x + 2), s(y + 2)], fill=WHITE)
        d.ellipse([s(47 - 11.5), s(44 - 11.5), s(47 + 11.5), s(44 + 11.5)], fill=BLUE)
        d.ellipse([s(47 - 10), s(44 - 10), s(47 + 10), s(44 + 10)], fill=GREEN)
        d.line([s(42.5), s(44), s(45.5), s(47), s(51), s(41)], fill=WHITE, width=s(2.8), joint="curve")
    return img.resize((size, size), Image.LANCZOS)


def main() -> None:
    sizes = [16, 20, 24, 32, 40, 48, 64, 128, 256]
    frames = [render(n) for n in sizes]
    OUT.parent.mkdir(parents=True, exist_ok=True)
    # BMP frames up to 128px for older Windows and for NSIS, which embeds the
    # icon into the installer; Pillow stores the 256px frame as PNG.
    frames[-1].save(OUT, format="ICO", sizes=[(n, n) for n in sizes], append_images=frames[:-1],
                    bitmap_format="bmp")
    print(f"wrote {OUT} ({OUT.stat().st_size} bytes)")


if __name__ == "__main__":
    main()
