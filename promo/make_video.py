"""Render the 30-second Relayward promo video (en / zh) with Pillow + ffmpeg.

Usage:  python promo/make_video.py [en|zh|all] [--preview]
Output: promo/out/relayward-promo-<lang>.mp4  (1280x720, 30 fps, soft synthesized BGM)
Requires: pip install pillow, ffmpeg on PATH, Windows fonts (Segoe UI / YaHei / Consolas).
"""
import subprocess
import sys
from pathlib import Path

from PIL import Image, ImageDraw, ImageFont

W, H, FPS, DUR, S = 1280, 720, 30, 30, 2  # S = supersampling factor
OUT = Path(__file__).parent / "out"

BG1, BG2 = (10, 16, 32), (18, 30, 58)
FG, MUTED = (236, 242, 255), (140, 156, 190)
ACC, ACC2 = (45, 212, 191), (96, 165, 250)
WARN, BAD, OK = (251, 191, 36), (248, 113, 113), (74, 222, 128)
CARD, CARD_LINE = (24, 38, 70), (48, 68, 112)

FONT_DIR = Path("C:/Windows/Fonts")
FONTS = {
    "en": (FONT_DIR / "segoeui.ttf", FONT_DIR / "segoeuib.ttf"),
    "zh": (FONT_DIR / "msyh.ttc", FONT_DIR / "msyhbd.ttc"),
}
MONO = FONT_DIR / "consola.ttf"

TEXT = {
    "en": {
        "t1": "Every app holds your mail key.",
        "s1": "Scattered keys. No logs. No limits.",
        "t2": "One gateway holds the only real key.",
        "s2": "Apps get their own SMTP password. You rotate the real key once.",
        "t3": "Control every sender",
        "cards": [("Per-app credentials", "own password, instant rotate"),
                  ("Rate limits", "hourly cap per app"),
                  ("Sender allow-list", "spoofed From is rejected"),
                  ("Full send log", "who sent what, to whom")],
        "st": {"sent": "sent", "limited": "rate_limited", "blocked": "rejected 550"},
        "t4": "Unsubscribe, built in",
        "s4": "RFC 8058 one-click headers + body footer. Per app, per recipient.",
        "mail_subj": "Your weekly digest",
        "mail_lines": ("Hi Alice,", "Here is what happened this week in your", "project: 12 commits, 3 merged PRs."),
        "footer": "Don't want these emails? Unsubscribe",
        "suppressed": "next send to bob@example.com: suppressed (app still gets 250)",
        "t5": "Deploy in minutes",
        "s5": "One static Go binary + one SQLite file. Zero dependencies.",
        "t6": "Relayward",
        "s6": "Self-hosted mail relay gateway",
        "chips": ("Single Go binary", "SQLite", "Zero dependencies"),
        "provider": "Mail provider", "gateway": "Relayward", "key": "REAL KEY", "pw": "app password",
    },
    "zh": {
        "t1": "每个程序都攥着你的发信 key",
        "s1": "key 到处都是，没有日志，没有限流",
        "t2": "一个网关，独占唯一的真实 key",
        "s2": "各程序用自己的 SMTP 密码；真实 key 只需在网关轮换一次",
        "t3": "每一个发信方都可控",
        "cards": [("按程序独立凭据", "各有密码，随时重置"),
                  ("发送限流", "每个程序每小时上限"),
                  ("发件地址白名单", "伪造 From 直接拒收"),
                  ("完整发送日志", "谁发给谁一清二楚")],
        "st": {"sent": "已发送", "limited": "已限流", "blocked": "已拒收 550"},
        "t4": "退订，原生内置",
        "s4": "RFC 8058 一键退订头 + 正文页脚，按程序、按收件人隔离",
        "mail_subj": "你的每周周报",
        "mail_lines": ("Alice 你好，", "这是你的项目本周动态：", "12 次提交，3 个已合并的 PR。"),
        "footer": "不想再收到此类邮件？点此退订",
        "suppressed": "再发 bob@example.com：已拦截（程序仍收到 250）",
        "t5": "几分钟完成部署",
        "s5": "单个静态 Go 二进制 + 一个 SQLite 文件，零依赖",
        "t6": "Relayward",
        "s6": "自托管的发信网关",
        "chips": ("单个 Go 二进制", "SQLite", "零依赖"),
        "provider": "邮件提供商", "gateway": "Relayward", "key": "真实 KEY", "pw": "程序密码",
    },
}

