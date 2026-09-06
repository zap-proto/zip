# SPDX-License-Identifier: BSD-3-Clause-Eco
"""The .zap schema a Python service declares — the pivot, and the only
projection that can refuse.

Everything a service is projected onto is computed from this file: the OpenAPI
document, the MCP tool list, the CLI, the SDKs, the docs pages. None of them is
written here, and none of them may be written here. They exist once, downstream,
and read this file; a second OpenAPI emitter written for Python would be a second
dialect of one document, which is exactly the drift the pivot exists to end.

So the whole job is to write this file correctly. Correct means two things a
reader can check:

  - the offsets are the ones the process on the other side derives, because the
    reader derives them again and refuses the file where the two disagree;
  - a Python service and a Go service declaring the same operations write the
    same bytes, because the names, the order and the layout are one rule each
    and not one per language.

# The refusal

Every other projection can describe anything. JSON Schema has `{}`, GraphQL has
a JSON scalar, the CLI has a string flag. Each is a way of writing "I do not
know what this is" and carrying on. This has no such word: a field IS an offset
and a width. So `Any` does not become `{}` here — the op is absent from the
schema and named in the ledger below it, with the field and the cause, which is
the one answer that leaves nobody a plausible-looking file to trust.
"""

from __future__ import annotations

from dataclasses import dataclass, field

from . import name as nm
from . import wire


@dataclass
class Field:
    name: str
    type: str
    offset: int
    doc: str = ""


@dataclass
class Struct:
    name: str
    fields: list[Field]
    size: int
    doc: str = ""


@dataclass
class Method:
    name: str
    request: str
    reply: str
    doc: str = ""


@dataclass
class Interface:
    name: str
    methods: list[Method] = field(default_factory=list)


@dataclass
class Gap:
    """One op the IDL cannot express, and the reason."""

    op: str
    field: str
    py: str
    cause: str


@dataclass
class Opacity:
    """One field whose bytes cross exactly and whose type name is lost."""

    struct: str
    field: str
    py: str
    lst: bool = False


@dataclass
class Loss:
    """One field that is part of the declaration and carries nothing."""

    struct: str
    field: str
    py: str
    cause: str = "empty message"


@dataclass
class Coded:
    """One field the schema states correctly that needs a generated codec."""

    struct: str
    field: str
    type: str


@dataclass
class Rename:
    """One op whose id the .zap lexer cannot spell."""

    op: str
    method: str


@dataclass
class Schema:
    """One .zap file: the structs the ops reach, the interfaces they form, and
    what does not cross."""

    package: str
    structs: list[Struct] = field(default_factory=list)
    interfaces: list[Interface] = field(default_factory=list)
    gaps: list[Gap] = field(default_factory=list)
    renamed: list[Rename] = field(default_factory=list)
    opaque: list[Opacity] = field(default_factory=list)
    dropped: list[Loss] = field(default_factory=list)
    coded: list[Coded] = field(default_factory=list)

    def ops(self) -> int:
        return sum(len(i.methods) for i in self.interfaces)

    def blocked(self) -> int:
        """How many distinct ops are absent. len(gaps) counts reasons, and one
        op can have several."""
        return len({g.op for g in self.gaps})

    def __str__(self) -> str:
        return _render(self)


def schema(package: str, *apps) -> Schema:
    """The .zap file the apps' typed ops describe.

    One interface per app and one struct namespace across all of them. An
    interface is a declared service and its method ordinals are positional, so a
    fleet written as one interface would renumber every service's methods when
    any service gained one. Types are the opposite: a declaration is the same
    declaration wherever it is reached, so it is named once and referred to.
    """
    s = Schema(package=package)
    e = _Emitter(s)
    for i, app in enumerate(apps):
        ops = sorted(app.ops(), key=lambda o: o.id)
        iname = app.name or (package if len(apps) == 1 else f"app{i}")
        iface = Interface(nm.idl(iname))
        e.iface, e.claimed = iface, {o.id for o in ops if nm.idl(o.id) == o.id}
        for op in ops:
            e.method(op)
        if iface.methods:
            s.interfaces.append(iface)
    s.structs.sort(key=lambda x: x.name)
    s.interfaces.sort(key=lambda x: x.name)
    s.gaps.sort(key=lambda g: (g.op, g.field))
    s.opaque.sort(key=lambda o: (o.struct, o.field))
    s.dropped.sort(key=lambda d: (d.struct, d.field))
    s.coded.sort(key=lambda c: (c.struct, c.field))
    return s


