"""Помощник для docs/demo/screenshots.sh.

  tty OUT CMD...                     запустить CMD с выводом в псевдотерминал
                                     (цвета включены, stdin пустой) и сохранить
                                     сырой ANSI-вывод в OUT; код выхода — как у CMD
  answer OUT TEXT CMD...             то же, но stdin тоже терминал: на вопрос
                                     «[y/N]» отвечает TEXT, как человек
  term OUT.html TITLE [--brief] [--drop T]... [--cut A B]... --cmd SHOWN FILE [--cmd SHOWN FILE]...
                                     окно терминала из ANSI-файлов, печатает
                                     высоту окна в пикселях; --cut A B заменяет
                                     строки от первой, содержащей A, до первой
                                     следующей, содержащей B (включительно), одной
                                     строкой «…»; --drop T убирает строки с T;
                                     --brief скрывает строки с путями артефактов
  sha FILE                           напечатать Plan SHA-256 из вывода run

Оформление меняет только вид: убирает перерисовку прогресс-бара, служебные
строки заглушки (MOCK:) и сокращает пути демо-каталога до ~/greeter.
"""
import fcntl
import html
import os
import pty
import re
import select
import struct
import subprocess
import sys
import termios

DEMO = os.environ.get("AI_TEAM_DEMO_DIR", "/tmp/ai-team-demo")
COLORS = {"31": "#ff7b72", "32": "#7ee2a8", "33": "#e5c07b", "34": "#79c0ff",
          "35": "#d2a8ff", "36": "#56d4dd"}


def run_tty(out, cmd, answer=None):
    master, slave = pty.openpty()
    fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 50, 120, 0, 0))
    stdin = slave if answer is not None else subprocess.DEVNULL
    proc = subprocess.Popen(cmd, stdin=stdin, stdout=slave, stderr=slave, close_fds=True)
    os.close(slave)
    buf = bytearray()
    while True:
        try:
            ready, _, _ = select.select([master], [], [], 0.2)
        except OSError:
            break
        if ready:
            try:
                chunk = os.read(master, 65536)
            except OSError:
                break
            if not chunk:
                break
            buf += chunk
            if answer is not None and b"[y/N]" in buf:
                os.write(master, answer.encode() + b"\n")
                answer = None
        elif proc.poll() is not None:
            break
    proc.wait()
    with open(out, "wb") as f:
        f.write(bytes(buf))
    return proc.returncode


def clean(text):
    text = text.replace("\r\n", "\n").replace("\r", "")
    text = re.sub(r"\x1b\[s.*?\x1b\[u", "", text, flags=re.S)
    for prefix in ("/private" + DEMO, DEMO):
        text = text.replace(prefix + "/greeter", "~/greeter").replace(prefix + "/origin.git", "~/origin.git")
    text = re.sub(r"(\.ai-team/worktrees/)[^/\s]+/", r"\1…/", text)
    return [line for line in text.split("\n") if not line.startswith("MOCK:")]


def cut(lines, cuts):
    for start, stop in cuts:
        out, i = [], 0
        while i < len(lines):
            plain = re.sub(r"\x1b\[[0-9;?]*[A-Za-z]", "", lines[i])
            if start in plain:
                j = i + 1
                while j < len(lines) and stop not in re.sub(r"\x1b\[[0-9;?]*[A-Za-z]", "", lines[j]):
                    j += 1
                out.append("\x1b[2m  …\x1b[0m")
                i = j + 1
                continue
            out.append(lines[i])
            i += 1
        lines = out
    return lines


ARTIFACT_LINE = re.compile(r"^\s+[→✓✗] \S+ \S+\(\d{4}-\d\d-\d\dT")


def brief(lines):
    plain = [re.sub(r"\x1b\[[0-9;?]*[A-Za-z]", "", line) for line in lines]
    return [line for line, p in zip(lines, plain) if not ARTIFACT_LINE.match(p)]


