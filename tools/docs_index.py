#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""把 web/src/docs.html 里的文档数据抽成 AI 检索用的 web/docs-index.json。

为什么不引 node 跑一遍：tools/ 下的门禁全部只依赖 Python，多一个运行时会
让 CI 和本地行为分叉。这里是「只解析 D 数组这一段 JS 字面量」——构造调用、
字符串、数组、对象，语法面很窄；一旦有人往 D 里写表达式（函数、模板串、
三元），解析器会直接报错而不是静默产出错内容，这是刻意的。

用法：
  python tools/docs_index.py            # 生成（覆盖）
  python tools/docs_index.py --check    # 只校验产物是否与源码同步（门禁用）
"""

import io
import json
import os
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SRC = os.path.join(ROOT, "web", "src", "docs.html")
OUT = os.path.join(ROOT, "web", "docs-index.json")

# 分块时每个正文块之间的分隔符。检索层按它切片段返回，所以要稳定。
SEP = "\n"


class ParseError(Exception):
    pass


class Tok:
    __slots__ = ("kind", "val", "pos")

    def __init__(self, kind, val, pos):
        self.kind = kind
        self.val = val
        self.pos = pos

    def __repr__(self):
        return "Tok(%s,%r)" % (self.kind, self.val)


def tokenize(src):
    out = []
    i, n = 0, len(src)
    while i < n:
        c = src[i]
        if c in " \t\r\n":
            i += 1
            continue
        if c == "/" and i + 1 < n and src[i + 1] == "/":
            j = src.find("\n", i)
            i = n if j < 0 else j + 1
            continue
        if c in "'\"":
            quote = c
            i += 1
            buf = []
            while i < n:
                ch = src[i]
                if ch == "\\":
                    if i + 1 >= n:
                        raise ParseError("字符串在转义处截断 @%d" % i)
                    nxt = src[i + 1]
                    if nxt == "n":
                        buf.append("\n")
                    elif nxt == "t":
                        buf.append("\t")
                    elif nxt == "r":
                        buf.append("\r")
                    elif nxt == "u":
                        buf.append(chr(int(src[i + 2:i + 6], 16)))
                        i += 4
                    else:
                        buf.append(nxt)
                    i += 2
                    continue
                if ch == quote:
                    i += 1
                    break
                buf.append(ch)
                i += 1
            else:
                raise ParseError("字符串未闭合 @%d" % i)
            out.append(Tok("str", "".join(buf), i))
            continue
        if c.isalpha() or c == "_":
            j = i
            while j < n and (src[j].isalnum() or src[j] == "_"):
                j += 1
            out.append(Tok("ident", src[i:j], i))
            i = j
            continue
        if c.isdigit() or (c == "-" and i + 1 < n and src[i + 1].isdigit()):
            j = i + 1
            while j < n and (src[j].isdigit() or src[j] == "."):
                j += 1
            out.append(Tok("num", src[i:j], i))
            i = j
            continue
        if c in "[]{}(),:":
            out.append(Tok("punct", c, i))
            i += 1
            continue
        raise ParseError("不认识的字符 %r @%d（D 数组里不应出现表达式）" % (c, i))
    return out


class Parser:
    def __init__(self, toks):
        self.t = toks
        self.i = 0

    def peek(self):
        return self.t[self.i] if self.i < len(self.t) else None

    def next(self):
        if self.i >= len(self.t):
            raise ParseError("意外到达结尾")
        tok = self.t[self.i]
        self.i += 1
        return tok

    def expect(self, val):
        tok = self.next()
        if tok.val != val:
            raise ParseError("期望 %r，实际 %r @%d" % (val, tok.val, tok.pos))
        return tok

    def value(self):
        tok = self.peek()
        if tok is None:
            raise ParseError("空输入")
        if tok.kind == "str":
            self.i += 1
            return tok.val
        if tok.kind == "num":
            self.i += 1
            return float(tok.val) if "." in tok.val else int(tok.val)
        if tok.kind == "ident":
            self.i += 1
            name = tok.val
            if name == "true":
                return True
            if name == "false":
                return False
            if name == "null":
                return None
            nxt = self.peek()
            if nxt is not None and nxt.val == "(":
                self.i += 1
                args = []
                if self.peek() is not None and self.peek().val != ")":
                    while True:
                        args.append(self.value())
                        sep = self.next()
                        if sep.val == ")":
                            break
                        if sep.val != ",":
                            raise ParseError("调用参数分隔符异常 %r @%d" % (sep.val, sep.pos))
                else:
                    self.expect(")")
                return ("call", name, args)
            return ("ident", name)
        if tok.val == "[":
            self.i += 1
            items = []
            if self.peek() is not None and self.peek().val != "]":
                while True:
                    items.append(self.value())
                    sep = self.next()
                    if sep.val == "]":
                        break
                    if sep.val != ",":
                        raise ParseError("数组分隔符异常 %r @%d" % (sep.val, sep.pos))
            else:
                self.expect("]")
            return items
        if tok.val == "{":
            self.i += 1
            obj = {}
            if self.peek() is not None and self.peek().val != "}":
                while True:
                    key = self.next()
                    if key.kind not in ("ident", "str"):
                        raise ParseError("对象键异常 %r @%d" % (key.val, key.pos))
                    self.expect(":")
                    obj[key.val] = self.value()
                    sep = self.next()
                    if sep.val == "}":
                        break
                    if sep.val != ",":
                        raise ParseError("对象分隔符异常 %r @%d" % (sep.val, sep.pos))
            else:
                self.expect("}")
            return obj
        raise ParseError("无法解析的记号 %r @%d" % (tok.val, tok.pos))


def split_d_array(src):
    start = src.index("var D = [")
    start = src.index("[", start)
    end = src.index("\n  ];", start)
    return src[start:end + len("\n  ]")]


def z_of(node):
    """把 z('中文','English') 或纯字符串统一成 {zh,en}。"""
    if isinstance(node, str):
        return {"zh": node, "en": node}
    if isinstance(node, tuple) and node[0] == "call" and node[1] == "z":
        zh = node[2][0] if len(node[2]) > 0 else ""
        en = node[2][1] if len(node[2]) > 1 else zh
        return {"zh": zh, "en": en}
    if isinstance(node, dict) and "zh" in node:
        return {"zh": node.get("zh", ""), "en": node.get("en", "")}
    return {"zh": "", "en": ""}


def items_of(node):
    """UL/OL/STEPS/FAQ 的元素：数组，每项是 z()。"""
    if not isinstance(node, list):
        return []
    return [z_of(x) for x in node]


def _pair(a, b):
    """P/NOTE 的双参数形态 → {zh,en}。第二个参数缺省时英文侧回落中文。"""
    zh = a if isinstance(a, str) else z_of(a)["zh"]
    if b is None:
        en = zh
    else:
        en = b if isinstance(b, str) else z_of(b)["en"]
    return {"zh": zh, "en": en}


def blocks_of(blocks):
    """把 body 里的构造调用展开成「结构化块」。

    检索只要纯文本，但静态分节页要还原成表格 / 代码 / 列表——纯文本拉平之后
    表格会变成一串「甲 · 乙 · 丙」，读起来完全失去结构，所以两种消费方共用
    这一份结构，再由 block_flat() 压成文本。
    """
    out = []
    for blk in blocks:
        kind = block_kind(blk)
        args = blk[2] if isinstance(blk, tuple) and len(blk) == 3 else []

        def arg(i, default=None):
            return args[i] if len(args) > i else default

        if kind == "P":
            pair = _pair(arg(0, ""), arg(1))
            out.append({"t": "p", "zh": pair["zh"], "en": pair["en"]})
        elif kind == "NOTE":
            # NOTE(zh, en, 'warn') —— 第三参数是提示级别，决定静态页的配色。
            pair = _pair(arg(0, ""), arg(1))
            lv = arg(2, "tip")
            out.append({"t": "note", "lv": lv if isinstance(lv, str) else "tip",
                        "zh": pair["zh"], "en": pair["en"]})
        elif kind == "CODE":
            # CODE('...','bat') 与 CODE(z('中文','English'))：后者是双语文件清单。
            code = z_of(arg(0, ""))
            label = arg(1, "")
            out.append({"t": "code", "label": label if isinstance(label, str) else "",
                        "zh": code["zh"], "en": code["en"]})
        elif kind == "TABLE":
            head = [z_of(x) for x in (arg(0) or [])]
            rows = [[z_of(c) for c in row] for row in (arg(1) or [])]
            out.append({"t": "table", "head": head, "rows": rows})
        elif kind in ("UL", "OL", "STEPS", "FAQ"):
            out.append({"t": kind.lower(), "items": items_of(arg(0) or [])})
        elif kind == "XLINK":
            links = []
            for it in (arg(0) or []):
                if isinstance(it, list) and len(it) >= 3:
                    links.append({"id": it[0], "zh": it[1], "en": it[2]})
            if links:
                out.append({"t": "xlink", "items": links})
        # H3 由调用方切开；ver 是页头版本占位，不是正文。
    return out


def block_flat(blocks):
    """结构化块 → {zh,en} 纯文本（检索用）。

    表头不拼进去：表头每行都重复一遍会让「端口 · 值 · 要点」这种词把每张表
    都命中，IDF 反而被稀释。
    """
    out = {"zh": [], "en": []}
    for b in blocks:
        t = b["t"]
        if t in ("p", "note", "code"):
            for lang in ("zh", "en"):
                if b[lang].strip():
                    out[lang].append(b[lang])
        elif t == "table":
            for lang in ("zh", "en"):
                rows = [" · ".join(c[lang] for c in r) for r in b["rows"]]
                if rows:
                    out[lang].append(SEP.join(rows))
        elif t in ("ul", "ol", "steps", "faq"):
            for lang in ("zh", "en"):
                text = SEP.join(i[lang] for i in b["items"])
                if text.strip():
                    out[lang].append(text)
        # xlink 只是「跳去哪一节」，不是正文：拼进检索文本会让每节都命中
        # 「排障索引」「安全边界」这类导航词。
    return {"zh": "\n".join(out["zh"]), "en": "\n".join(out["en"])}


def block_text(block):
    """单个块的纯文本。"""
    return block_flat(blocks_of([block]))


def block_kind(blk):
    """块的种类：函数调用取构造名（P/NOTE/H3/...），字面对象取 k（如 {k:'ver'}）。"""
    if isinstance(blk, tuple) and len(blk) == 3 and blk[0] == "call":
        return blk[1]
    if isinstance(blk, dict):
        return blk.get("k")
    return None


def build(with_blocks=False):
    """解析文档。

    with_blocks=True 时每个 chunk 额外带 "blocks"（结构化块），供静态分节页
    还原成表格 / 代码 / 列表。AI 检索的产物不带它——那份 JSON 要嵌进 exe，
    纯文本就够，没必要把单元格结构也塞进去。
    """
    src = io.open(SRC, encoding="utf-8").read()
    node = Parser(tokenize(split_d_array(src))).value()
    if not isinstance(node, list):
        raise ParseError("D 不是数组")

    sections = []
    for sec in node:
        if not isinstance(sec, dict):
            raise ParseError("节的形态异常：%r" % (sec,))
        sid = sec.get("id")
        if not isinstance(sid, str):
            raise ParseError("节缺 id：%r" % (sec,))
        title = z_of(sec.get("title"))
        group = z_of(sec.get("group"))
        chunks = []

        # 节的第一个 H3 之前那段正文，标题就是节标题，锚点 doc-<id>。
        cur = {"id": sid, "anchor": "doc-" + sid, "heading": title, "zh": [], "en": [], "blocks": []}
        for blk in sec.get("body", []):
            kind = block_kind(blk)
            if kind in ("H3", "h3"):
                chunks.append(cur)
                if isinstance(blk, tuple):
                    args3 = blk[2]
                    hid = args3[0] if len(args3) > 0 else sid
                    heading = {"zh": args3[1] if len(args3) > 1 else "",
                               "en": args3[2] if len(args3) > 2 else (args3[1] if len(args3) > 1 else "")}
                else:
                    hid = blk.get("id", sid)
                    heading = z_of(blk)
                cur = {"id": hid, "anchor": "s-" + str(hid), "heading": heading,
                       "zh": [], "en": [], "blocks": []}
                continue
            if kind == "ver":
                continue
            txt = block_text(blk)
            if txt["zh"].strip():
                cur["zh"].append(txt["zh"])
            if txt["en"].strip():
                cur["en"].append(txt["en"])
            if with_blocks:
                cur["blocks"].extend(blocks_of([blk]))
        chunks.append(cur)

        clean = []
        for ch in chunks:
            zh = "\n".join(ch["zh"]).strip()
            en = "\n".join(ch["en"]).strip()
            if not zh and not en:
                continue
            item = {
                "id": ch["id"],
                "anchor": ch["anchor"],
                "heading": ch["heading"],
                "text": {"zh": zh, "en": en},
            }
            if with_blocks:
                item["blocks"] = ch["blocks"]
            clean.append(item)

        sections.append({
            "id": sid,
            "anchor": "doc-" + sid,
            "group": group,
            "title": title,
            "chunks": clean,
        })

    return {
        "source": "web/src/docs.html",
        "note": "由 tools/docs_index.py 生成，勿手改。改文档请改 web/src/docs.html。",
        "sections": sections,
    }


def render(doc):
    return json.dumps(doc, ensure_ascii=False, indent=1, sort_keys=False) + "\n"


def write_out():
    """生成产物。返回 (节数, 块数, 字节数)。

    单独抽出来是给 web_build.py 复用的：文档索引与 docs.html 同源，
    必须一起重建，否则「改完文档忘了重建索引」只能靠门禁事后报红。
    """
    doc = build()
    body = render(doc)
    io.open(OUT, "w", encoding="utf-8", newline="\n").write(body)
    secs = doc["sections"]
    return len(secs), sum(len(s["chunks"]) for s in secs), len(body.encode("utf-8"))


def check_sync():
    """校验产物与源码同步。返回 (是否同步, 说明)。"""
    doc = build()
    body = render(doc)
    secs = doc["sections"]
    chunks = sum(len(s["chunks"]) for s in secs)
    if not os.path.exists(OUT):
        return False, "docs-index 缺失：先跑 python tools/docs_index.py"
    old = io.open(OUT, encoding="utf-8", newline="").read()
    if old.replace("\r\n", "\n") != body:
        return False, "docs-index 与 web/src/docs.html 不同步：重跑 python tools/docs_index.py"
    return True, "docs-index 同步：%d 节 / %d 块" % (len(secs), chunks)


def main():
    check = "--check" in sys.argv[1:]
    if check:
        ok, msg = check_sync()
        print(msg)
        return 0 if ok else 1
    secs, chunks, nbytes = write_out()
    print("wrote %s：%d 节 / %d 块 / %.1f KB" % (
        os.path.relpath(OUT, ROOT), secs, chunks, nbytes / 1024.0))
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except ParseError as e:
        print("解析 D 数组失败：%s" % e)
        sys.exit(2)
