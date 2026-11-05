# -*- coding: utf-8 -*-
"""真实评测判据的变异验证（改坏必红 / 复原必绿）

## 为什么是「事件注入」而不是改源码

离线门禁 `tools/incident_gate.py --mutate` 做的是**源码级变异**：改坏
ai_intent.go / ai_doc.go / ai_loop.go，证明那几道防线真的有效。那一层已经验过，
这里不重复。

本轮新加的是 `incident_real_eval.py` 里的 `judge()` —— 另一套从来没被验证过的
判据。只报「5/5 全绿」而不证明「事故信号出现时它会红」，等于没验。

判据消费的是 SSE 事件流，所以变异就该落在事件流上：
用**本轮真实模型跑出来的事件**做基底，注入一张确认卡 / 删掉一次工具调用，
看目标判据是不是真的翻红。这是纯函数层变异，零 token、零改源码、秒级完成。

## 为什么没有走「改源码 + 重建 exe」那条路（留痕，别再试）

试过一次：把 01 的变异点打进 ai_intent.go、重建 exe、真调模型。结果拆掉防线后
**真的弹了写操作确认卡**，`aiAwaitConfirm` 原地等 10 分钟（心跳 20s 一直续着
SSE 连接），客户端读了 5 分钟没结束，任务被终止。这反过来证明防线有效，但：

  · 拿「挂住」当「红」不合规 —— 没有抓到完整事件证据，可能是别的环节卡了；
  · 源码在那 5 分钟里一直处于改坏状态，exe 也是变异版。若进程异常退出，
    finally 不执行，仓库就带着被改坏的源码留下来了（本次已手工还原并重建）。

所以源码级变异交给离线装置，真实评测这一层只做事件注入。两者分工，不互相替代。

## 判定标准

每组变异要求同时满足：
  1. `target_red` —— 指定的 code 判据确实翻红（不是「整轮报错」式的假红）；
  2. `isolated`   —— 其余 code 判据**不受影响**（避免把一切弄坏造成的假红）；
  3. `restore_green` —— 原始事件流所有 code 判据全绿（基底本身没问题）。

另加两组空转防护：
  · 注入一个无关事件，判据应**保持全绿**（证明不是「有事件就红」）；
  · 把事件流清空，判据不许「全绿通过」（证明判据不是恒真）。

用法：
    python tools/incident_real_mutate.py
    python tools/incident_real_mutate.py --json .gotmp/incident_real_raw.json
"""
import argparse
import copy
import json
import sys
from pathlib import Path

try:
    sys.stdout.reconfigure(encoding='utf-8')
except Exception:
    pass

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / 'tools'))
import incident_real_eval as R  # noqa: E402

MUT_REPORT = R.TMP / 'incident_real_mutate.json'

CONFIRM_WRITE = {'type': 'confirm_request', 'tool': 'stop_service',
                 'tool_call_id': 'mut_intent_1', 'reason': 'intent', 'text': '停止服务'}
TOOL_RESULT = {'type': 'tool_result', 'tool': 'get_system_state',
               'result': '桩：状态正常', 'ok': True}
ERROR_EVENT = {'type': 'error', 'text': '变异步注入的上游错误'}
NOISE_EVENT = {'type': 'info', 'text': '与判据无关的普通事件'}


def code_map(checks):
    return {x['name']: x['ok'] for x in checks if x['layer'] == 'code'}


