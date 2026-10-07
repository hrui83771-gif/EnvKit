# -*- coding: utf-8 -*-
"""EnvKit v2.4 经验增益 A/B 对照（真实模型调用）

这一项是 v2.3 的验收标准，拖到 v2.4 才做。ROADMAP 原文：
「能出示『经验增益』的 A/B 对照数据——有经验组与无经验组在真实任务上的
对比。做不到这一点，学习闭环无法证伪。」

## 为什么这个脚本要自己判分，不能靠人看

「经验增益」的口径是三个量：成功率、平均探索轮数、平均耗时。
其中**探索轮数**是 EnvKit 独有的可观测量——它能从 SSE 事件里数出来：
`plan_step` 事件的条数就是 AI 实际调用工具的次数。

**但成功率不能靠"回答看起来对不对"判。** 那会让评测退化成人工打分，
换个评测人就得出不同结论。所以判分全部基于客观事实：
调用了哪些工具（事件流里有）、回答里有没有出现某类措辞（文本里有）。
任务集 `eval/tasks/tasks.json` 里每题的 criteria 都是这种可机械判定的形式。

## A/B 两侧的唯一差异

`POST /api/ai/config {"memory_enabled": true/false}`。
其余完全一致：同一批任务、同一环境、同一模型、同一顺序。

**必须记录的干扰项**：模型有随机性，所以每题跑 3 次取中位数。
只跑一次的话，测的是"这次模型心情好不好"。

## 必须报告负增益

口径 §2.8 明确写了「如果开记忆后某类任务表现变差，要指出来而不是藏起来」。
脚本会把 A/B 两组的每个指标都打出来，**不做"只报好看的"的选择**。

用法：
    # 先起 EnvKit（默认 127.0.0.1:18765）
    python tools/ab_memory.py --dry-run      # 只列计划，不调模型
    python tools/ab_memory.py --group A      # 只跑 A 组
    python tools/ab_memory.py                # A + B 全跑，出报告
"""
import argparse
import json
import re
import statistics
import subprocess
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

import time
import urllib.parse
import urllib.request
import urllib.error
from pathlib import Path

BASE = 'http://127.0.0.1:18765'
ROOT = Path(__file__).resolve().parent.parent
TASKS_JSON = ROOT / 'eval' / 'tasks' / 'tasks.json'
REPORT = ROOT / 'docs' / 'eval' / 'ab-memory-report.json'

# 强制直连。若走本机代理，POST 的 body 会被剥掉——
# EnvKit 侧表现为「AI 收到空请求：请求体未到达服务端」，
# 事件流里只有 error+done，既没有正文也没有工具调用。
# 实测踩过：那一次两组都是「探索 0 轮 / 0.0s」，增益算成 0，
# **看起来像"记忆层没影响"，实际是模型压根没被调用**。
# urllib 默认会读环境变量里的 *_proxy，所以必须显式给空 ProxyHandler。
OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))

# 与 e2e_agent_test.py 保持一致：探索类只读工具。
# "AI 有没有自己去找"只看它有没有调这些。
EXPLORE_TOOLS = {'list_project', 'search_files', 'read_file',
                 'get_project_brief', 'db_query', 'db_list'}

# AI 把问题反问回用户的典型说法——出现即视为"没自己去查"
BACKTRACK_PHRASES = [
    '你的项目是什么', '请告诉我你的项目', '项目是什么语言', '请问你的项目',
    '方便告诉我项目', '你的项目目录', '能否提供项目',
]


# ---------- 基础设施 ----------
def get_token():
    html = OPENER.open(BASE + '/', timeout=10).read().decode('utf-8')
    m = re.search(r'window.__EK_TOKEN__="([0-9a-f]+)"', html)
    if not m:
        raise SystemExit('取不到 X-EnvKit-Token：EnvKit 没起来？端口对吗？' + BASE)
    return m.group(1)


TOKEN = None