_font_cache = {}


def font(lang, size, bold=False, mono=False):
    key = (lang, size, bold, mono)
    if key not in _font_cache:
        path = MONO if mono else FONTS[lang][1 if bold else 0]
        _font_cache[key] = ImageFont.truetype(str(path), int(size * S))
    return _font_cache[key]


def clamp(x, a=0.0, b=1.0):
    return max(a, min(b, x))


def prog(t, start, dur):
    return clamp((t - start) / dur)


def ease(p):  # ease-out cubic
    return 1 - (1 - p) ** 3


def lerp(a, b, p):
    return a + (b - a) * p


def mix(c1, c2, p):
    return tuple(int(lerp(a, b, p)) for a, b in zip(c1, c2))


def fade(color, a, bg=BG1):
    return mix(bg, color, clamp(a))


_bg = None


def background():
    global _bg
    if _bg is None:
        img = Image.new("RGB", (W * S, H * S), BG1)
        px = ImageDraw.Draw(img)
        for y in range(H * S):
            px.line([(0, y), (W * S, y)], fill=mix(BG1, BG2, y / (H * S)))
        _bg = img
    return _bg.copy()


class Canvas:
    def __init__(self, lang):
        self.lang = lang
        self.img = background()
        self.d = ImageDraw.Draw(self.img)

    def rect(self, x, y, w, h, fill=None, outline=None, r=12, width=2):
        self.d.rounded_rectangle([x * S, y * S, (x + w) * S, (y + h) * S], radius=r * S,
                                 fill=fill, outline=outline, width=width * S)

    def text(self, x, y, s, size=28, color=FG, bold=False, mono=False, anchor="la"):
        self.d.text((x * S, y * S), s, font=font(self.lang, size, bold, mono), fill=color, anchor=anchor)

    def line(self, p1, p2, color, width=3):
        self.d.line([p1[0] * S, p1[1] * S, p2[0] * S, p2[1] * S], fill=color, width=width * S)

    def dot(self, x, y, r, color):
        self.d.ellipse([(x - r) * S, (y - r) * S, (x + r) * S, (y + r) * S], fill=color)

    def title(self, t, s, alpha, dy=0):
        self.text(W / 2, 62 + dy, t, 44, fade(FG, alpha), bold=True, anchor="mm")
        if s:
            self.text(W / 2, 110 + dy, s, 22, fade(MUTED, alpha), anchor="mm")

    def finish(self):
        return self.img.resize((W, H), Image.LANCZOS)


def lerp_pt(a, b, p):
    return (lerp(a[0], b[0], p), lerp(a[1], b[1], p))


# ---- scene 1+2: scattered keys -> gateway --------------------------------------------------
APPS = ["Gitea", "Kanboard", "Grafana", "Wiki", "CRM"]
APP_X, APP_W, APP_H = 80, 270, 54
APP_Y = [190 + i * 82 for i in range(5)]
PROV = (1000, 250, 210, 200)
GATE = (545, 285, 190, 130)


def draw_tag(c, x, y, label, color, alpha, w=None):
    if alpha <= 0.02:
        return
    w = w or (len(label) * (14 if c.lang == "zh" else 8.5) + 20)
    c.rect(x, y, w, 24, fill=fade(color, alpha * 0.18), outline=fade(color, alpha), r=6, width=1)
    c.text(x + w / 2, y + 12, label, 13, fade(color, alpha), bold=True, anchor="mm")