def to_html(lines):
    rows = []
    for line in lines:
        state = {"bold": False, "dim": False, "color": None}
        parts = []
        for piece in re.split(r"(\x1b\[[0-9;]*m)", line):
            m = re.fullmatch(r"\x1b\[([0-9;]*)m", piece)
            if m:
                for code in (m.group(1) or "0").split(";"):
                    if code in ("0", ""):
                        state = {"bold": False, "dim": False, "color": None}
                    elif code == "1":
                        state["bold"] = True
                    elif code == "2":
                        state["dim"] = True
                    elif code in COLORS:
                        state["color"] = COLORS[code]
                continue
            piece = re.sub(r"\x1b\[[0-9;?]*[A-Za-z]", "", piece)
            if not piece:
                continue
            style = ""
            if state["color"]:
                style += "color:%s;" % state["color"]
            if state["bold"]:
                style += "font-weight:700;"
            if state["dim"]:
                style += "opacity:.55;"
            text = html.escape(piece)
            parts.append('<span style="%s">%s</span>' % (style, text) if style else text)
        rows.append("".join(parts))
    while rows and not rows[-1].strip():
        rows.pop()
    return "\n".join(rows)


TERM_PAGE = """<!doctype html><html><head><meta charset="utf-8"><style>
body{margin:0;padding:24px;background:#e9efec;font-family:-apple-system,"Segoe UI",sans-serif;box-sizing:border-box;min-height:100vh;display:flex}
.win{flex:1;background:#0f1a18;border-radius:12px;box-shadow:0 18px 50px rgba(15,40,34,.28);overflow:hidden}
.bar{height:36px;display:flex;align-items:center;gap:8px;padding:0 14px;background:#172623}
.dot{width:12px;height:12px;border-radius:50%%}.t{margin-left:10px;color:#8ea39d;font-size:13px}
pre{margin:0;padding:18px 22px 22px;color:#d7e9e1;font:14px/1.55 "SF Mono",Menlo,Consolas,monospace;white-space:pre-wrap;word-break:break-all}
.ps{color:#7ee2a8;font-weight:700}.cmd{color:#fff;font-weight:600}
</style></head><body><div class="win"><div class="bar"><span class="dot" style="background:#ff5f56"></span><span class="dot" style="background:#ffbd2e"></span><span class="dot" style="background:#27c93f"></span><span class="t">%s</span></div><pre>%s</pre></div></body></html>"""

def main(argv):
    cmd = argv[0]
    if cmd == "tty":
        return run_tty(argv[1], argv[2:])
    if cmd == "answer":
        return run_tty(argv[1], argv[3:], answer=argv[2])
    if cmd == "sha":
        with open(argv[1], encoding="utf-8", errors="replace") as f:
            m = re.search(r"Plan SHA-256:\s*([0-9a-f]{64})", re.sub(r"\x1b\[[0-9;]*m", "", f.read()))
        if not m:
            return 1
        print(m.group(1))
        return 0
    if cmd == "term":
        out, title, rest = argv[1], argv[2], argv[3:]
        cuts, drops, body, short, total = [], [], "", False, 0
        while rest:
            if rest[0] == "--drop":
                drops.append(rest[1])
                rest = rest[2:]
            elif rest[0] == "--brief":
                short = True
                rest = rest[1:]
            elif rest[0] == "--cut":
                cuts.append((rest[1], rest[2]))
                rest = rest[3:]
            elif rest[0] == "--cmd":
                with open(rest[2], encoding="utf-8", errors="replace") as f:
                    lines = cut(clean(f.read()), cuts)
                if short:
                    lines = brief(lines)
                lines = [l for l in lines if not any(d in l for d in drops)]
                # Строки длиннее ширины окна переносятся: учитываем их в высоте.
                total += 3 + sum(1 + len(re.sub(r"\x1b\[[0-9;?]*[A-Za-z]", "", l)) // 118 for l in lines)
                body += '<span class="ps">%s</span> <span class="cmd">%s</span>\n%s\n\n' % (
                    html.escape(os.environ.get("PROMPT", "~/greeter $")), html.escape(rest[1]), to_html(lines))
                rest = rest[3:]
            else:
                raise SystemExit("unknown argument: " + rest[0])
        with open(out, "w", encoding="utf-8") as f:
            f.write(TERM_PAGE % (html.escape(title), body.rstrip()))
        print(80 + int(total * 21.7))
        return 0
    raise SystemExit(__doc__)


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