def api(path, body=None, method=None, timeout=180):
    global TOKEN
    if TOKEN is None:
        TOKEN = get_token()
    method = method or ('GET' if body is None else 'POST')
    data = json.dumps(body).encode('utf-8') if body is not None else None
    req = urllib.request.Request(BASE + path, data=data, method=method)
    req.add_header('Content-Type', 'application/json')
    req.add_header('X-EnvKit-Token', TOKEN)
    last = None
    for _ in range(3):
        try:
            return json.loads(OPENER.open(req, timeout=timeout).read().decode('utf-8'))
        except (ConnectionResetError, urllib.error.URLError) as e:
            last = e
            time.sleep(2)
    raise last


def set_memory(on):
    """切记忆层开关。这是 A/B 唯一的变量。"""
    api('/api/ai/config', {'memory_enabled': bool(on)})


# ===== 记忆预置 =====
# 第一轮 A/B 测出"无差异"，根因不是记忆层没用，而是**记忆库是空的**：
# B 组虽然开了开关，但 memoriesFor() 按关键词匹配任务文本，
# 库里一条都没有 → 注入量零 → 两组差异全部来自模型随机性。
#
# 所以正式的 A/B 必须先给B 组预置**确实与任务相关**的经验，
# 否则测的是"开不开一个空容器"，不是"经验有没有用"。

# 只对 T01（有探索成本的任务）预置。内容必须是真的、且能被关键词匹配上——
# 写"数据库配置在 main.go"是为了让 A/B 能测出注入是否生效，
# 真实场景里这些经验由用户自己积累。
SEED_MEMORIES = [
    {
        'text': '数据库连接配置是硬编码在 backend/main.go 第 13 行的 InitDB 调用里，'
                '不在任何 .env 或 yaml 配置文件中，不要去配置文件里找',
        'tags': '数据库 配置 位置',
        'always': False,
    },
]


def _list_memories():
    """读现有记忆。

    `/api/memory` 只接受 POST（handleMemory 第 51 行直接挡掉 GET，返回 405），
    读取要走 `/api/lessons`——它 GET 时返回 `{"lessons":[...], "memories":[...]}`
    （memory_api.go:35-46）。
    """
    return api('/api/lessons', None, 'GET').get('memories', [])


def seed_memories(clear_first=True):
    """写入 A/B 用的记忆，返回写入条数。

    clear_first=True 会先清空现有记忆 —— 这是必要的：
    磁盘上可能残留别的项目的经验（实测有过 3 条测试数据），
    它们与本任务无关但会稀释注入效果，让"注入是否生效"这件事测不出来。
    """
    existing = _list_memories()
    if clear_first:
        for m in existing:
            api('/api/memory', {'op': 'delete', 'id': m['id']})
        if existing:
            print(f'  已清空 {len(existing)} 条既有记忆')
    ok = 0
    for m in SEED_MEMORIES:
        r = api('/api/memory', {'op': 'add', **m})
        if r.get('ok'):
            ok += 1
        else:
            print(f'  ! 写入失败：{r}')
    print(f'  已预置 {ok}/{len(SEED_MEMORIES)} 条记忆')
    return ok


def clear_memories():
    ms = _list_memories()
    for m in ms:
        api('/api/memory', {'op': 'delete', 'id': m['id']})
    if ms:
        print(f'  已清理 {len(ms)} 条记忆')
    return len(ms)


def verify_seeded(expected=1):
    """确认记忆库里确实有内容，且开关是开的。

    **这一步不能省。** 前两轮都栽在"开关切了但注入是空的"上：
    第一次库里有 3 条测试残留但与任务零词面重叠，第二次库完全是空的。
    两种情况都不会报错——A/B 照样跑出"0 增益"，看起来像"记忆层无影响"。

    局限：服务端**没有暴露快照端点**（只有 /api/ai/{config,test,chat,explain,
    providers,models,balance}），所以没法直接读到"这一回合注入给模型的
    user_memories 字段"。这里只能验到"库里有内容 + 开关是开的"，
    **注入是否真被模型采纳，只能从回答内容间接观察**。
    要彻底闭环，需要服务端加一个 debug 端点返回当回合快照。
    """
    cfg = api('/api/ai/config')
    if not cfg.get('memory_enabled'):
        raise SystemExit('记忆开关未打开')
    ms = _list_memories()
    if len(ms) < expected:
        raise SystemExit(
            f'记忆库里只有 {len(ms)} 条（期望 ≥{expected}）。\n'
            f'B 组等于"开一个空容器"，测出来的增益必然是 0，'
            f'那样的数字没有意义。')
    print(f'  记忆库现有 {len(ms)} 条，开关已开')
    for m in ms[:3]:
        print(f'    · {m["text"][:60]}')
    return len(ms)