def scene_gateway(c, t, T):
    p2 = ease(prog(t, 4.6, 1.4))  # 0 = direct (chaos), 1 = via gateway
    appear = [ease(prog(t, 0.2 + i * 0.25, 0.5)) for i in range(5)]
    prov_a = ease(prog(t, 0.2, 0.6))
    c.rect(PROV[0], PROV[1], PROV[2], PROV[3], fill=fade(CARD, prov_a), outline=fade(ACC2, prov_a), r=16)
    c.text(PROV[0] + PROV[2] / 2, PROV[1] + 70, "@", 54, fade(ACC2, prov_a), bold=True, anchor="mm")
    c.text(PROV[0] + PROV[2] / 2, PROV[1] + 130, T["provider"], 22, fade(FG, prov_a), anchor="mm")
    if p2 > 0:
        gy = GATE[1] + (1 - p2) * 20
        c.rect(GATE[0], gy, GATE[2], GATE[3], fill=fade((20, 70, 78), p2), outline=fade(ACC, p2), r=18, width=3)
        c.text(GATE[0] + GATE[2] / 2, gy + 50, T["gateway"], 30, fade(ACC, p2), bold=True, anchor="mm")
        draw_tag(c, GATE[0] + GATE[2] / 2 - 40, gy + 80, T["key"], WARN, p2, w=80)
    pin = (PROV[0], PROV[1] + PROV[3] / 2)
    gin = (GATE[0], GATE[1] + GATE[3] / 2)
    gout = (GATE[0] + GATE[2], GATE[1] + GATE[3] / 2)
    for i, name in enumerate(APPS):
        a = appear[i]
        y = APP_Y[i]
        cy = y + APP_H / 2
        start = (APP_X + APP_W, cy)
        if a > 0:
            end = lerp_pt(start, pin, a)
            if p2 < 0.98:  # direct line to the provider fades into the gradient, then is gone
                bgc = mix(BG1, BG2, (start[1] + end[1]) / 2 / H)
                c.line(start, end, mix(bgc, BAD, a * (1 - p2) * 0.8), 2)
            if p2 > 0:
                c.line(start, lerp_pt(start, gin, p2), fade(ACC, p2 * 0.9), 2)
        c.rect(APP_X, y + (1 - a) * 14, APP_W, APP_H, fill=fade(CARD, a), outline=fade(CARD_LINE, a), r=10)
        c.text(APP_X + 18, y + APP_H / 2 + (1 - a) * 14, name, 22, fade(FG, a), bold=True, anchor="lm")
        draw_tag(c, APP_X + APP_W - 96, y + 15, T["key"], BAD, a * (1 - p2), w=80)
        draw_tag(c, APP_X + APP_W - 116, y + 15, T["pw"], ACC, a * p2, w=100)
        if t > 1.2 + i * 0.1:
            ph = (t * 0.55 + i * 0.21) % 1
            if p2 < 0.5:
                pt = lerp_pt(start, pin, ph)
                c.dot(pt[0], pt[1], 5, fade(BAD, a * (1 - p2 * 2)))
            else:
                pt = lerp_pt(start, gin, ph)
                c.dot(pt[0], pt[1], 5, fade(ACC, p2))
    if p2 > 0:
        c.line(gout, pin, fade(ACC, p2), 4)
        for k in range(3):
            pt = lerp_pt(gout, pin, (t * 0.6 + k / 3) % 1)
            c.dot(pt[0], pt[1], 6, fade(WARN, p2))
    tp1 = ease(prog(t, 0.1, 0.5)) * (1 - ease(prog(t, 4.2, 0.4)))
    tp2 = ease(prog(t, 4.7, 0.5))
    if tp1 > 0:
        c.title(T["t1"], T["s1"], tp1)
    if tp2 > 0:
        c.title(T["t2"], T["s2"], tp2, dy=(1 - tp2) * 10)


