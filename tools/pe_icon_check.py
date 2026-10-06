# -*- coding: utf-8 -*-
"""校验 exe 里真正嵌入的图标：解析 PE 资源段，取出 RT_GROUP_ICON / RT_ICON，
重建 ICO 并渲染成预览图（用于确认 rsrc 编进去的是新图标、且尺寸齐全）。

用法：python tools/pe_icon_check.py EnvKit.exe [_icon_preview/exe_icon.png]
"""
import io
import os
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

from PIL import Image

RT_ICON, RT_GROUP_ICON = 3, 14


def u16(b, o):
    return struct.unpack_from("<H", b, o)[0]


def u32(b, o):
    return struct.unpack_from("<I", b, o)[0]


def pe_resources(path):
    """返回 (icon_blobs{id: bytes}, group_entries[dict])"""
    d = open(path, "rb").read()
    e_lfanew = u32(d, 0x3C)
    assert d[e_lfanew:e_lfanew + 4] == b"PE\0\0", "不是 PE 文件"
    coff = e_lfanew + 4
    nsec = u16(d, coff + 2)
    opt_size = u16(d, coff + 16)
    opt = coff + 20
    magic = u16(d, opt)
    dd = opt + (112 if magic == 0x20B else 96)          # DataDirectory 起点
    res_rva, res_size = u32(d, dd + 2 * 8), u32(d, dd + 2 * 8 + 4)
    secs = []
    so = opt + opt_size
    for i in range(nsec):
        s = so + i * 40
        name = d[s:s + 8].rstrip(b"\0").decode("ascii", "ignore")
        vsize, vaddr, rawsize, rawptr = (u32(d, s + 8), u32(d, s + 12),
                                         u32(d, s + 16), u32(d, s + 20))
        secs.append((name, vaddr, vsize, rawptr, rawsize))

    def off(rva):
        for _, vaddr, vsize, rawptr, rawsize in secs:
            if vaddr <= rva < vaddr + max(vsize, rawsize):
                return rawptr + (rva - vaddr)
        return None

    root = off(res_rva)
    icons, groups = {}, []

    def walk(dir_off, level, path_ids):
        n_named, n_id = u16(d, dir_off + 12), u16(d, dir_off + 14)
        for i in range(n_named + n_id):
            e = dir_off + 16 + i * 8
            name_or_id, child = u32(d, e), u32(d, e + 4)
            if child & 0x80000000:                       # 指向下一级目录
                walk(root + (child & 0x7FFFFFFF), level + 1, path_ids + [name_or_id])
            else:                                        # 指向数据条目
                de = root + child
                data_rva, data_size = u32(d, de), u32(d, de + 4)
                data = d[off(data_rva):off(data_rva) + data_size]
                # 资源树固定三层：类型 → 名称/ID → 语言（1033=英文），数据条目出现在第三层，
                # 走到的 path_ids 为 [类型, 名称/ID]（语言层不再递归）
                if level == 2 and len(path_ids) >= 2 and path_ids[0] == RT_ICON:
                    icons[path_ids[-1]] = data
                if level == 2 and len(path_ids) >= 2 and path_ids[0] == RT_GROUP_ICON:
                    cnt = u16(data, 4)
                    for k in range(cnt):
                        g = 6 + k * 14
                        w, h, cc, rsv, planes, bpp, nbytes, iid = struct.unpack_from("<BBBBHHIH", data, g)
                        groups.append({"w": w or 256, "h": h or 256, "bpp": bpp,
                                       "id": iid, "bytes": nbytes})
    walk(root, 0, [])
    return icons, groups


def rebuild_ico(icons, groups):
    entries, blobs, offset = [], [], 6 + 16 * len(groups)
    for g in groups:
        data = icons.get(g["id"])
        if data is None:
            continue
        entries.append((g["w"], g["h"], len(data), offset))
        blobs.append(data)
        offset += len(data)
    out = bytearray(struct.pack("<HHH", 0, 1, len(entries)))
    for (w, h, size, o) in entries:
        out += struct.pack("<BBBBHHII", 0 if w >= 256 else w, 0 if h >= 256 else h, 0, 0, 1, 32, size, o)
    for b in blobs:
        out += b
    return bytes(out)


def main():
    exe = sys.argv[1] if len(sys.argv) > 1 else "EnvKit.exe"
    out = sys.argv[2] if len(sys.argv) > 2 else os.path.join("_icon_preview", "exe_icon.png")
    icons, groups = pe_resources(exe)
    print("PE 内 RT_ICON 条目:", len(icons), " RT_GROUP_ICON 尺寸:", [g["w"] for g in groups])
    if not groups:
        raise SystemExit("exe 里没有图标资源！")
    ico = rebuild_ico(icons, groups)
    im = Image.open(io.BytesIO(ico))
    sizes = sorted({s[0] for s in im.info.get("sizes", [])})
    print("重建后的 ICO 尺寸:", sizes)
    os.makedirs(os.path.dirname(out), exist_ok=True)

    # 预览：浅底/深底各一行，从左到右按 16→256 原生像素并排（放大 2 倍便于观察）
    pad, zoom = 14, 2
    rowh = max(sizes) * zoom + pad * 2
    width = sum(s * zoom + pad for s in sizes) + pad
    sheet = Image.new("RGB", (width, rowh * 2), (245, 245, 247))
    for ci, bg in enumerate(((245, 245, 247, 255), (32, 32, 36, 255))):
        x = pad
        for s in sizes:
            im.size = (s, s)                       # ICO 容器：按需解码到指定尺寸
            src = im.copy().convert("RGBA").resize((s * zoom, s * zoom), Image.NEAREST)
            band = Image.new("RGBA", (s * zoom, s * zoom), bg)
            band.alpha_composite(src)
            sheet.paste(band.convert("RGB"), (x, ci * rowh + pad))
            x += s * zoom + pad
    sheet.save(out)
    print("预览图:", out, sheet.size)


if __name__ == "__main__":
    main()
