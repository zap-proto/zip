# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package orders

# The order to stop, and why.
struct Cancel {
    # The receipt id the order was placed under.
    id     text @0
    # Said back to the customer, so write it for them.
    reason text @8
}

# Whether this service can take orders right now.
struct Health {
    # True once stock levels have been read at least once.
    ready bool @0
    # Seconds this process has been answering.
    since u64  @8
}

# What a customer asks for.
struct Order {
    # The catalogue number of the thing being bought.
    sku   text @0
    # How many of it, which is at least one.
    count u32  @8
    # Anything the picker needs to know.
    note  text @16
}

# One answer to a query, and where the next one starts.
struct Page {
    # The orders themselves, newest first.
    orders list<bytes> @0
    # How many matched, which is not how many are here.
    total  u64         @8
}

# Which orders to answer with.
struct Query {
    # Only orders for this catalogue number; empty for all of them.
    sku   text @0
    # At most this many, newest first.
    limit u32  @8
}

# What an order became, once the stock was there or was not.
struct Receipt {
    # What to cancel this order by. Opaque; do not read it.
    id     text @0
    # What it came to, in the smallest unit of the currency.
    cents  u64  @8
    # False when the stock ran out; nothing is queued.
    filled bool @16
}

interface orders {
    # Stop an order that has not shipped. An order that has shipped is refused.
    delete_orders_by_id(req: Cancel)
    # Say whether this service can take orders, and for how long it has been up.
    get_health() returns (rep: Health)
    # Answer with the orders for one catalogue number, newest first.
    get_orders(req: Query) returns (rep: Page)
    # Place an order, which is filled from stock or refused, never queued.
    # The receipt is the whole answer: an id to cancel it by, what it came to, and
    # whether the stock was there.
    post_orders(req: Order) returns (rep: Receipt)
}

# ---------------------------------------------------------------------
# 4 op(s) here. What follows is what this schema does not carry.
#
# blocked (1) — the op is absent; the field has no wire form:
#   post_orders_note  Note.body  Any  (any)
#
# opaque (1) — crosses, arrives without its name:
#   Page.orders  Order (list element)
