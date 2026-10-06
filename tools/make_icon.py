# -*- coding: utf-8 -*-
"""从一张方形插画生成 EnvKit 的多尺寸图标 app.ico（并可选输出网页 favicon 的 data-URI）。

用法（在本目录的上一级执行）：
    python tools/make_icon.py                      # 默认：头部+徽章、圆角
    python tools/make_icon.py --crop C             # 脸部特写（小尺寸更清晰）
    python tools/make_icon.py --crop B --square    # 方形（不切圆角）
    python tools/make_icon.py --crop A             # 整图（不推荐：16~32px 会糊）
    python tools/make_icon.py --favicon            # 额外打印 favicon 的 data-URI

生成后必须重新编入资源并构建：
    "%USERPROFILE%\\go\\bin\\rsrc.exe" -ico app.ico -o rsrc.syso
    go build -ldflags "-s -w" -o EnvKit.exe .
"""
import base64
import io
import os
import re
import struct
import sys


# **stdout 显式设 UTF-8。**
#
# Windows 上 PowerShell / 重定向给的是 GBK，
# 于是 `print('  ⚠ …')` 会抛 UnicodeEncodeError ——
# 崩在评测中途，看起来像「装置坏了」，而它只是控制台编码。
# recovery_sandbox.py 实测踩过：评测一行都没跑就崩在这里。
try:
    sys.stdout.reconfigure(encoding='utf-8')
except Exception:
    pass

from PIL import Image, ImageDraw

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SRC = os.path.join(ROOT, "cici.png")
ICO = os.path.join(ROOT, "app.ico")

# 裁剪预设（源图像素坐标；按 2048×2048 调好的，换图请自行微调）
CROPS = {
    "A": (0, 0, 2048, 2048),        # 整图
    "B": (130, 170, 1520, 1560),    # 头部 + 徽章（推荐）
    "C": (420, 360, 1480, 1420),    # 脸部特写（16~24px 最清晰）
}
SIZES = [16, 24, 32, 48, 64, 128, 256]


def rounded(img, radius_ratio=0.22, ss=4):
    """圆角遮罩（4 倍超采样做抗锯齿，避免小尺寸出现锯齿）"""
    w, h = img.size
    big = img.resize((w * ss, h * ss), Image.LANCZOS)
    mask = Image.new("L", big.size, 0)
    ImageDraw.Draw(mask).rounded_rectangle(
        [0, 0, big.size[0] - 1, big.size[1] - 1], radius=int(big.size[0] * radius_ratio), fill=255)
    big.putalpha(Image.composite(big.getchannel("A"), Image.new("L", big.size, 0), mask))
    return big.resize((w, h), Image.LANCZOS)


def bmp_entry(img):
    """把一张 RGBA 图编码成 ICO 里的 BMP 条目（BITMAPINFOHEADER + 倒序 BGRA + AND 掩码）。

    为什么不用 Pillow 直接 save('ICO')：新版 Pillow 对 RGBA 全部写 PNG 条目，
    虽然 Vista+ 都支持，但小尺寸用 BMP 才是兼容性最好的标准做法（旧外壳/第三方工具都认）。
    """
    w, h = img.size
    px = img.load()
    xor = bytearray()
    andmask = bytearray()
    rowbytes = ((w + 31) // 32) * 4
    for y in range(h - 1, -1, -1):          # BMP 是自下而上
        row = bytearray()
        androw = bytearray(rowbytes)
        for x in range(w):
            r, g, b, a = px[x, y]
            row += bytes((b, g, r, a))
            if a < 128:                      # 全透明像素在 AND 掩码里置 1
                androw[x // 8] |= 0x80 >> (x % 8)
        xor += row
        andmask += androw
    header = struct.pack("<IiiHHIIiiII", 40, w, h * 2, 1, 32, 0, len(xor), 0, 0, 0, 0)
    return bytes(header) + bytes(xor) + bytes(andmask)


def write_ico(path, images):
    """images: [(size, PIL.Image)]；<=64 用 BMP，>=128 用 PNG（标准做法）"""
    entries, blobs = [], []
    offset = 6 + 16 * len(images)
    for size, img in images:
        if size <= 64:
            data = bmp_entry(img)
            bpp = 32
        else:
            buf = io.BytesIO()
            img.save(buf, format="PNG", optimize=True)
            data = buf.getvalue()
            bpp = 32
        entries.append(struct.pack("<BBBBHHII",
                                   0 if size >= 256 else size, 0 if size >= 256 else size,
                                   0, 0, 1, bpp, len(data), offset))
        blobs.append(data)
        offset += len(data)
    with open(path, "wb") as f:
        f.write(struct.pack("<HHH", 0, 1, len(images)))
        for e in entries:
            f.write(e)
        for b in blobs:
            f.write(b)


def build(favicon=False):
    crop = "B"
    do_round = True
    inject_web = False
    for i, a in enumerate(sys.argv[1:]):
        if a == "--crop":
            crop = sys.argv[i + 2].upper()
        elif a == "--square":
            do_round = False
        elif a == "--favicon":
            favicon = True
        elif a == "--inject-web":
            inject_web = True
    if crop not in CROPS:
        raise SystemExit("crop 只能是 " + "/".join(CROPS))

    im = Image.open(SRC).convert("RGBA")
    icon = im.crop(CROPS[crop])
    if do_round:
        icon = rounded(icon)
    write_ico(ICO, [(s, icon.resize((s, s), Image.LANCZOS)) for s in SIZES])
    print("已生成 %s（crop=%s，%s，尺寸 %s）" % (
        os.path.relpath(ICO, ROOT), crop, "圆角" if do_round else "方形",
        ",".join(str(s) for s in SIZES)))

    buf = io.BytesIO()
    icon.resize((64, 64), Image.LANCZOS).save(buf, format="PNG", optimize=True)
    uri = "data:image/png;base64," + base64.b64encode(buf.getvalue()).decode()

    if favicon:
        print("favicon（64×64 PNG，%d 字节）：" % len(buf.getvalue()))
        print(uri)

    if inject_web:
        # 把图标写进前端（页面标题旁的 logo + 浏览器标签页图标），保持单文件不依赖外部资源
        p = os.path.join(ROOT, "web", "index.html")
        s = io.open(p, encoding="utf-8").read()
        new, n = re.subn(r'(const APP_ICON = ")data:image/png;base64,[^"]*(";)',
                         lambda m: m.group(1) + uri + m.group(2), s)
        if n != 1:
            raise SystemExit("web/index.html 里没找到 APP_ICON 占位，未注入（n=%d）" % n)
        io.open(p, "w", encoding="utf-8", newline="\n").write(new)
        print("已注入 web/index.html 的 APP_ICON（%d 字节 base64），记得重新 go build" % len(uri))


if __name__ == "__main__":
    build()
