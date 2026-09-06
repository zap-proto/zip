# SPDX-License-Identifier: BSD-3-Clause-Eco
"""Declaring an operation.

One entry. `@app.post("/orders")` over an annotated function records the address
and the two declarations it carries, and that one record is what the schema, and
through it every projection, is derived from. There is nothing else to write and
nowhere else to write it: the annotation is the type, the docstring is the prose,
and neither is restated in a decorator argument.

# What a handler may look like

A method carries one declaration per direction, so a handler takes one argument
and returns one value, each a dataclass — or neither, for an operation that
carries nothing that way. Anything else is refused where it is declared rather
than at the schema, because the author is standing right there:

    @app.post("/orders")
    def place(order: Order) -> Receipt:
        '''Place an order. It is filled from stock or refused, never queued.'''

The refusal is the whole reason to read the signature eagerly. A handler whose
reply is unannotated has no reply this can name, and the alternative to saying so
now is an SDK method returning `{}` that nobody notices until a peer reads it.
"""

from __future__ import annotations

import dataclasses
import inspect
from dataclasses import dataclass
from typing import Any, Callable, get_type_hints

from . import name as nm
from . import wire


class Refused(Exception):
    """A declaration this cannot record, raised where it is written."""


@dataclass(frozen=True)
class Op:
    """One declared operation: where it is addressed, what it carries, and what
    it says about itself."""

    method: str
    path: str
    id: str
    request: Any  # the declaration it takes, or None
    reply: Any    # the declaration it answers with, or None
    doc: str
    fn: Callable

    def __call__(self, *a, **kw):
        return self.fn(*a, **kw)


class App:
    """A service: its name, and the operations it declares.

    The name is the interface a schema declares it as. One app is one service,
    which is the same split the schema makes — an interface's method ordinals
    are positional, so several services written as one interface would renumber
    each other's methods.
    """

    def __init__(self, name: str = ""):
        self.name = name
        self._ops: list[Op] = []

    def ops(self) -> list[Op]:
        return list(self._ops)

    def get(self, path: str):
        return self._verb("GET", path)

    def post(self, path: str):
        return self._verb("POST", path)

    def put(self, path: str):
        return self._verb("PUT", path)

    def patch(self, path: str):
        return self._verb("PATCH", path)

    def delete(self, path: str):
        return self._verb("DELETE", path)

    def head(self, path: str):
        return self._verb("HEAD", path)

    def _verb(self, method: str, path: str):
        def declare(fn: Callable) -> Op:
            op = _read(method, path, fn)
            if any(o.id == op.id for o in self._ops):
                raise Refused(
                    f"{op.id} is declared twice: every projection keys on the id, so "
                    f"the second takes the first's place in the SDK, the tool list and "
                    f"the document")
            self._ops.append(op)
            return op

        return declare


def _read(method: str, path: str, fn: Callable) -> Op:
    """One operation, read from the function that implements it.

    `get_type_hints` resolves the annotations — including the metadata
    `Annotated` carries, which is where a field's prose is — and `__doc__` is the
    operation's own. Both are here at run time, which is why this needs no pass
    over the source and no second file to keep in step with it.
    """
    where = f"{fn.__module__}.{fn.__qualname__}"
    hints = get_type_hints(fn, include_extras=True)
    params = list(inspect.signature(fn).parameters.values())
    if len(params) > 1:
        raise Refused(
            f"{where} takes {len(params)} arguments; a method carries one declaration "
            f"per direction, so a handler takes the request and nothing else")

    request = None
    if params:
        p = params[0]
        if p.name not in hints:
            raise Refused(f"{where} does not say what {p.name} is; the annotation is the type")
        request = _payload(where, "takes", hints[p.name])
    if "return" not in hints:
        raise Refused(
            f"{where} does not say what it answers; annotate the reply, or `-> None` "
            f"for an operation that answers nothing")
    reply = _payload(where, "answers with", hints["return"])
    return Op(method, path, nm.op(method, path), request, reply,
              inspect.cleandoc(fn.__doc__ or ""), fn)


def _payload(where: str, verb: str, t: Any) -> Any:
    """One direction's declaration, or None for a direction that carries
    nothing."""
    if t is None or t is type(None):
        return None
    if not wire.declares(t):
        raise Refused(
            f"{where} {verb} {wire.pyname(t)}, which declares no fields; a method "
            f"carries a dataclass in each direction it carries anything")
    return t