# ---- scene 3: control -----------------------------------------------------------------------
def scene_control(c, t, T):
    c.title(T["t3"], "", ease(prog(t, 0, 0.5)))
    cw, gap = 262, 24
    x0 = (W - (4 * cw + 3 * gap)) / 2
    glyph = ["key", "gauge", "shield", "list"]
    for i, (h, sub) in enumerate(T["cards"]):
        a = ease(prog(t, 0.3 + i * 0.25, 0.5))
        x, y = x0 + i * (cw + gap), 130 + (1 - a) * 24
        c.rect(x, y, cw, 150, fill=fade(CARD, a), outline=fade(CARD_LINE, a), r=14)
        cx, cy = x + 40, y + 44
        col = fade(ACC, a)
        if glyph[i] == "key":
            c.d.ellipse([(cx - 16) * S, (cy - 16) * S, (cx + 4) * S, (cy + 4) * S], outline=col, width=4 * S)
            c.line((cx + 2, cy + 2), (cx + 24, cy + 24), col, 4)
            c.line((cx + 14, cy + 14), (cx + 22, cy + 6), col, 4)
        elif glyph[i] == "gauge":
            c.d.arc([(cx - 22) * S, (cy - 18) * S, (cx + 22) * S, (cy + 26) * S], 180, 360, fill=col, width=4 * S)
            c.line((cx, cy + 4), (cx + 12, cy - 12), col, 4)
        elif glyph[i] == "shield":
            pts = [(cx - 20, cy - 18), (cx + 20, cy - 18), (cx + 20, cy + 4), (cx, cy + 24), (cx - 20, cy + 4)]
            c.d.polygon([(px * S, py * S) for px, py in pts], outline=col, width=4 * S)
            c.line((cx - 8, cy + 2), (cx - 2, cy + 9), col, 4)
            c.line((cx - 2, cy + 9), (cx + 10, cy - 8), col, 4)
        else:
            for k in range(3):
                c.line((cx - 20, cy - 12 + k * 14), (cx + 22, cy - 12 + k * 14), col, 4)
        c.text(x + 24, y + 90, h, 22, fade(FG, a), bold=True)
        c.text(x + 24, y + 126, sub, 15, fade(MUTED, a))
    ta = ease(prog(t, 1.6, 0.5))
    tx, ty, tw = 150, 330, W - 300
    c.rect(tx, ty, tw, 290, fill=fade((14, 24, 48), ta), outline=fade(CARD_LINE, ta), r=14)
    rows = [("gitea", "alice@example.com", "sent", OK),
            ("kanboard", "bob@example.com", "sent", OK),
            ("wiki", "carol@example.com", "limited", WARN),
            ("gitea", "dave@example.com", "sent", OK),
            ("spoof", "eve@example.com", "blocked", BAD)]
    for i, (app, to, st, col) in enumerate(rows):
        a = ease(prog(t, 2.0 + i * 0.55, 0.4))
        y = ty + 28 + i * 50 + (1 - a) * 10
        c.text(tx + 28, y + 14, app, 20, fade(ACC2, a), mono=True, anchor="lm")
        c.text(tx + 190, y + 14, to, 20, fade(FG, a), mono=True, anchor="lm")
        label = T["st"][st]
        pw = max(120, len(label) * (17 if c.lang == "zh" else 11) + 30)
        px = tx + tw - 40 - pw
        c.rect(px, y, pw, 30, fill=fade(col, a * 0.18), outline=fade(col, a), r=15, width=1)
        c.text(px + pw / 2, y + 15, label, 15, fade(col, a), bold=True, anchor="mm")


