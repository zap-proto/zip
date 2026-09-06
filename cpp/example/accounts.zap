# Generated from C++ declarations. Do not edit.
# Every struct and method below is derived from one service's member
# functions, and every offset from the layout the op-call plane encodes
# against.

package accounts

# One account, as the ledger holds it.
struct account {
    # The id the ledger issued. Stable for the life of the account.
    id       text @0
    # Balance in the smallest unit of the account's currency.
    cents    i64  @8
    # The currency, as ISO 4217, upper case.
    currency text @16
    # Where the account stands.
    state    u8   @24
}

# A movement to post against one account.
struct entry {
    # The account to post against.
    account   text  @0
    # How much to move, in the smallest unit. A debit is negative.
    cents     i64   @8
    # What the statement shows beside the amount.
    memo      text  @16
    # A reference the caller carries through untouched. The ledger reads none of it.
    reference bytes @24
}

# Which account to read.
struct lookup {
    # The id to read.
    id text @0
}

# One page of accounts.
struct page {
    # The accounts on this page, in id order.
    accounts list<bytes> @0
    # How many accounts exist in total, ignoring the window.
    total    u32         @8
}

# What posting an entry produced.
struct receipt {
    # The ledger sequence the entry landed at. Rises by one per posted entry.
    seq   u64             @0
    # The account as it stands after the entry applied.
    moved bytes           @8
    # The hash the ledger sealed the entry under.
    seal  bytes_fixed[32] @16
}

# Where to start reading the account list, and how far to read.
struct window {
    # Skip this many accounts.
    offset     u32  @0
    # Read at most this many.
    limit      u16  @4
    # Read from the highest id down instead of the lowest id up.
    descending bool @6
}

# The ledger's accounts: what they hold, and what moves them.
interface accounts {
    # Read a page of accounts, lowest id first unless the window says otherwise.
    list_(req: window) returns (rep: page)
    # Post one movement and answer the account it moved.
    # The sequence in the receipt is the ledger's own order, which is what a
    # caller reconciles against; the seal is over the entry as it was written.
    post(req: entry) returns (rep: receipt)
    # Read one account by id.
    # An id nobody opened reads as a closed account with a zero balance, so a
    # caller never has to tell "absent" from "empty" by the shape of the answer.
    read(req: lookup) returns (rep: account)
}

# ---------------------------------------------------------------------
# 3 op(s) here. What follows is what this schema does not carry.
#
# opaque (2) — crosses, arrives without its name:
#   page.accounts  account (list element)
#   receipt.moved  account
#
# coded (1) — needs a generated codec; the reflective one refuses:
#   receipt.seal  bytes_fixed[32]
#
# renamed (1) — spelled differently here than on every other surface:
#   list  ->  list_