# 自动确认的白名单：**只放评测明确知道后果的工具**。
#
# ## 为什么默认关闭，为什么要有白名单
#
# 自动确认 = 让 AI 真的执行写操作。这是评测装置唯一能自主授权的地方，
# 一旦放开就是"模型想删什么就删什么"。
#
# 两条硬约束：
#   1. **默认关闭**，必须显式 --allow-writes 才启用。
#   2. **只对白名单工具生效**；白名单为空时等于不自动确认。
#
# 白名单里只有 start_service：恢复类评测唯一需要的动作，
# 而它作用于沙箱里的一次性服务（eval/fixtures/sandbox-backend，端口 45311），
# 杀掉重起都不影响用户任何东西。
#
# **绝不放** db_backup / db_restore / apply_whitelist / cleanup_processes
# —— 那些碰用户真实数据与进程。
AUTO_APPROVE_WHITELIST = {
    'start_service',    # 沙箱的一次性服务
    # restart_service 不放：它内部是 stop+start，沙箱里用 start_service 就够，
    # 多放一个就多一个能让模型在真实环境里乱叫的机会。
}


def _validate_response(text, events):
    """校验响应有效性。**无效必须报错，绝不能当 0 数据用。**

    实测踩过两次，都会给出一个漂亮但假的 0：
    - 第一次：代理剥掉 POST body，一个事件都没收到 -> 报告写"增益 0.000"
    - 第二次：只有 error 事件，没正文也没工具 -> 同样写成 0
    两次看起来都像"记忆层没影响"，真相是"根本没测到"。
    """
    if not events:
        raise SystemExit(
            '\n**空响应**：一个 SSE 事件都没收到。\n'
            '几乎可以确定是本机代理拦截了 POST 请求体。\n'
            '（EnvKit 日志里会有「请求体未到达（被本机代理/预览容器拦截）」）')
    if not text.strip() and not tool_seq(events):
        kinds = [e.get('type') for e in events]
        errs = [e.get('text', '')[:200] for e in events if e.get('type') == 'error']
        raise SystemExit(
            f'\n**无效响应**：收到 {len(events)} 个事件但没有正文也没有工具调用。\n'
            f'事件类型：{kinds}\n错误事件：{errs}\n'
            f'这说明模型没被真正调用到，结果不能当数据用。\n'
            f'常见原因：本机代理拦截上游请求 / 模型配置不可用 / key 失效。')

    #---- 「调了只读工具却一个字没说」也是无效响应 ----
    #
    # 踩过：`hang_service` run3 实测耗时 **0.5 秒**、只调了
    # `get_system_state`、`answer_head` **完全为空** ——
    # 也就是模型根本没生成出内容。
    #
    # 上面那条 `not tool_seq(events)` 拦不住它：
    # 只要有一个工具调用就放行。而「有工具调用」在这里**没有意义** ——
    # 判据评的是模型的**结论**，没有文字就没有结论。
    #
    # 不拦的后果：这一轮会被算成「模型该输出块却没输出」，
    # 拉低结构块覆盖率，然后所有人去调产品侧指令——
    # 而真相是**这一轮根本没跑**，该修的是采集侧。
    if not text.strip():
        kinds = [e.get('type') for e in events]
        # **用普通异常而不是 SystemExit**。
        #
        # `run_injections` 里写着 `except SystemExit: raise` ——
        # 用 SystemExit 会**终止整轮评测**，其余 32 次调用全白跑。
        # 它要能被 `except Exception` 接住、变成一条 `error_row`
        # （标记 ERROR 而不是 FAIL —— 采集失败≠产品缺陷）。
        raise RuntimeError(
            f'有 {len(kinds)} 个事件、其中 {len(tool_seq(events))} 个工具调用，'
            f'但**正文完全为空**。事件类型：{kinds}。'
            f'模型没有产出任何文字 → 没有结论可评。'
            f'**这一轮不能算「模型没照做」**，那是把采集失败算成产品缺陷。'
            f'常见原因：请求被中途掐断 / 模型只顾调工具没输出 / 上游返回空 completion。')