# ---- scene 4: unsubscribe -------------------------------------------------------------------
def scene_unsub(c, t, T):
    c.title(T["t4"], T["s4"], ease(prog(t, 0, 0.5)))
    a = ease(prog(t, 0.3, 0.6))
    mx, my, mw, mh = 90, 170, 520, 380
    paper = (244, 247, 252)
    c.rect(mx, my + (1 - a) * 20, mw, mh, fill=fade(paper, a), outline=fade(CARD_LINE, a), r=14)
    c.text(mx + 28, my + 40 + (1 - a) * 20, T["mail_subj"], 24, fade((40, 52, 80), a, paper), bold=True, anchor="lm")
    c.line((mx + 24, my + 70), (mx + mw - 24, my + 70), fade((200, 208, 224), a, paper), 2)
    for i, ln in enumerate(T["mail_lines"]):
        c.text(mx + 28, my + 110 + i * 34, ln, 19, fade((70, 84, 112), a, paper), anchor="lm")
    fa = ease(prog(t, 3.0, 0.6))
    c.line((mx + 24, my + 290), (mx + mw - 24, my + 290), fade((190, 198, 214), fa, paper), 1)
    c.text(mx + 28, my + 322, T["footer"], 16, fade((120, 130, 150), fa, paper), anchor="lm")
    if fa > 0.1:
        c.rect(mx + 20, my + 300, mw - 40, 46, outline=fade(ACC, fa), r=8, width=2)
    ha = ease(prog(t, 1.2, 0.6))
    hx, hy, hw, hh = 650, 170, 540, 190
    c.rect(hx, hy, hw, hh, fill=fade((9, 14, 28), ha), outline=fade(CARD_LINE, ha), r=14)
    lines = ["List-Unsubscribe:", "  <https://mail.example.com/u/TOKEN>",
             "List-Unsubscribe-Post:", "  List-Unsubscribe=One-Click"]
    shown = int(sum(len(s) for s in lines) * ease(prog(t, 1.6, 1.4)))
    for i, s in enumerate(lines):
        n = clamp(shown, 0, len(s))
        c.text(hx + 24, hy + 32 + i * 36, s[:int(n)], 19, fade(OK if i % 2 == 0 else FG, ha), mono=True, anchor="lm")
        shown -= len(s)
    pa = ease(prog(t, 4.2, 0.6))
    py = 410 + (1 - pa) * 14
    c.rect(hx, py, hw, 140, fill=fade(CARD, pa), outline=fade(WARN, pa), r=14)
    c.rect(hx + 24, py + 24, 120, 32, fill=fade(WARN, pa * 0.18), outline=fade(WARN, pa), r=16, width=1)
    c.text(hx + 84, py + 40, "suppressed", 16, fade(WARN, pa), bold=True, anchor="mm")
    c.text(hx + 24, py + 96, T["suppressed"], 17, fade(FG, pa), anchor="lm")


# ---- scene 5: deploy ------------------------------------------------------------------------
def scene_deploy(c, t, T):
    c.title(T["t5"], T["s5"], ease(prog(t, 0, 0.5)))
    ta = ease(prog(t, 0.2, 0.5))
    x, y, w, h = 150, 160, W - 300, 380
    c.rect(x, y, w, h, fill=fade((7, 11, 22), ta), outline=fade(CARD_LINE, ta), r=14)
    for i, col in enumerate((BAD, WARN, OK)):
        c.dot(x + 28 + i * 22, y + 24, 6, fade(col, ta))
    script = [
        ("$ make build", FG, 0.6),
        ("$ export UPSTREAM_KEY=********", FG, 1.3),
        ("$ ./bin/relayward serve -config config.yaml", FG, 2.0),
        ("INFO starting relayward smtp_listen=:587 admin_listen=:8081", MUTED, 3.2),
        ("INFO generated initial admin token (shown once)", MUTED, 3.5),
        ("> open http://127.0.0.1:8081/admin", ACC, 4.0),
    ]
    for i, (s, col, t0) in enumerate(script):
        n = int(len(s) * clamp((t - t0) / (0.6 if s.startswith("$") else 0.25)))
        if n > 0:
            c.text(x + 36, y + 70 + i * 48, s[:n], 21, col, mono=True, anchor="lm")
    if int(t * 2) % 2 == 0:
        c.rect(x + w - 60, y + h - 50, 14, 24, fill=ACC, r=2, width=1)


# ---- scene 6: closing -----------------------------------------------------------------------
def scene_close(c, t, T):
    a = ease(prog(t, 0.0, 0.6))
    cx, cy = W / 2, 200 - (1 - a) * 20
    c.dot(cx, cy, 44, fade(ACC, a))
    c.text(cx, cy, "R", 52, fade(BG1, a), bold=True, anchor="mm")
    c.text(W / 2, 320, T["t6"], 78, fade(FG, a), bold=True, anchor="mm")
    c.text(W / 2, 392, T["s6"], 30, fade(MUTED, ease(prog(t, 0.4, 0.6))), anchor="mm")
    chips = T["chips"]
    widths = [len(s) * (22 if c.lang == "zh" else 11) + 50 for s in chips]
    x = (W - (sum(widths) + 20 * (len(chips) - 1))) / 2
    for i, s in enumerate(chips):
        ca = ease(prog(t, 0.9 + i * 0.25, 0.5))
        c.rect(x, 440, widths[i], 46, fill=fade(CARD, ca), outline=fade(ACC, ca), r=23, width=1)
        c.text(x + widths[i] / 2, 463, s, 20, fade(FG, ca), anchor="mm")
        x += widths[i] + 20
    c.text(W / 2, 560, "github.com/biliblihuorong/relayward-mail", 26,
           fade(ACC2, ease(prog(t, 1.8, 0.6))), mono=True, anchor="mm")


