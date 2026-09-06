# SPDX-License-Identifier: BSD-3-Clause-Eco
"""Names, and the three alphabets a name has to survive.

An operation is addressed at a method and a path, spelled in a schema as an
identifier, and read back by people. Those are three alphabets and one name, so
the rules that reduce a name from one to the next live here together rather than
beside each caller that needs one — which is how a document and a schema come to
call one operation two things.

Every rule here is the Go one, character for character: `ID` is openapi.go's,
`idl` and `ident` are zapschema.go's. That is not politeness. The .zap file is
where a Python service and a Go service declaring the same operations have to
meet, and they meet under a name — so a second opinion about how to spell one is
a second service.
"""

_KEEP = "-."


def op(method: str, path: str) -> str:
    """The operation's name, derived from its method and its absolute path.

    '_' encodes '/', so a character that also folded to '_' would collide with a
    path boundary: '-' and '.' are kept, which is what keeps /v1/pricing-policy
    and /v1/pricing/policy two names. A parameter contributes `by_<name>`, so
    /v1/a/{b} and /v1/a/b stay apart too. The leading v1 is dropped because every
    address carries it and so it tells nothing apart.
    """
    out = [method.lower()]
    stars = 0
    first = True
    for seg in path.split("/"):
        if not seg:
            continue
        if first:
            first = False
            if seg == "v1":
                continue
        by = ""
        if seg in ("*", "+"):
            stars += 1
            seg, by = f"wildcard{stars}", "by_"
        elif seg.startswith("{"):
            seg, by = seg.strip("{}"), "by_"
        elif seg.startswith(":"):
            seg, by = seg[1:], "by_"
        out.append("_" + by + _sanitize(seg))
    return "".join(out)


def _sanitize(s: str) -> str:
    """A path segment reduced to the characters legal in an operation id."""
    return "".join(c if (c.isascii() and (c.isalnum() or c in _KEEP)) else "_" for c in s.lower())


# The words the .zap grammar already spends. A struct called `text` parses as
# the scalar wherever it is named, so the field silently changes type; a struct
# called `list` does not parse at all.
RESERVED = frozenset(
    "bool u8 u16 u32 u64 i8 i16 i32 i64 f32 f64 text bytes bytes_fixed list "
    "package struct interface type returns".split()
)


def idl(s: str) -> str:
    """s as a name the .zap lexer accepts and its parser will not read as
    something else."""
    out = ident(s)
    return out + "_" if out in RESERVED else out


def ident(s: str) -> str:
    """s reduced to what the .zap lexer accepts: a letter or '_' first, then
    letters, digits and '_'."""
    out = "".join(
        c if (c.isascii() and (c.isalpha() or c == "_" or (c.isdigit() and i > 0))) else "_"
        for i, c in enumerate(s)
    )
    if not out:
        return "_"
    return "_" + out if out[0].isdigit() else out