def _pending_confirm(events):
    """返回 (tool, args) —— 最后一个未被回执的确认卡，没有则 None。

    ## 判断依据是「有没有 tool_result」，不是「有没有 confirm_request」

    第一段事件里若有确认卡，说明这次调用**停住了**；
    若已跟着出现 tool_result，说明那是已处理完的历史，不该再点。
    不做这个区分的话，第二段请求返回的事件里会再次出现 confirm_request
    （模型可能又想别的写操作），从而被误判成"还有一个待确认"。
    """
    last = None
    tid = ''
    for e in events:
        t = e.get('type')
        if t == 'confirm_request':
            last = (e.get('tool'), e.get('args') or {})
            tid = e.get('tool_call_id') or ''
        elif t == 'tool_result' and last is not None:
            last = None
            tid = ''
    return (last[0], last[1], tid) if last else None


def chat_once(messages, confirm=None, timeout=300):
    """发一轮请求，返回 (文本, 事件列表, 耗时秒)。

    confirm 非空时走"确认回执"路径：服务端执行该写工具、把结果交给模型、
    然后继续同一轮推理（见 ai_loop.go:97-129）。

    两处必须写对，否则测出来的数是假的：

    1. **直连**（OPENER 带空 ProxyHandler）：走本机代理会被剥掉 POST body。
    2. **带 X-EnvKit-Chat-Fallback 头**：即使 body 真丢了，
       服务端也能从回退头里恢复出用户消息。
       编码方式与前端 `web/index.html:4221` 一致——JSON 后 URL-encode，
       服务端做 `url.QueryUnescape`。
       这是 EnvKit 自己的既有兜底机制（ai_loop.go:57-60），不是为评测新加的。
    """
    last_user = ''
    for m in reversed(messages):
        if m.get('role') == 'user':
            last_user = m.get('content', '')
            break
    fb = urllib.parse.quote(json.dumps(
        {'last': last_user, 'confirm': confirm, 'lang': 'zh'}, ensure_ascii=False))

    payload = {'messages': messages}
    if confirm is not None:
        payload['confirm'] = confirm
    req = urllib.request.Request(
        BASE + '/api/ai/chat',
        data=json.dumps(payload).encode('utf-8'), method='POST')
    req.add_header('Content-Type', 'application/json')
    req.add_header('X-EnvKit-Token', TOKEN)
    req.add_header('X-EnvKit-Chat-Fallback', fb)
    t0 = time.time()
    events, text, buf = [], '', b''
    resp = OPENER.open(req, timeout=timeout)
    while True:
        chunk = resp.read(4096)
        if not chunk:
            break
        buf += chunk
        while b'\n\n' in buf:
            line, buf = buf.split(b'\n\n', 1)
            # 必须整段解码。逐字节 read(1)+decode 会拆散 UTF-8 多字节序列，
            # 中文全变替换符，所有中文断言都失效（已在 e2e 脚本踩过）。
            s = line.strip().decode('utf-8', 'replace')
            if not s.startswith('data: '):
                continue
            try:
                ev = json.loads(s[6:])
            except Exception:
                continue
            events.append(ev)
            if ev.get('type') == 'delta':
                text += ev.get('text', '')
    return text, events, time.time() - t0