SCENES = [(0, 10, scene_gateway), (10, 16, scene_control), (16, 22, scene_unsub),
          (22, 27, scene_deploy), (27, 30, scene_close)]


def render_frame(lang, f):
    T = TEXT[lang]
    t_all = f / FPS
    c = Canvas(lang)
    for s0, s1, fn in SCENES:
        if s0 <= t_all < s1:
            fn(c, t_all - s0, T)
            break
    c.dot(40, 36, 10, ACC)
    c.text(60, 36, "Relayward", 18, MUTED, bold=True, anchor="lm")
    c.rect(0, H - 6, W * (t_all / DUR), 6, fill=ACC, r=0, width=0)
    img = c.finish()
    for s0, _, _ in SCENES[1:]:  # short fade-in at each scene boundary
        d = t_all - s0
        if 0 <= d < 0.3:
            img = Image.blend(Image.new("RGB", (W, H), BG1), img, d / 0.3)
    return img


def make_bgm(path):
    """Soft synthesized ambient pad (Am - F - C - G, 7.5 s each), low volume, no external assets."""
    chords = [(220.00, 261.63, 329.63), (174.61, 220.00, 261.63),
              (261.63, 329.63, 392.00), (196.00, 246.94, 293.66)]
    inputs, labels = [], []
    n = 0
    for i, chord in enumerate(chords):
        for f in chord:
            inputs += ["-f", "lavfi", "-i", f"sine=f={f}:d=9:r=44100"]
            ms = int(i * 7500)
            labels.append(f"[{n}:a]afade=t=in:d=1.5,afade=t=out:st=7:d=2,adelay={ms}|{ms},volume=0.5[n{n}]")
            n += 1
    mix_in = "".join(f"[n{k}]" for k in range(n))
    graph = (";".join(labels) + f";{mix_in}amix=inputs={n}:normalize=0,lowpass=f=900,"
             "tremolo=f=0.25:d=0.35,aecho=0.8:0.6:700:0.3,volume=2.6,"
             f"atrim=0:{DUR},afade=t=in:d=1.5,afade=t=out:st={DUR - 2.5}:d=2.5[out]")
    subprocess.run(["ffmpeg", "-y", "-loglevel", "error", *inputs, "-filter_complex", graph,
                    "-map", "[out]", "-ac", "2", "-ar", "44100", str(path)], check=True)


def render(lang, preview=False):
    OUT.mkdir(parents=True, exist_ok=True)
    if preview:
        for sec in (3, 8, 13, 19, 25, 29):
            render_frame(lang, sec * FPS).save(OUT / f"preview-{lang}-{sec:02d}.png")
        return
    out = OUT / f"relayward-promo-{lang}.mp4"
    bgm = OUT / "bgm.wav"
    make_bgm(bgm)
    cmd = ["ffmpeg", "-y", "-loglevel", "error", "-f", "rawvideo", "-pix_fmt", "rgb24", "-s", f"{W}x{H}",
           "-r", str(FPS), "-i", "-", "-i", str(bgm), "-c:v", "libx264", "-pix_fmt", "yuv420p", "-crf", "20",
           "-preset", "medium", "-c:a", "aac", "-b:a", "128k", "-t", str(DUR),
           "-movflags", "+faststart", str(out)]
    p = subprocess.Popen(cmd, stdin=subprocess.PIPE)
    for f in range(FPS * DUR):
        p.stdin.write(render_frame(lang, f).tobytes())
    p.stdin.close()
    p.wait()
    print("wrote", out)


if __name__ == "__main__":
    args = [a for a in sys.argv[1:] if not a.startswith("--")]
    which = args[0] if args else "all"
    for lg in (["en", "zh"] if which == "all" else [which]):
        render(lg, preview="--preview" in sys.argv)
