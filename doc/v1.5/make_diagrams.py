#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""Generate tango v1.5 architecture diagrams (PIL only, CJK fonts).

Mimics the v1.4 diagram style (doc/v1.4/*.png): layered colored rounded-box
overview + two left-to-right flow diagrams. Renders at 2x then downscales with
LANCZOS for crisp anti-aliased edges. Output: doc/v1.5/{overview,upload-flow,
cfgsync-flow}.png
"""
import os
from PIL import Image, ImageDraw, ImageFont

HERE = os.path.dirname(os.path.abspath(__file__))
SS = 2  # supersample factor

# ---- fonts (Windows CJK) --------------------------------------------------
FONT_REG = r"C:\Windows\Fonts\msyh.ttc"
FONT_BLD = r"C:\Windows\Fonts\msyhbd.ttc"
_fc = {}


def font(px, bold=False):
    key = (px, bold)
    if key not in _fc:
        _fc[key] = ImageFont.truetype(FONT_BLD if bold else FONT_REG, int(px * SS))
    return _fc[key]


# ---- palette (echoes v1.4) ------------------------------------------------
C_ENTRY = ("#eef0f3", "#9aa3ad")
C_ROLE = ("#cfe2f3", "#5b8fb9")
C_ENGINE_NOTE = ("#e8eef7", "#7aa0c4")
C_ORCH = ("#fce5cd", "#e69138")
C_ROOT = ("#d9d2e9", "#8e7cc3")
C_SUB = ("#fff2cc", "#d6b656")
C_BASE = ("#efefef", "#b7b7b7")
C_GREEN = ("#d9ead3", "#6aa84f")
C_RED = ("#f4cccc", "#cc4125")
C_PURPLE = ("#d9d2e9", "#8e7cc3")
C_BLUE = ("#cfe2f3", "#5b8fb9")
INK = "#222222"
SUBINK = "#555555"
LINE = "#9aa3ad"


def new_canvas(w, h, bg="#ffffff"):
    img = Image.new("RGB", (int(w * SS), int(h * SS)), bg)
    return img, ImageDraw.Draw(img)


def finish(img, path):
    w, h = img.size
    img = img.resize((w // SS, h // SS), Image.LANCZOS)
    img.save(path)
    print("wrote", path, img.size)


def s(v):
    return int(v * SS)


def _wrap_center(d, cx, y, lines, fnt, fill, lh):
    for ln in lines:
        bb = d.textbbox((0, 0), ln, font=fnt)
        w = bb[2] - bb[0]
        d.text((s(cx) - w // 2, s(y)), ln, font=fnt, fill=fill)
        y += lh


def box(d, x, y, w, h, colors, title=None, subs=None,
        tpx=15, spx=10.5, radius=10, dashed=False, title_color=INK):
    fill, outline = colors
    xy = [s(x), s(y), s(x + w), s(y + h)]
    if dashed:
        d.rounded_rectangle(xy, radius=s(radius), fill=fill)
        _dashed_rrect(d, xy, s(radius), outline, s(2), dash=s(7), gap=s(5))
    else:
        d.rounded_rectangle(xy, radius=s(radius), fill=fill, outline=outline, width=s(1.6))
    cx = x + w / 2
    subs = subs or []
    th = tpx + 3 if title else 0
    sh = (spx + 4) * len(subs)
    cy = y + (h - (th + sh)) / 2
    if title:
        bb = d.textbbox((0, 0), title, font=font(tpx, bold=True))
        tw = bb[2] - bb[0]
        d.text((s(cx) - tw // 2, s(cy)), title, font=font(tpx, bold=True), fill=title_color)
        cy += th
    _wrap_center(d, cx, cy, subs, font(spx), SUBINK, s(spx + 4))


def _dashed_rrect(d, xy, r, color, width, dash, gap):
    x0, y0, x1, y1 = xy
    def dline(a, b, horiz):
        if horiz:
            x = a[0]
            while x < b[0]:
                d.line([x, a[1], min(x + dash, b[0]), a[1]], fill=color, width=width)
                x += dash + gap
        else:
            yv = a[1]
            while yv < b[1]:
                d.line([a[0], yv, a[0], min(yv + dash, b[1])], fill=color, width=width)
                yv += dash + gap
    dline((x0 + r, y0), (x1 - r, y0), True)
    dline((x0 + r, y1), (x1 - r, y1), True)
    dline((x0, y0 + r), (x0, y1 - r), False)
    dline((x1, y0 + r), (x1, y1 - r), False)


def arrow(d, x0, y0, x1, y1, color=LINE, width=2.0, head=8, dashed=False):
    import math
    X0, Y0, X1, Y1 = s(x0), s(y0), s(x1), s(y1)
    if dashed:
        ang = math.atan2(Y1 - Y0, X1 - X0)
        dist = math.hypot(X1 - X0, Y1 - Y0)
        dash, gap = s(7), s(5)
        t = 0
        while t < dist:
            xa = X0 + math.cos(ang) * t
            ya = Y0 + math.sin(ang) * t
            xb = X0 + math.cos(ang) * min(t + dash, dist)
            yb = Y0 + math.sin(ang) * min(t + dash, dist)
            d.line([xa, ya, xb, yb], fill=color, width=s(width))
            t += dash + gap
    else:
        d.line([X0, Y0, X1, Y1], fill=color, width=s(width))
    ang = math.atan2(Y1 - Y0, X1 - X0)
    hl = s(head)
    p1 = (X1, Y1)
    p2 = (X1 - hl * math.cos(ang - 0.45), Y1 - hl * math.sin(ang - 0.45))
    p3 = (X1 - hl * math.cos(ang + 0.45), Y1 - hl * math.sin(ang + 0.45))
    d.polygon([p1, p2, p3], fill=color)


def ctext(d, cx, y, text, px, fill=INK, bold=False):
    fnt = font(px, bold=bold)
    bb = d.textbbox((0, 0), text, font=fnt)
    d.text((s(cx) - (bb[2] - bb[0]) // 2, s(y)), text, font=fnt, fill=fill)


def ltext(d, x, y, text, px, fill=INK, bold=False):
    d.text((s(x), s(y)), text, font=font(px, bold=bold), fill=fill)


# ===========================================================================
# Diagram 1: overview
# ===========================================================================
def overview():
    W, H = 1400, 880
    img, d = new_canvas(W, H)
    ctext(d, W / 2, 22, "tango v1.5 架构总览 — 单一二进制 · 上报日志 + Data API + 运行时配置同步(cfgsync)", 21, INK, bold=True)
    ctext(d, W / 2, 52, "运行角色由配置键 role.mode 选定（daemon / gateway / cli）；api 是被内嵌的引擎库，不可派发", 12, SUBINK)

    LBL = 16
    GX0, GX1 = 168, 1380
    GW = GX1 - GX0

    def rowlabel(y, t1, t2=None):
        ltext(d, 24, y, t1, LBL, INK, bold=True)
        if t2:
            ltext(d, 24, y + 20, t2, 9.5, SUBINK)

    # 入口
    rowlabel(96, "入口")
    box(d, GX0, 84, GW, 46, C_ENTRY,
        title="main.go",
        subs=["config.Load → cfgtree.Tree → logging.Init → role.FromTree 取 mode → role.Get(role.mode).Run(ctx, tree)"],
        tpx=14, spx=10.5)

    # 角色层
    rowlabel(168, "角色层", "role/")
    rw = (GW - 3 * 18) / 4
    roles = [
        ("daemon", ["tailer → pipeline 常驻", "信号/fd看门狗/运行时指标"]),
        ("gateway", ["HTTP：/upload /ejson", "/sql /config + /healthz"]),
        ("cli", ["stdin 一次性", "upload/ejson/sql/config"]),
    ]
    for i, (t, subs) in enumerate(roles):
        box(d, GX0 + i * (rw + 18), 156, rw, 64, C_ROLE, title=t, subs=subs, tpx=15, spx=9.5)
    box(d, GX0 + 3 * (rw + 18), 156, rw, 64, C_ENGINE_NOTE,
        title="api.Engine", subs=["可复用引擎库（库角色）", "非 role.mode 派发"], tpx=14, spx=9.5, dashed=True)

    # api band
    box(d, GX0, 230, GW, 32, ("#eef3fa", "#7aa0c4"),
        subs=["api.Engine（被 gateway / cli / client 内嵌）：New / NewFromTree · Upload / Run · EJSON · SQL · EnsureIndexes · StartCfgsync · PublishConfig · Close"],
        spx=10.5)

    # 编排领域
    rowlabel(300, "编排领域")
    box(d, GX0, 288, GW, 56, C_ORCH,
        title="cfgsync — 运行时动态配置同步（embed daemon/gateway；写侧 gateway/cli/api 同核）", title_color="#7a4a10",
        subs=["读侧 Watcher：poll / changestream → observe → 单调版本守卫 → Registry.Apply → parser.SwapFilter 原子热替换（坏配置保留 last-good）",
              "写侧 Publish：校验(allowlist + 编译 filter) → dao.EJSON updateOne($set + $inc:version) upsert（DocumentDB 安全）→ _tango_config"],
        tpx=14, spx=10)

    # 引擎根包
    rowlabel(372, "引擎根包", "领域间只经此层")
    cw = (GW - 3 * 18) / 4
    roots = [("process", "三种上传策略唯一入口"), ("parser", "TA 解析 + 上报 filter"),
             ("source", "日志来源契约 + 门面"), ("dao", "MongoDB 持久化 + Data API")]
    for i, (t, sub) in enumerate(roots):
        box(d, GX0 + i * (cw + 18), 360, cw, 46, C_ROOT, title=t, subs=[sub], tpx=15, spx=9.5, title_color="#4b3b78")

    # 子包
    rowlabel(440, "子包", "领域内部实现")
    subs_rows = [
        ["single · batch", "pipeline · core"],
        ["talog · filter", "(Holder 原子热替换)"],
        ["httpbody · stdin", "tailer · taapi"],
        ["store · mongo", "ejson · sql"],
    ]
    for i, lines in enumerate(subs_rows):
        box(d, GX0 + i * (cw + 18), 428, cw, 58, C_SUB, subs=lines, spx=11)

    # arrows: roots -> subs (vertical), api band -> roots
    for i in range(4):
        cx = GX0 + i * (cw + 18) + cw / 2
        arrow(d, cx, 406, cx, 427, color=LINE, width=1.6, head=7)
    # entry -> roles
    arrow(d, W / 2, 130, W / 2, 155, color=LINE, width=1.8, head=7)
    # cfgsync hot-swap (dashed, up to role band)
    arrow(d, GX0 + 40, 288, GX0 + 40, 263, color="#e69138", width=1.8, head=7, dashed=True)
    ltext(d, GX0 + 52, 268, "热替换 live filter", 9, "#b06a16")

    # 基础
    rowlabel(516, "基础")
    bw = (GW - 2 * 18) / 3
    bases = [("logging", "全局日志（Init/Recover）"), ("cfgtree", "依赖中立配置载体(mapstructure)"),
             ("config", "viper：文件<env<flag → Tree")]
    for i, (t, sub) in enumerate(bases):
        box(d, GX0 + i * (bw + 18), 504, bw, 46, C_BASE, title=t, subs=[sub], tpx=14, spx=9.5)

    # collections band
    box(d, GX0, 566, GW, 38, ("#fbf0d9", "#d6b656"),
        title=None,
        subs=["MongoDB 集合：user · event · dead_letter · id_mapping · id_counter（身份自增）· _tango_config（cfgsync 中心文档）"],
        spx=11)

    # legend
    ly = 624
    box(d, GX0, ly, GW, 96, ("#fafafa", "#cccccc"), radius=8)
    ltext(d, GX0 + 18, ly + 12, "图例", 12, INK, bold=True)
    keys = [("角色层 role", C_ROLE), ("编排 cfgsync", C_ORCH), ("引擎根包", C_ROOT),
            ("子包(实现)", C_SUB), ("基础", C_BASE)]
    kx = GX0 + 90
    for t, (fill, outline) in keys:
        d.rounded_rectangle([s(kx), s(ly + 12), s(kx + 22), s(ly + 28)], radius=s(4), fill=fill, outline=outline, width=s(1.4))
        ltext(d, kx + 28, ly + 13, t, 10, SUBINK)
        kx += 28 + 9 * len(t) + 30
    ltext(d, GX0 + 18, ly + 42,
          "· 依赖向下：上层只经根包门面（process/parser/source/dao）调下层，领域之间不跨界 import 兄弟子包（架构铁律）。", 10.5, SUBINK)
    ltext(d, GX0 + 18, ly + 60,
          "· 橙色虚线箭头 = cfgsync 把中心文档的 filter 子树热替换进 daemon/gateway 的 live parser.filter（原子 Holder swap）。", 10.5, SUBINK)
    ltext(d, GX0 + 18, ly + 78,
          "· 相对 v1.4：移除 worker / backfill / taskqueue / fileupload / filebatch / UserSnapshot；remoteconfig 收敛为 cfgsync；SQL 改为注入式依赖外部 mongosql。", 10.5, "#a05a1a")

    finish(img, os.path.join(HERE, "overview.png"))


# ===========================================================================
# Diagram 2: upload-flow
# ===========================================================================
def upload_flow():
    W, H = 1400, 620
    img, d = new_canvas(W, H)
    ctext(d, W / 2, 22, "图 A · 单行上报数据流（v1.5）", 20, INK, bold=True)
    ctext(d, W / 2, 50, "三种策略 single / batch / pipeline 共享 core.Processor 的逐行规则；差异只在批量与并发编排", 12, SUBINK)

    # source
    box(d, 40, 200, 175, 96, C_PURPLE, title="Source", title_color="#4b3b78",
        subs=["tailer（文件·daemon）", "httpbody（请求体·gateway/api）", "stdin（控制台·cli）", "Run(ctx) <-chan string"], tpx=15, spx=9.5)

    # processor
    box(d, 250, 188, 230, 120, C_BLUE, title="core.Processor", title_color="#2f5a78",
        subs=["① ParseLine → *Record", "② Filter.Keep（include/exclude）", "③ Identity.Resolve → #user_id", "④ Category 分类 → 写模型", "逐行 panic recover 兜底"], tpx=15, spx=9.5)
    arrow(d, 216, 248, 249, 248, width=2.2)

    # branches
    bx = 545
    box(d, bx, 110, 360, 70, C_GREEN, title="track* → EventWriteModel", title_color="#3a6a28",
        subs=["track=$setOnInsert(幂等) · track_update=$set", "track_overwrite=Replace；均带 _ts 防回退"], tpx=13, spx=9.5)
    box(d, bx, 196, 360, 86, C_GREEN, title="user_* → UserWriteModel", title_color="#3a6a28",
        subs=["set/$inc/$push/$addToSet/$unset/del", "$max 守 meta 时戳；_ts 过滤防旧覆盖新", "（document-form，DocumentDB 安全）"], tpx=13, spx=9.5)
    box(d, bx, 298, 360, 56, C_RED, title="parse/identity 失败 · panic → DeadLetterModel", title_color="#8a2a18",
        subs=["dead_letter 集合 {_ts, line, error}"], tpx=12.5, spx=9.5)
    ctext(d, bx + 180, 372, "filter 命中 → 丢弃（不写任何集合，有意丢弃）", 11, "#a05a1a")

    arrow(d, 482, 230, bx - 2, 145, width=2.0)
    arrow(d, 482, 248, bx - 2, 238, width=2.0)
    arrow(d, 482, 268, bx - 2, 322, width=2.0)

    # collections
    cx = 960
    box(d, cx, 110, 175, 70, ("#fbf0d9", "#d6b656"), title="event", subs=["#uuid unique"], tpx=15, spx=9.5)
    box(d, cx, 196, 175, 86, ("#fbf0d9", "#d6b656"), title="user", subs=["#user_id unique"], tpx=15, spx=9.5)
    box(d, cx, 298, 175, 56, ("#fbf0d9", "#d6b656"), title="dead_letter", subs=["_ts"], tpx=14, spx=9.5)
    for yy, h in ((110, 70), (196, 86), (298, 56)):
        arrow(d, bx + 360, yy + h / 2, cx - 2, yy + h / 2, width=2.0)

    box(d, 1155, 150, 205, 200, ("#eef3fa", "#7aa0c4"), title="Store.BulkWrite", title_color="#2f5a78",
        subs=["无序 bulk + 指数退避重试", "(200ms→2s, MaxElapsedTime)", "", "E11000(_ts skip) 视为成功", "—— 不重试不报错", "", "WriteStats.Retries 计数"], tpx=14, spx=9.5)
    arrow(d, 1135, 240, 1154, 240, width=2.0)

    # bottom band
    box(d, 40, 440, 1320, 120, ("#fafafa", "#cccccc"), radius=8)
    ltext(d, 58, 452, "三种上传策略（process.mode）", 13, INK, bold=True)
    ltext(d, 58, 478, "· single  —— 逐行即时写（每行一次 BulkWrite）", 11, SUBINK)
    ltext(d, 58, 500, "· batch   —— 累积至 batchSize 同步 flush；EOF / ctx 取消时 flush 余量", 11, SUBINK)
    ltext(d, 58, 522, "· pipeline —— N worker + 用户亲和路由(account>distinct，FNV hash)；动态批阈值 + flush ticker；最终用 background ctx flush 不丢数据", 11, SUBINK)
    ltext(d, 720, 478, "daemon 强制 pipeline；gateway/cli/api 由 process.mode 三选一", 11, "#a05a1a")
    ltext(d, 720, 500, "正确性不依赖跨 worker 顺序：_ts 条件更新保证旧记录永不覆盖新记录", 11, "#a05a1a")
    ltext(d, 720, 522, "统计：TotalLines/ParsedOK/Parse|Identity|Write|FilterErrors/User|EventWrites/DeadLetters/Filtered", 11, "#a05a1a")

    finish(img, os.path.join(HERE, "upload-flow.png"))


# ===========================================================================
# Diagram 3: cfgsync-flow
# ===========================================================================
def cfgsync_flow():
    W, H = 1400, 640
    img, d = new_canvas(W, H)
    ctext(d, W / 2, 22, "图 B · cfgsync 读写同核（v1.5）", 20, INK, bold=True)
    ctext(d, W / 2, 50, "运行时动态配置同步：写侧三面同核 Publish，读侧 Watcher 经版本守卫热替换 live filter", 12, SUBINK)

    # write side faces
    box(d, 40, 96, 250, 44, C_ROLE, title="gateway POST /config", tpx=13)
    box(d, 40, 150, 250, 44, C_ROLE, title="cli function=config", tpx=13)
    box(d, 40, 204, 250, 44, C_ROLE, title="api.PublishConfig", tpx=13)

    box(d, 330, 120, 250, 104, C_ORCH, title="cfgsync.Publish", title_color="#7a4a10",
        subs=["剥 _id/version", "校验：allowlist + 编译 filter", "(off-allowlist / 不可编译 → 拒绝)"], tpx=15, spx=9.5)
    for yy in (118, 172, 226):
        arrow(d, 290, yy, 329, 172, width=1.8)

    box(d, 620, 130, 260, 84, C_BLUE, title="dao.EJSON updateOne", title_color="#2f5a78",
        subs=["$set + $inc:{version:1}, upsert", "（DocumentDB 安全，无 pipeline update）"], tpx=14, spx=9.5)
    arrow(d, 580, 172, 619, 172, width=2.0)

    # central doc
    box(d, 940, 120, 230, 104, ("#fbf0d9", "#d6b656"), title="_tango_config", tpx=16,
        subs=["{ _id: filter,", "  version: <单调 int>,", "  filter:{include,exclude} }"], spx=10)
    arrow(d, 880, 172, 939, 172, width=2.2)

    # read side container
    box(d, 40, 300, 1320, 210, ("#f3f7fc", "#9aa3ad"), radius=10)
    ltext(d, 58, 312, "读侧 Watcher（embed daemon / gateway；一次性 cli 与库 api 不订阅读侧）", 13, INK, bold=True)

    # backends -> observe
    box(d, 70, 350, 250, 120, C_ENGINE_NOTE, title="backend 触发", title_color="#2f5a78",
        subs=["启动收敛拉取一次", "poll tick（默认 5s）", "changestream 事件（亚秒）", "reconcile 兜底全量读(60s)"], tpx=14, spx=9.5)
    arrow(d, 1100, 172, 1100, 300, width=2.0, dashed=True)
    ltext(d, 1110, 230, "fetchDoc(findOne) / watch", 9.5, SUBINK)
    arrow(d, 195, 224, 195, 349, width=1.8, dashed=True)

    box(d, 360, 360, 250, 100, ("#fff2cc", "#d6b656"), title="observe(doc)", title_color="#7a4a10",
        subs=["nil/缺 version → no-op", "单调守卫：version>last？", "否 → 丢弃（不回退）"], tpx=14, spx=9.5)
    arrow(d, 322, 410, 359, 410, width=2.0)

    box(d, 650, 360, 240, 100, C_ORCH, title="Registry.Apply", title_color="#7a4a10",
        subs=["按子树路由到 applier", "allowlist 外 → 拒绝记 warn", "保留字 _id/version 跳过"], tpx=14, spx=9.5)
    arrow(d, 610, 410, 649, 410, width=2.0)
    ltext(d, 612, 388, "是", 10, "#3a6a28")

    box(d, 930, 360, 240, 100, C_GREEN, title="parser.SwapFilter", title_color="#3a6a28",
        subs=["编译新 filter → 原子 Holder swap", "编译失败 → 保留 last-good", "（坏配置打不挂）"], tpx=14, spx=9.5)
    arrow(d, 890, 410, 929, 410, width=2.0)

    box(d, 1200, 360, 130, 100, C_BLUE, title="live filter", title_color="#2f5a78",
        subs=["worker 热路径", "无锁读取"], tpx=14, spx=9.5)
    arrow(d, 1170, 410, 1199, 410, width=2.0)

    # safety model band
    box(d, 40, 530, 1320, 86, ("#fafafa", "#cccccc"), radius=8)
    ltext(d, 58, 542, "安全模型（非瞬时一致，而是）：有界陈旧 + 自愈 + 不回退 + 坏配置打不挂", 13, INK, bold=True)
    ltext(d, 58, 568, "启动收敛读 · 单调版本守卫(防回退/重放) · 校验后再换+保留 last-good · 先订阅后快照(消除 read↔subscribe TOCTOU) · 定时 reconcile 兜底", 11, SUBINK)
    ltext(d, 58, 590, "默认 allowlist 只放 parser.filter（顶多加 logging.level）；dao.mongo.* / role.mode / role.gateway.addr / cfgsync.* 自身绝不可远程覆盖", 11, "#a05a1a")

    finish(img, os.path.join(HERE, "cfgsync-flow.png"))


if __name__ == "__main__":
    overview()
    upload_flow()
    cfgsync_flow()