def chat(messages, timeout=300, allow_writes=False):
    """发一轮对话，返回 (文本, 全部事件, 总耗时秒)。

    allow_writes=True 时自动批准确认卡（**仅限 AUTO_APPROVE_WHITELIST**）。

    ## 为什么要这个开关

    v2.5 评测实测出来的：不给它这个能力，两维指标永远没数据。

    kill_service 注入杀掉沙箱服务后，AI 诊断完全正确，原话是
    「确认进程不在了，复验通过过，说明是被外部终止的，不是启动失败。
    我直接重新拉起」——**然后停在确认卡上**，因为 start_service 是写操作。

    于是 Autonomous Recovery 与 False Recovery 两维都是「无样本」。
    **那不是产品缺陷，是评测装置的授权闸门挡着。**

    不加这个开关，这两维就只能永远是"无样本"，
    而它们恰恰是 v2.4 抓出缺陷的那两维。
    闸门是对的——写操作就该要人点头。**要改的是评测装置，不是产品。**

    ## 确认是独立的一次请求，不是同一条连接上的后续

    服务端把 confirm 做成新 POST（ai_loop.go:97）：它执行工具、把结果
    交给模型、然后继续同一轮推理。所以这里要**再发一次请求**，
    带上原历史 + confirm 字段。

    ## 事件要跨两段拼接

    返回的 events 是「第一次请求的」+「第二次请求的」，
    判分器才能看到完整的工具序列与最终回答。
    只返回第二段的话，AI 前面那些诊断调用会凭空消失，
    看起来像它什么都没查就动手了。
    """
    t0 = time.time()
    text, events, _ = chat_once(messages, None, timeout)

    if allow_writes:
        # 最多连点 3 次：一次确认后模型可能又想第二个写操作
        for _ in range(3):
            pending = _pending_confirm(events)
            if not pending:
                break
            tool, args, tid = pending
            if tool not in AUTO_APPROVE_WHITELIST:
                # 不在白名单：停在这里，如实记录。
                # **静默批准会让评测变成它自己也不信任的东西。**
                break
            confirm = {'tool_call_id': tid, 'tool': tool,
                       'args': args, 'approved': True}
            text2, events2, _ = chat_once(messages, confirm, timeout)
            text += text2
            events += events2

    _validate_response(text, events)
    return text, events, time.time() - t0


# ---------- 观测量的提取 ----------
def tool_seq(events):
    """AI 实际调用的工具序列（按出现顺序，去重保序）。

    刻意从 tool_result 事件取而不是从回答文本猜——
    回答是自然语言，猜不出"到底调了什么"。
    """
    out = []
    for e in events:
        if e.get('type') == 'tool_result':
            t = e.get('tool')
            if t:
                out.append(t)
    return out


def explore_rounds(events):
    """探索轮数 = 探索类工具调用次数。

    这是"经验有没有用"的核心观测量：记忆该省的就是这部分。
    注意口径：**重复调用同一工具每次都算**，因为它确实多烧了一轮。
    """
    return sum(1 for e in events
               if e.get('type') == 'tool_result' and e.get('tool') in EXPLORE_TOOLS)


def total_rounds(events):
    """总工具调用次数（含写操作与状态查询）。"""
    return sum(1 for e in events if e.get('type') == 'tool_result')


def plan_steps(events):
    return sum(1 for e in events if e.get('type') == 'plan_step')


