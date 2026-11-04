#!/usr/bin/env python3
"""重新生成 recordings/*.jsonl（事故回放语料）。

格式与 ai_replay.go 的 aiExchange 完全一致：一行 JSON、body 存**原始 SSE 字节**。
回放走的是线上那条解析路径，所以这里必须写 SSE 文本而不是"结构化回答"。

{{DOC_ID}} 是 runner 在运行时替换的占位符：文档 id 由 aiDocStore 随机生成，
静态录像里没法写死。替换发生在写临时文件之后、设 ENVKIT_AI_REPLAY 之前。

产物已固化在仓库里；这个脚本只是让语料的构造方式可核对、可重生成。
"""
import json
from pathlib import Path

HERE = Path(__file__).parent
OUT = HERE / "recordings"
OUT.mkdir(exist_ok=True)


def sse(*frames: str, done: bool = True) -> str:
    out = "".join("data: " + f + "\n\n" for f in frames)
    if done:
        out += "data: [DONE]\n\n"
    return out


def content_frame(text: str, finish: str = "stop") -> str:
    return json.dumps(
        {"choices": [{"delta": {"content": text}, "finish_reason": finish}]},
        ensure_ascii=False,
    )


def usage_frame(prompt: int, completion: int) -> str:
    return json.dumps(
        {"choices": [], "usage": {"prompt_tokens": prompt, "completion_tokens": completion}},
        ensure_ascii=False,
    )


def tool_call_frame(name: str, arguments: dict) -> str:
    # arguments 在 OpenAI SSE 里是**字符串**，不是对象
    return json.dumps(
        {
            "choices": [
                {
                    "delta": {
                        "tool_calls": [
                            {
                                "index": 0,
                                "id": "call_incident_1",
                                "type": "function",
                                "function": {"name": name, "arguments": json.dumps(arguments, ensure_ascii=False)},
                            }
                        ]
                    },
                    "finish_reason": "tool_calls",
                }
            ]
        },
        ensure_ascii=False,
    )


def exchange(seq: int, body: str, n_msgs: int, n_tools: int = 12) -> dict:
    return {
        "v": 1,
        "seq": seq,
        "at": "2026-11-04 19:32:00.000",
        "status": 200,
        "stream": True,
        "model": "incident-replay",
        "n_msgs": n_msgs,
        "n_tools": n_tools,
        "body": body,
    }


def write(name: str, rows: list[dict]) -> None:
    p = OUT / name
    p.write_text("".join(json.dumps(r, ensure_ascii=False) + "\n" for r in rows), encoding="utf-8", newline="\n")
    print(f"wrote {p.name}: {len(rows)} exchange(s), {p.stat().st_size} bytes")


# 01 —— 文档污染：材料里全是写操作关键词，模型只该回答文档内容，不该弹确认卡
write(
    "01-doc-pollution.jsonl",
    [
        exchange(
            1,
            sse(
                content_frame("这是一份关于农产品溯源管理系统的论文初稿。"),
                usage_frame(2100, 20),
            ),
            n_msgs=3,
        )
    ],
)

# 02 —— docs 引用：模型回答不重要，断言落在「真正发给上游的请求体」里有没有材料标记
write(
    "02-doc-ref-hydrate.jsonl",
    [
        exchange(
            1,
            sse(
                content_frame("已读到你附的项目内部编号说明。"),
                usage_frame(900, 16),
            ),
            n_msgs=3,
        )
    ],
)

# 03 —— 「帮我看看这份报告」不该被当成状态查询
write(
    "03-intent-narrow.jsonl",
    [
        exchange(
            1,
            sse(
                content_frame("这份报告总结的是本季度系统运行情况。"),
                usage_frame(900, 18),
            ),
            n_msgs=3,
        )
    ],
)

# 04 —— 大文档分页：模型必须真调 read_doc 才能拿到第 330 行的答案
write(
    "04-read-doc-paging.jsonl",
    [
        exchange(
            1,
            sse(
                tool_call_frame("read_doc", {"doc_id": "{{DOC_ID}}", "offset": 300, "lines": 400}),
                usage_frame(3000, 24),
            ),
            n_msgs=3,
        ),
        exchange(
            2,
            sse(
                content_frame("THE_HIDDEN_ANSWER_IS_BLUE_7742"),
                usage_frame(3600, 12),
            ),
            n_msgs=5,
        ),
    ],
)

# 05 —— 负向对照：不带文档的运维指令必须照常走预检，防止「收口过猛」把对的判成错
write(
    "05-ops-command-control.jsonl",
    [
        exchange(
            1,
            sse(
                content_frame("已读取系统日志，暂无异常。"),
                usage_frame(1200, 14),
            ),
            n_msgs=5,
        )
    ],
)
