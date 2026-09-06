# SPDX-License-Identifier: BSD-3-Clause-Eco
"""What a Python declaration is on the wire.

`typing` is the type source. A handler's request and reply are dataclasses, and
`get_type_hints(cls, include_extras=True)` answers with the resolved annotation
of every field — including the metadata `Annotated` carries, which is where a
field's own prose is written. Nothing is read from a comment and nothing is read
twice: the annotation IS the declaration, at run time, in this process.

# The layout is the contract

A field is an offset and a width. The offsets here are the ones zip derives for
the equivalent Go declaration — each slot aligned to its own width, a nested
value eight bytes, `bytes_fixed[N]` inline and aligned to one — because the .zap
this produces is read by a process that derives them again and refuses the file
where the two disagree. Reproducing that rule is the whole of what a front end
owes the wire; every other artifact is downstream of the file.

# Optionality is in the type

`Optional[str]` is `Union[str, None]`, which is a fact about the declaration and
not a tag beside it. Go reads the same fact from a pointer, and from a `validate`
struct tag for the document. Here there is one place to look and nothing to keep
in step.

# A refusal is an answer

`Any` names no type, a dict is a key set rather than a layout, and a set has no
order to lay out. None of those can be a slot, so this does not invent one: the
field is refused, by name and with a cause, and [zip.zap] reports it rather than
emitting a struct with the awkward field quietly dropped.
"""

from __future__ import annotations

import dataclasses
import types
from dataclasses import dataclass
from typing import Annotated, Any, NewType, Union, get_args, get_origin, get_type_hints

# The exact-width integers, and the narrow float. Python's `int` is unbounded and
# a slot is not, so a declaration that means u32 says u32; `int` alone is i64,
# which is what Go's `int` is on this wire.
u8 = NewType("u8", int)
u16 = NewType("u16", int)
u32 = NewType("u32", int)
u64 = NewType("u64", int)
i8 = NewType("i8", int)
i16 = NewType("i16", int)
i32 = NewType("i32", int)
i64 = NewType("i64", int)
f32 = NewType("f32", float)

_NEW = {u8: "u8", u16: "u16", u32: "u32", u64: "u64",
        i8: "i8", i16: "i16", i32: "i32", i64: "i64", f32: "f32"}

_PLAIN = {bool: "bool", int: "i64", float: "f64", str: "text", bytes: "bytes"}

_WIDTH = {"bool": 1, "u8": 1, "i8": 1, "u16": 2, "i16": 2,
          "u32": 4, "i32": 4, "f32": 4, "u64": 8, "i64": 8, "f64": 8,
          "text": 8, "bytes": 8}

# The causes, one string per reason, spelled as the Go projection spells them so
# that counting them across languages is one count.
MAP = "map"                 # no map in the IDL: a key set is not a layout
ANY = "any"                 # the annotation names no type at all
EMPTY = "no fields"         # nothing crosses, so there is no struct to declare
UNWIRABLE = "no wire form"  # a set, a tuple, a union, a list of lists
REACHES = "reaches one"     # every field is fine; one of them holds a type that is not


@dataclass(frozen=True)
class Fixed:
    """The marker that makes a bytes field inline and N wide: `Annotated[bytes,
    fixed(32)]`. An id is exactly the value that should say its width."""

    n: int


def fixed(n: int) -> Fixed:
    return Fixed(n)


@dataclass(frozen=True)
class Slot:
    """One field: a .zap type at a byte offset."""

    name: str          # the field's own spelling, which is its name on the wire
    type: str          # the .zap spelling — u64, text, list<u8>, bytes_fixed[32]
    offset: int
    width: int
    doc: str = ""      # the field's own prose, from Annotated
    elem: str = ""     # a list's element spelling
    opt: bool = False  # Optional: the value may be absent, and the slot is the same
    holds: Any = None  # the declaration a `bytes` slot actually carries
    holds_list: bool = False


@dataclass(frozen=True)
class Refusal:
    """One field that cannot be a slot, and why."""

    field: str
    py: str
    cause: str


@dataclass(frozen=True)
class Shape:
    """One declaration laid out: its slots, the size of its fixed area, and
    every field that could not be one.

    Both halves are the answer. A shape with refusals declares nothing — a
    struct emitted with the awkward field dropped is a contract the wire does
    not keep — and the refusals are the work list.
    """

    slots: tuple[Slot, ...] = ()
    size: int = 0
    refusals: tuple[Refusal, ...] = ()
    hollow: tuple[Slot, ...] = ()  # nested values whose own layout has no slots

    def ok(self) -> bool:
        return not self.refusals


class Unwirable(Exception):
    """Raised while reading one field's type; caught by [shape], which turns it
    into a [Refusal] naming the field."""

    def __init__(self, cause: str, py: str):
        super().__init__(cause)
        self.cause, self.py = cause, py


_shapes: dict[Any, Shape] = {}
_doing: set[Any] = set()


def declares(t: Any) -> bool:
    """Whether t is a declaration this wire can be asked about."""
    return dataclasses.is_dataclass(t) and isinstance(t, type)


def shape(t: Any) -> Shape:
    """The layout of one declaration.

    Fields in declaration order, each slot aligned to its own width, the fixed
    area rounded to eight. A declaration that reaches itself has no fixed size
    and is refused where it recurs.
    """
    if t in _shapes:
        return _shapes[t]
    if t in _doing:
        raise Unwirable(REACHES, pyname(t))
    _doing.add(t)
    try:
        sh = _build(t)
    finally:
        _doing.discard(t)
    _shapes[t] = sh
    return sh