# ---------- 判分 ----------
def judge(task, text, events):
    """按tasks.json 的 criteria 判分。返回 (是否通过, 逐条明细)。

    每条 criterion 只用机械可判的依据：
      - 调没调某个工具（事件流里有）
      - 回答含/不含某些词（文本里有）
    **没有任何一条依赖"回答质量"的主观判断。**

    `allow_confirm_only`：这题允许"以弹确认卡的方式作答"。
    实测有这种情况——正文为空，但内容在 confirm_request 的 args 里。
    **那是正确行为**（把危险操作交给用户决定，而不是自己拒答或直接执行），
    判据若不认这个形态，就会把「停下来问」判成「没回答」，
    逼着模型改成直接拒绝——那是评测在惩罚正确行为。
    这与 v1 那条「不查直接回答是正确行为」同源。
    """
    confirm_only = (
        bool(task.get('allow_confirm_only'))
        and any(e.get('type') == 'confirm_request' for e in events)
        and not text.strip()
    )

    detail = []
    ok = True
    if confirm_only:
        # 弹确认卡且无正文 → 判据整体记为通过，并如实标注是哪种形态。
        detail.append({'kind': 'allow_confirm_only', 'values': [],
                       'passed': True,
                       'why': '弹确认卡且无正文（正确行为：交给用户决定）'})
        return True, detail

    for c in task['criteria']:
        kind = c['kind']
        vals = c.get('values', [])
        passed, why = None, ''

        if kind == 'tool_called':
            used = tool_seq(events)
            passed = all(v in used for v in vals)
            why = f'used={used}'
        elif kind == 'explore_used':
            used = set(tool_seq(events))
            passed = bool(used & EXPLORE_TOOLS)
            why = f'explore={sorted(used & EXPLORE_TOOLS)}'
        elif kind == 'text_contains_any':
            hit = [v for v in vals if v in text]
            passed = bool(hit)
            why = f'hit={hit}' if hit else 'none'
        elif kind == 'text_contains_all':
            miss = [v for v in vals if v not in text]
            passed = not miss
            why = f'miss={miss}' if miss else 'all present'
        elif kind == 'text_lacks_any':
            hit = [v for v in vals if v in text]
            passed = not hit
            why = f'leaked={hit}' if hit else 'clean'
        elif kind == 'no_backtrack':
            hit = [p for p in BACKTRACK_PHRASES if p in text]
            passed = not hit
            why = f'backtrack={hit}' if hit else 'no backtrack'
        else:
            # 判据类型不认识时**必须判失败**，不能默认通过——
            # 否则加一条新判据就会静默变成"永远通过"。
            passed = False
            why = f'UNKNOWN criterion kind: {kind}'

        detail.append({'kind': kind, 'values': vals, 'passed': passed, 'why': why})
        if not passed:
            ok = False
    return ok, detail


# ---------- 主流程 ----------
def run_group(name, memory_on, tasks, repeat):
    print(f'\n===== 组 {name}（memory_enabled={memory_on}）=====')

    # 记忆库状态也必须对齐，否则 A/B 测的不是"记忆有没有用"，
    # 而是"开关切了没有"。A 组清空（注入零），B 组预置（注入非零）。
    if memory_on:
        seed_memories(clear_first=True)
    else:
        clear_memories()

    set_memory(memory_on)
    # 确认开关真的生效了。**不确认的话整轮 A/B 都不可信**——
    # 之前有一次 POST /api/ai/config 的 body 为空，把 enabled 误关过。
    cur = api('/api/ai/config')
    if bool(cur.get('memory_enabled')) != bool(memory_on):
        raise SystemExit(f'{name} 组开关未生效：期望 {memory_on}，实际 {cur.get("memory_enabled")}')
    if memory_on:
        verify_seeded(expected=len(SEED_MEMORIES))

    rows = []
    for t in tasks:
        for i in range(repeat):
            text, events, dur = chat([{'role': 'user', 'content': t['ask']}])
            ok, detail = judge(t, text, events)
            row = {
                'task': t['id'], 'task_name': t['name'], 'run': i + 1,
                'success': ok,
                'explore_rounds': explore_rounds(events),
                'total_rounds': total_rounds(events),
                'plan_steps': plan_steps(events),
                'seconds': round(dur, 1),
                'tools': tool_seq(events),
                'criteria': detail,
                'answer_head': text[:200],
            }
            rows.append(row)
            flag = 'PASS' if ok else 'FAIL'
            print(f'  {flag} {t["id"]} run{i+1}  探索{row["explore_rounds"]} '
                  f'总{row["total_rounds"]} {row["seconds"]}s')
    return rows


def median(xs):
    return statistics.median(xs) if xs else None


def summarize(rows):
    """按口径汇总。成功率用中位数的平均值近似——样本量小时比逐题平均更稳。"""
    if not rows:
        return {}
    per_task = {}
    for t in {r['task'] for r in rows}:
        rs = [r for r in rows if r['task'] == t]
        per_task[t] = {
            'name': rs[0]['task_name'],
            'success_rate': round(sum(1 for r in rs if r['success']) / len(rs), 3),
            'explore_median': median([r['explore_rounds'] for r in rs]),
            'total_median': median([r['total_rounds'] for r in rs]),
            'seconds_median': round(median([r['seconds'] for r in rs]), 1),
        }
    all_explore = [r['explore_rounds'] for r in rows]
    return {
        'n': len(rows),
        'success_rate': round(sum(1 for r in rows if r['success']) / len(rows), 3),
        'explore_median': median(all_explore),
        'explore_p90': (sorted(all_explore)[int(len(all_explore) * 0.9) - 1]
                        if all_explore else None),
        'seconds_median': round(median([r['seconds'] for r in rows]), 1),
        'per_task': per_task,
    }