class _Emitter:
    def __init__(self, s: Schema):
        self.s = s
        self.named: dict[object, str] = {}  # declaration -> its schema name, "" for refused
        self.taken: set[str] = set()
        self.iface: Interface | None = None
        self.claimed: set[str] = set()
        self.op = ""

    def method(self, op) -> None:
        """One op, as a method. An op whose request or reply cannot be expressed
        contributes gaps and no method: a method whose payload is a struct that
        was never declared is a dangling reference, not a schema."""
        self.op = op.id
        req, req_ok = self.payload(op.request)
        rep, rep_ok = self.payload(op.reply)
        if not (req_ok and rep_ok):
            return
        self.iface.methods.append(
            Method(self.method_name(op.id), req, rep, op.doc))

    def method_name(self, oid: str) -> str:
        """The op's own id, spelled the way the .zap lexer accepts.

        An op that can spell its own name keeps it, which is what stops a folded
        neighbour taking a name its owner could have had. One that cannot is
        recorded rather than quietly folded: a name that differs on one surface
        out of seven is what a reader of the other six will not think to check.
        """
        if nm.idl(oid) == oid:
            return oid
        base = nm.idl(oid)
        out, n = base, 2
        while out in self.claimed:
            out, n = base + str(n), n + 1
        self.claimed.add(out)
        self.s.renamed.append(Rename(oid, out))
        return out

    def payload(self, t) -> tuple[str, bool]:
        """The struct one direction carries, or "" for a direction that carries
        nothing. The bool is whether it could be expressed at all."""
        if t is None:
            return "", True
        sh = wire.shape(t)
        if sh.ok() and not sh.slots:
            return "", True  # a declaration with nothing in it is the same absence
        n = self.define(t)
        return (n, True) if n else ("", False)

    def define(self, t) -> str:
        """Declare t and answer with its name, or "" when it cannot be declared."""
        if t in self.named:
            if not self.named[t]:
                # A declaration is refused once and blamed again for every op
                # that reaches it, because every one of them is what cannot cross.
                self.gap(wire.pyname(t), wire.pyname(t), wire.REACHES)
            return self.named[t]

        sh = wire.shape(t)
        if not sh.ok():
            self.named[t] = ""
            for r in sh.refusals:
                self.gap(r.field, r.py, r.cause)
            return ""
        if not sh.slots:
            self.named[t] = ""
            self.gap(wire.pyname(t), wire.pyname(t), wire.EMPTY)
            return ""

        n = self.name(t)
        self.named[t] = n
        self.taken.add(n)

        fields = []
        for slot in sh.slots:
            fields.append(Field(nm.idl(slot.name), slot.type, slot.offset, slot.doc))
            if slot.holds is not None:
                self.s.opaque.append(
                    Opacity(n, slot.name, wire.pyname(slot.holds), slot.holds_list))
            if slot.type.startswith("bytes_fixed[") or slot.elem.startswith("bytes_fixed["):
                self.s.coded.append(Coded(n, slot.name, slot.type))
        for slot in sh.hollow:
            self.s.dropped.append(Loss(n, slot.name, wire.pyname(slot.holds)))
        self.s.structs.append(Struct(n, fields, sh.size, wire.prose(t)))
        return n

    def name(self, t) -> str:
        """The .zap name for t: its own name, qualified by its module when a
        different declaration already holds that name, then by an ordinal."""
        base = nm.idl(wire.pyname(t))
        if base in ("", "_"):
            base = nm.idl(self.op) + "_anon"
        if base not in self.taken:
            return base
        mod = getattr(t, "__module__", "")
        if mod:
            base = nm.idl(mod.rsplit(".", 1)[-1]) + "_" + base
        out, n = base, 2
        while out in self.taken:
            out, n = base + str(n), n + 1
        return out

    def gap(self, fld: str, py: str, cause: str) -> None:
        self.s.gaps.append(Gap(self.op, fld, py, cause))