def _build(t: Any) -> Shape:
    slots: list[Slot] = []
    bad: list[Refusal] = []
    hollow: list[Slot] = []
    off = 0
    hints = get_type_hints(t, include_extras=True)
    for f in dataclasses.fields(t):
        ann = hints.get(f.name, Any)
        try:
            slot = _slot(f.name, ann)
        except Unwirable as u:
            bad.append(Refusal(f"{pyname(t)}.{f.name}", u.py, u.cause))
            continue
        off = _align(off, _alignment(slot))
        slot = dataclasses.replace(slot, offset=off)
        off += slot.width
        slots.append(slot)
        if slot.holds is not None and not shape(slot.holds).slots:
            hollow.append(slot)
    return Shape(tuple(slots), _align(off, 8), tuple(bad), tuple(hollow))


def _alignment(s: Slot) -> int:
    """Where a slot may begin: its own width, except for inline bytes.

    Those bytes are inline and align to one, so a 32-byte id follows a u32 at
    offset 4 rather than at 32 — which is what the file on the other side reads.
    """
    return 1 if s.type.startswith("bytes_fixed[") else s.width


def _slot(name: str, ann: Any) -> Slot:
    """One field's slot, minus its offset."""
    ann, doc, fix, opt = _unwrap(ann)
    spelling, elem, holds, holds_list = _spell(ann, fix)
    width = fix.n if fix else _WIDTH.get(spelling, 8)
    return Slot(name=name, type=spelling, offset=0, width=width, doc=doc,
                elem=elem, opt=opt, holds=holds, holds_list=holds_list)


def _unwrap(ann: Any) -> tuple[Any, str, Fixed | None, bool]:
    """The annotation with Annotated and Optional peeled off, and what they said:
    the field's prose, its fixed width, and whether it may be absent.

    Both wrappers are peeled repeatedly and in either order, because
    `Annotated[Optional[str], "…"]` and `Optional[Annotated[str, "…"]]` are the
    same declaration written two ways.
    """
    doc, fix, opt = "", None, False
    while True:
        if get_origin(ann) is Annotated:
            args = get_args(ann)
            ann = args[0]
            for m in args[1:]:
                if isinstance(m, str) and not doc:
                    doc = m.strip()
                elif isinstance(m, Fixed):
                    fix = m
            continue
        if _optional(ann) is not None:
            ann, opt = _optional(ann), True
            continue
        return ann, doc, fix, opt


def _optional(ann: Any) -> Any:
    """The one type an Optional carries, or None if ann is not one.

    A union of two types that are both something is not optionality, it is two
    types where a slot holds one; that falls through to the refusal below.
    """
    if get_origin(ann) not in (Union, types.UnionType):
        return None
    rest = [a for a in get_args(ann) if a is not type(None)]
    return rest[0] if len(rest) == 1 and len(get_args(ann)) > len(rest) else None


def _spell(ann: Any, fix: Fixed | None) -> tuple[str, str, Any, bool]:
    """The .zap spelling of one type: (type, list element, the declaration a
    `bytes` slot carries, whether that declaration is the list's element)."""
    if fix is not None:
        if ann is not bytes:
            raise Unwirable(UNWIRABLE, pyname(ann))
        return f"bytes_fixed[{fix.n}]", "", None, False
    if ann in _NEW:
        return _NEW[ann], "", None, False
    if ann in _PLAIN:
        return _PLAIN[ann], "", None, False
    if ann is Any or ann is object:
        raise Unwirable(ANY, "Any")
    origin = get_origin(ann)
    if origin is list or ann is list:
        return _list(ann)
    if declares(ann):
        # A nested value crosses as a complete message in an {offset,length}
        # slot, so it is `bytes` here — the bytes are exact and the name is
        # lost. Its own layout is derived, because a value that cannot lay out
        # cannot be carried inside one that can.
        sh = shape(ann)
        if sh.refusals:
            raise Unwirable(REACHES, pyname(ann))
        return "bytes", "", ann, False
    if origin is dict or ann is dict:
        raise Unwirable(MAP, pyname(ann))
    raise Unwirable(UNWIRABLE, pyname(ann))


def _list(ann: Any) -> tuple[str, str, Any, bool]:
    args = get_args(ann)
    if not args:
        # A list of what? The declaration does not say, so there is no element
        # width, and a list whose element has no width has no wire form.
        raise Unwirable(ANY, "list")
    el, _, fix, _ = _unwrap(args[0])
    if get_origin(el) is list:
        # A list of lists has no wire form: the element of a list is a fixed
        # width, and a list is an offset and a length that live elsewhere.
        raise Unwirable(UNWIRABLE, pyname(ann))
    spelling, _, holds, _ = _spell(el, fix)
    return f"list<{spelling}>", spelling, holds, holds is not None


def _align(off: int, n: int) -> int:
    return (off + n - 1) & ~(n - 1)


def pyname(t: Any) -> str:
    """The type as a person would grep for it."""
    if t is None:
        return "None"
    return getattr(t, "__name__", None) or str(t).replace("typing.", "")


def prose(t: Any) -> str:
    """A declaration's own words: its docstring, minus the one dataclasses
    writes when the author wrote none."""
    doc = (t.__doc__ or "").strip()
    if doc.startswith(f"{t.__name__}(") and doc.endswith(")"):
        return ""  # dataclasses' generated signature line, which nobody wrote
    return doc
