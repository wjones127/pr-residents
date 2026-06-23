"""Minimal YAML reader for the pr-residents config subset.

We deliberately avoid a PyYAML dependency: the remote routine sandbox has no
guaranteed pip access (PEP 668), so pr-sync stays stdlib-only. This parser
handles exactly the block-YAML subset our config files use:

  - `key: scalar`            scalars: str, int, bool, null, and inline `[]`
  - `key:` + indented block  (nested map or list)
  - `- scalar`               list of scalars
  - `- key: value` ...       list of maps (e.g. escalation.yml rules)
  - `#` full-line and trailing inline comments

It is intentionally strict about structure and not a general YAML engine.
"""

from __future__ import annotations

import re
from typing import Any, NamedTuple

_INLINE_COMMENT = re.compile(r"\s+#.*$")


class _Line(NamedTuple):
    indent: int
    text: str


def _tokenize(src: str) -> list[_Line]:
    lines: list[_Line] = []
    for raw in src.splitlines():
        stripped = raw.strip()
        if not stripped or stripped.startswith("#"):
            continue
        text = _INLINE_COMMENT.sub("", raw).rstrip()
        indent = len(text) - len(text.lstrip(" "))
        lines.append(_Line(indent, text.strip()))
    return lines


def _scalar(token: str) -> Any:
    token = token.strip()
    if (token.startswith('"') and token.endswith('"')) or (
        token.startswith("'") and token.endswith("'")
    ):
        return token[1:-1]
    if token in ("[]",):
        return []
    if token in ("", "null", "~"):
        return None
    if token in ("true", "True"):
        return True
    if token in ("false", "False"):
        return False
    if re.fullmatch(r"-?\d+", token):
        return int(token)
    return token


class _Parser:
    def __init__(self, lines: list[_Line]):
        self.lines = lines
        self.pos = 0

    def _peek(self) -> _Line | None:
        return self.lines[self.pos] if self.pos < len(self.lines) else None

    def _is_list_item(self, text: str) -> bool:
        return text == "-" or text.startswith("- ")

    def parse_block(self, indent: int) -> Any:
        head = self._peek()
        if head is None or head.indent != indent:
            return None
        if self._is_list_item(head.text):
            return self._parse_list(indent)
        return self._parse_map(indent)

    def _parse_nested(self, parent_indent: int) -> Any:
        nxt = self._peek()
        if nxt is None or nxt.indent <= parent_indent:
            return None
        return self.parse_block(nxt.indent)

    def _parse_list(self, indent: int) -> list:
        out: list = []
        while True:
            it = self._peek()
            if it is None or it.indent != indent or not self._is_list_item(it.text):
                break
            rest = it.text[1:].strip()
            self.pos += 1
            if rest == "":
                out.append(self._parse_nested(indent))
            elif ":" in rest:
                nxt = self._peek()
                sib_indent = nxt.indent if (nxt and nxt.indent > indent) else indent + 2
                out.append(self._parse_map(sib_indent, injected=rest))
            else:
                out.append(_scalar(rest))
        return out

    def _parse_map(self, indent: int, injected: str | None = None) -> dict:
        out: dict = {}
        if injected is not None:
            self._consume_kv(injected, out, indent)
        while True:
            it = self._peek()
            if it is None or it.indent != indent or self._is_list_item(it.text):
                break
            self.pos += 1
            self._consume_kv(it.text, out, indent)
        return out

    def _consume_kv(self, line: str, out: dict, indent: int) -> None:
        key, _, val = line.partition(":")
        key = key.strip()
        val = val.strip()
        if val == "":
            out[key] = self._parse_nested(indent)
        else:
            out[key] = _scalar(val)


def loads(src: str) -> Any:
    parser = _Parser(_tokenize(src))
    lines = parser.lines
    if not lines:
        return {}
    return parser.parse_block(lines[0].indent)


def load_file(path: str) -> Any:
    with open(path, "r", encoding="utf-8") as fh:
        return loads(fh.read())