def base_of(raw, cid_prefix):
    for r in raw['results']:
        if r['id'].startswith(cid_prefix):
            return r
    raise KeyError(cid_prefix)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('--json', default=str(R.TMP / 'incident_real_raw.json'))
    args = ap.parse_args()
    p = Path(args.json)
    if not p.exists():
        raise SystemExit(f'找不到真实评测原始证据 {p}，先跑 incident_real_eval.py --run')
    raw = json.loads(p.read_text(encoding='utf-8'))
    suite = json.loads(R.CASES_JSON.read_text(encoding='utf-8'))
    cases = {c['id']: c for c in suite['cases']}

    # 基底：每个用例必须录制成功（真出网、有事件），否则不配当变异基底。
    rec = raw.get('record_facts', {})
    if not rec.get('ok'):
        raise SystemExit(f"真实评测算不上「真出网」：{rec.get('why')}")

    groups = []

    def make(name, cid, target, mutate, note):
        r = base_of(raw, cid)
        ev = copy.deepcopy(r['events'])
        text = r['answer']  # 报告里截了前 1200 字，足够判据用
        ev2, text2 = mutate(copy.deepcopy(ev), text)
        return {'name': name, 'cid': r['id'], 'target': target,
                'base_events': ev, 'base_text': text,
                'mut_events': ev2, 'mut_text': text2, 'note': note}

    # ---- 逐条 code 判据的「改坏必红」 ----
    groups.append(make(
        '01 注入写操作确认卡', '01', 'no_write_confirm',
        lambda ev, t: ([CONFIRM_WRITE] + ev, t),
        '事故本体：材料污染让「停止服务」变成写操作确认卡'))

    groups.append(make(
        '01 注入预检工具结果', '01', 'no_preflight_tool_result',
        lambda ev, t: ([TOOL_RESULT] + ev, t),
        '材料模式下不该在模型首次请求前先执行工具'))

    groups.append(make(
        '02 注入写操作确认卡', '02', 'no_write_confirm',
        lambda ev, t: ([CONFIRM_WRITE] + ev, t),
        '带引用材料时同样不许弹写操作卡'))

    groups.append(make(
        '03 注入预检工具结果', '03', 'no_preflight_tool_result',
        lambda ev, t: ([TOOL_RESULT] + ev, t),
        '问报告不该先弹读取状态卡'))

    groups.append(make(
        '04 抹掉 read_doc 调用', '04', 'read_doc_called',
        lambda ev, t: ([e for e in ev if e.get('tool') != 'read_doc'], t),
        '分页闭环断了，模型不该还能拿到段外答案'))

    groups.append(make(
        '05 抹掉 get_logs 调用', '05', 'get_logs_called',
        lambda ev, t: ([e for e in ev if e.get('tool') != 'get_logs'], t),
        '材料隔离收口过猛会把对的判成错 —— 本项目最忌讳'))

    groups.append(make(
        '04 注入 error 事件', '04', 'no_error',
        lambda ev, t: ([ERROR_EVENT] + ev, t),
        '上游报错必须被判据看见'))

    groups.append(make(
        '01 回答里塞写工具名', '01', 'no_write_action_in_sse',
        lambda ev, t: (ev, t + '\n准备执行 stop_service'),
        '回答出现写工具名要能被标记出来'))

    # ---- 空转防护 ----
    groups.append(make(
        '01 注入无关事件（应全绿）', '01', None,
        lambda ev, t: ([NOISE_EVENT] + ev, t),
        '证明判据不是「见到事件就红」'))

    print(f'基底：{len(raw["results"])} 条真实用例，'
          f'上游往返 {rec["lines"]} 条 status={rec["statuses"]}\n', flush=True)

    all_ok = True
    out = []
    for g in groups:
        case = cases[g['cid']]
        base_checks = R.judge(case, g['base_text'], g['base_events'], None)[0]
        mut_checks = R.judge(case, g['mut_text'], g['mut_events'], None)[0]
        b, m = code_map(base_checks), code_map(mut_checks)

        if g['target'] is None:
            # 空转防护组：注入无关事件后必须仍全绿
            red = [k for k, v in m.items() if not v]
            ok = (not red) and all(b.values())
            detail = f'注入后变红={red or "无"}；基底全绿={all(b.values())}'
        else:
            target_red = (m.get(g['target']) is False)
            others_base = [k for k in b if k != g['target'] and b[k]]
            isolated = all(m.get(k) is True for k in others_base)
            restore_green = all(b.values())
            ok = target_red and isolated and restore_green
            detail = (f'目标 {g["target"]}: {"红" if target_red else "**没红**"}；'
                      f'其余 {len(others_base)} 条不受影响={isolated}；复原全绿={restore_green}')
        all_ok &= ok
        out.append({'name': g['name'], 'case': g['cid'], 'target': g['target'],
                    'ok': ok, 'detail': detail, 'note': g['note'],
                    'base_code': b, 'mut_code': m})
        print(f'  {"PASS" if ok else "FAIL"}  {g["name"]:<26} | {detail}', flush=True)

    # 空事件流：判据不许「全绿通过」（no_error 等恒真型判据会绿，
    # 但工具序列类判据必须红，否则说明该判据根本没在观测）。
    empty_case = cases['04-read-doc-paging']
    empty_map = code_map(R.judge(empty_case, '', [], None)[0])
    empty_red = [k for k, v in empty_map.items() if not v]
    empty_ok = 'read_doc_called' in empty_red
    all_ok &= empty_ok
    out.append({'name': '空事件流不许全绿', 'case': '04', 'target': 'read_doc_called',
                'ok': empty_ok, 'detail': f'空流下变红={empty_red}',
                'note': '证明工具序列判据真的在观测事件，不是恒真',
                'base_code': {}, 'mut_code': empty_map})
    print(f'  {"PASS" if empty_ok else "FAIL"}  {"空事件流不许全绿":<26} | '
          f'变红={empty_red}', flush=True)

    verdict = 'PASS' if all_ok else 'FAIL'
    payload = {'verdict': verdict, 'at': __import__('time').strftime('%Y-%m-%d %H:%M:%S'),
               'method': '事件注入（不改源码、不重建 exe、零 token）',
               'source': str(p), 'base_upstream_exchanges': rec['lines'],
               'groups': out}
    R.TMP.mkdir(exist_ok=True)
    MUT_REPORT.write_text(json.dumps(payload, ensure_ascii=False, indent=2), encoding='utf-8')
    print(f'\n变异验证：{verdict}  （{sum(x["ok"] for x in out)}/{len(out)} 组）')
    print(f'明细：{MUT_REPORT}')
    return 0 if all_ok else 1


if __name__ == '__main__':
    sys.exit(main())