def _lines(doc: str) -> list[str]:
    """A doc comment as comment lines, dropping blanks so a paragraph break does
    not become a bare '#'."""
    return [ln.strip() for ln in doc.split("\n") if ln.strip()] if doc else []


def _render(s: Schema) -> str:
    b: list[str] = [
        "# Generated from typed ops. Do not edit.\n",
        "# Every struct and method below is derived from one op's In and Out, and\n",
        "# every offset from the layout the op-call plane encodes against.\n\n",
        f"package {s.package}\n\n",
    ]
    for st in s.structs:
        for ln in _lines(st.doc):
            b.append(f"# {ln}\n")
        b.append(f"struct {st.name} {{\n")
        nw = max((len(f.name) for f in st.fields), default=0)
        tw = max((len(f.type) for f in st.fields), default=0)
        for f in st.fields:
            for ln in _lines(f.doc):
                b.append(f"    # {ln}\n")
            b.append(f"    {f.name:<{nw}} {f.type:<{tw}} @{f.offset}\n")
        b.append("}\n\n")
    for i in s.interfaces:
        b.append(f"interface {i.name} {{\n")
        for m in i.methods:
            for ln in _lines(m.doc):
                b.append(f"    # {ln}\n")
            b.append(f"    {m.name}(")
            if m.request:
                b.append(f"req: {m.request}")
            b.append(")")
            if m.reply:
                b.append(f" returns (rep: {m.reply})")
            b.append("\n")
        b.append("}\n\n")
    b.append(_ledger(s))
    return "".join(b).rstrip("\n") + "\n"


def _ledger(s: Schema) -> str:
    """What the schema above does not carry.

    It lives in the same file rather than beside it because a second artifact is
    a second thing to keep in step, and the point is that a reader learns the
    cost in the same breath as the contract. A schema listing only what crosses
    would be true and would still mislead.
    """
    if not (s.gaps or s.dropped or s.opaque or s.coded or s.renamed):
        return ""
    b = ["# ---------------------------------------------------------------------\n",
         f"# {s.ops()} op(s) here. What follows is what this schema does not carry.\n"]
    if s.dropped:
        b.append(f"#\n# dropped ({len(s.dropped)}) — the value does not cross, and nothing fails:\n")
        b += [f"#   {d.struct}.{d.field}  {d.py}  ({d.cause})\n" for d in s.dropped]
    if s.gaps:
        b.append(f"#\n# blocked ({len(s.gaps)}) — the op is absent; the field has no wire form:\n")
        b += [f"#   {g.op}  {g.field}  {g.py}  ({g.cause})\n" for g in s.gaps]
    if s.opaque:
        b.append(f"#\n# opaque ({len(s.opaque)}) — crosses, arrives without its name:\n")
        b += [f"#   {o.struct}.{o.field}  {o.py}{' (list element)' if o.lst else ''}\n"
              for o in s.opaque]
    if s.coded:
        b.append(f"#\n# coded ({len(s.coded)}) — needs a generated codec; the reflective one refuses:\n")
        b += [f"#   {c.struct}.{c.field}  {c.type}\n" for c in s.coded]
    if s.renamed:
        b.append(f"#\n# renamed ({len(s.renamed)}) — spelled differently here than on every other surface:\n")
        b += [f"#   {r.op}  ->  {r.method}\n" for r in s.renamed]
    return "".join(b)
