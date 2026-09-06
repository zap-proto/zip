# SPDX-License-Identifier: BSD-3-Clause-Eco
"""A real service, declared in Python and projected everywhere from one file.

Nothing here is written for the schema. The types are the ones the handlers take
and answer with, the widths are the ones the values need, and the sentences are
the ones a person reading the code wants — `typing` and `__doc__` are read at run
time, so the declaration and its description are the same text.

Two operations are here to be measured rather than admired:

  - `note` takes a field typed `Any`, and is therefore ABSENT from the schema and
    named in its ledger. That is the point of the .zap projection: every other
    surface has a way of writing "something goes here" and carrying on, and this
    one does not, so an op nobody can generate a client for says so out loud.
  - `find` answers with a list of orders, which crosses exactly and arrives
    without its type name — the ledger says that too.

    python3 -m zip example.orders:app -o example/orders.zap
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Annotated, Any, Optional

import zip


@dataclass
class Order:
    """What a customer asks for."""

    sku: Annotated[str, "The catalogue number of the thing being bought."]
    count: Annotated[zip.u32, "How many of it, which is at least one."]
    note: Annotated[Optional[str], "Anything the picker needs to know."] = None


@dataclass
class Receipt:
    """What an order became, once the stock was there or was not."""

    id: Annotated[str, "What to cancel this order by. Opaque; do not read it."]
    cents: Annotated[zip.u64, "What it came to, in the smallest unit of the currency."]
    filled: Annotated[bool, "False when the stock ran out; nothing is queued."]


@dataclass
class Query:
    """Which orders to answer with."""

    sku: Annotated[str, "Only orders for this catalogue number; empty for all of them."]
    limit: Annotated[zip.u32, "At most this many, newest first."]


@dataclass
class Page:
    """One answer to a query, and where the next one starts."""

    orders: Annotated[list[Order], "The orders themselves, newest first."]
    total: Annotated[zip.u64, "How many matched, which is not how many are here."]


@dataclass
class Cancel:
    """The order to stop, and why."""

    id: Annotated[str, "The receipt id the order was placed under."]
    reason: Annotated[str, "Said back to the customer, so write it for them."]


@dataclass
class Health:
    """Whether this service can take orders right now."""

    ready: Annotated[bool, "True once stock levels have been read at least once."]
    since: Annotated[zip.u64, "Seconds this process has been answering."]


@dataclass
class Note:
    """A remark from another system about an order."""

    id: Annotated[str, "The receipt id the remark is about."]
    body: Annotated[Any, "Whatever that system sends, which is why this cannot cross."]


app = zip.App("orders")

# What the service knows, newest first. It is a list because this is an example
# and a list is the whole of what these five operations need; nothing about the
# declarations above changes when it becomes a table.
placed: list[tuple[str, Order]] = []
stock = 100
remarks: dict[str, list[Any]] = {}


@app.post("/v1/orders")
def place(order: Order) -> Receipt:
    """Place an order, which is filled from stock or refused, never queued.

    The receipt is the whole answer: an id to cancel it by, what it came to, and
    whether the stock was there.
    """
    receipt = Receipt(id=f"r{len(placed)}", cents=1200 * order.count,
                      filled=order.count <= stock)
    if receipt.filled:
        placed.insert(0, (receipt.id, order))
    return receipt


@app.get("/v1/orders")
def find(q: Query) -> Page:
    """Answer with the orders for one catalogue number, newest first."""
    matched = [o for _, o in placed if q.sku in ("", o.sku)]
    return Page(orders=matched[: q.limit], total=len(matched))


@app.delete("/v1/orders/{id}")
def cancel(c: Cancel) -> None:
    """Stop an order that has not shipped. An order that has shipped is refused."""
    placed[:] = [row for row in placed if row[0] != c.id]


@app.get("/v1/health")
def health() -> Health:
    """Say whether this service can take orders, and for how long it has been up."""
    return Health(ready=stock > 0, since=len(placed))


@app.post("/v1/orders/note")
def note(n: Note) -> None:
    """Record a remark another system made about an order."""
    remarks.setdefault(n.id, []).append(n.body)
