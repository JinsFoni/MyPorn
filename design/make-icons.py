#!/usr/bin/env python3
"""从 design/app-icon.png（1254×1254 母版）生成站点用到的全部图标。

一次性脚本，改图标时重跑即可：

    python3 design/make-icons.py

产出：
  web/public/icon-192.png            普通图标（圆角，圆角外透明）
  web/public/icon-512.png            同上，512
  web/public/icon-maskable-512.png   Android 自适应图标（满幅不透明，系统自己裁）
  web/public/apple-touch-icon.png    iOS 主屏图标（满幅不透明，圆角由 iOS 自己加）
  web/public/favicon-32.png          浏览器标签页
  web/public/favicon-16.png          同上，小尺寸
  web/public/favicon.ico             老浏览器兜底（16/32/48 三档）

侧边栏标题左边那枚是单色 SVG 字形，不走这套位图（见 web/src/components/BrandMark.svelte）。
"""

from pathlib import Path

from PIL import Image, ImageDraw

ROOT = Path(__file__).resolve().parent.parent
SRC = ROOT / "design" / "app-icon.png"
PUBLIC = ROOT / "web" / "public"

# 母版本身就画了一圈霓虹圆角边框，圆角半径约占边长 17.5%；
# 生成时按同样比例切出透明角，贴到任何底色上都不会出现黑角。
CORNER_RATIO = 0.175


# 这张图是渐变丰富的插画，256 色量化后肉眼分辨不出，体积却能小一个数量级
# （512 → 296KB 降到 38KB），图标本身又是 PWA 预缓存的常驻文件，值得。
ICON_COLORS = 256


def downsample(im: Image.Image, size: int, colors: int | None = None) -> Image.Image:
    out = im.resize((size, size), Image.LANCZOS)
    if colors:
        out = out.convert("RGB").quantize(colors=colors, method=Image.FASTOCTREE)
    return out


def with_rounded_alpha(im: Image.Image, size: int, colors: int | None = None) -> Image.Image:
    """缩小到 size，并把圆角外切成透明（4 倍超采样画蒙版，边缘不发毛）。"""
    out = im.resize((size, size), Image.LANCZOS)
    ss = 4
    mask = Image.new("L", (size * ss, size * ss), 0)
    radius = round(size * CORNER_RATIO) * ss
    ImageDraw.Draw(mask).rounded_rectangle(
        (0, 0, size * ss - 1, size * ss - 1), radius=radius, fill=255
    )
    mask = mask.resize((size, size), Image.LANCZOS)
    out.putalpha(mask)
    if colors:
        # 带 alpha 一起量化（P 模式存 tRNS），同样 256 色，文件比 RGBA 小一个数量级。
        out = out.quantize(colors=colors, method=Image.FASTOCTREE)
    return out


def main() -> None:
    src = Image.open(SRC).convert("RGBA")

    with_rounded_alpha(src, 192, ICON_COLORS).save(PUBLIC / "icon-192.png", optimize=True)
    with_rounded_alpha(src, 512, ICON_COLORS).save(PUBLIC / "icon-512.png", optimize=True)

    # 自适应图标 / iOS 主屏：满幅不透明，裁切交给系统，透明边反而会被填成白底。
    downsample(src, 512, ICON_COLORS).save(PUBLIC / "icon-maskable-512.png", optimize=True)
    downsample(src, 180, ICON_COLORS).save(PUBLIC / "apple-touch-icon.png", optimize=True)

    # 小图标本来就只有几 KB，不量化，保住细边和白色播放键的干净边。
    with_rounded_alpha(src, 32).save(PUBLIC / "favicon-32.png", optimize=True)
    with_rounded_alpha(src, 16).save(PUBLIC / "favicon-16.png", optimize=True)
    with_rounded_alpha(src, 48).save(
        PUBLIC / "favicon.ico",
        sizes=[(16, 16), (32, 32), (48, 48)],
    )

    for name in (
        "icon-192.png",
        "icon-512.png",
        "icon-maskable-512.png",
        "apple-touch-icon.png",
        "favicon-32.png",
        "favicon-16.png",
        "favicon.ico",
    ):
        print(f"{name:24} {(PUBLIC / name).stat().st_size:>7} B")


if __name__ == "__main__":
    main()