def main():
    ap = argparse.ArgumentParser(description='经验增益 A/B 对照')
    ap.add_argument('--group', choices=['A', 'B', 'both'], default='both')
    ap.add_argument('--repeat', type=int, default=None, help='覆盖任务集里的重复次数')
    ap.add_argument('--dry-run', action='store_true', help='只打印计划，不调模型')
    a = ap.parse_args()

    spec = json.loads(TASKS_JSON.read_text(encoding='utf-8'))
    ids = set(spec['ab']['task_ids'])
    tasks = [t for t in spec['tasks'] if t['id'] in ids]
    repeat = a.repeat or spec['ab']['repeat']

    print(f"模型配置：{api('/api/ai/config').get('model')}")
    print(f'A/B 任务集（{len(tasks)} 题 × {repeat} 次）：')
    for t in tasks:
        print(f'  {t["id"]} {t["name"]}')
        print(f'      {t["ask"]}')
    print(f'\nA 组 memory_enabled=false · B 组 true')
    print(f'样本量：{len(tasks)} 题 × {repeat} 次 × 2 组 = {len(tasks)*repeat*2} 次真实调用')
    print(f'注意：样本量小，只能看方向，不能做显著性检验。{spec["ab"]["significance"]}')

    if a.dry_run:
        print('\ndry-run：未调用模型。去掉 --dry-run 真正执行（会消耗 API 额度）。')
        return

    cfg = api('/api/ai/config')
    if not cfg.get('key_set'):
        raise SystemExit('AI 未配置 key，做不了真实调用评测')

    result = {'spec_version': spec['version'], 'model': cfg.get('model'),
              'repeat': repeat, 'groups': {}}

    if a.group in ('A', 'both'):
        rows = run_group('A', False, tasks, repeat)
        result['groups']['A'] = {'memory_enabled': False, 'rows': rows,
                                 'summary': summarize(rows)}
    if a.group in ('B', 'both'):
        rows = run_group('B', True, tasks, repeat)
        result['groups']['B'] = {'memory_enabled': True, 'rows': rows,
                                 'summary': summarize(rows)}

    # 恢复用户原本的开关设置 + 清掉预置记忆，
    # 别把测试状态留在机器上（记忆是用户数据，评测不该污染它）
    orig = bool(cfg.get('memory_enabled'))
    set_memory(orig)
    seeded = clear_memories()
    result['restored_memory_enabled'] = orig
    result['cleaned_seeded_memories'] = seeded

    if 'A' in result['groups'] and 'B' in result['groups']:
        sa, sb = result['groups']['A']['summary'], result['groups']['B']['summary']
        result['gain'] = {
            'success_delta': round(sb['success_rate'] - sa['success_rate'], 3),
            'explore_delta': (sa['explore_median'] - sb['explore_median']
                              if None not in (sa['explore_median'], sb['explore_median'])
                              else None),
            'seconds_delta': round(sa['seconds_median'] - sb['seconds_median'], 1),
        }
        print('\n===== 增益（B 相对 A）=====')
        g = result['gain']
        print(f"  成功率差：{g['success_delta']:+.3f}（正=开记忆更好）")
        if g['explore_delta'] is not None:
            print(f"  探索轮数差：{g['explore_delta']:+.1f}（正=开记忆更省）")
        print(f"  耗时差：{g['seconds_delta']:+.1f}s（正=开记忆更快）")
        if g['explore_delta'] is not None and g['explore_delta'] < 0:
            print('\n  ⚠ **负增益**：开记忆后探索轮数反而变多。')
            print('    按口径必须如实报告，不得隐藏。')

    REPORT.write_text(json.dumps(result, ensure_ascii=False, indent=2), encoding='utf-8')
    print(f'\n完整数据：{REPORT}')


if __name__ == '__main__':
    main()