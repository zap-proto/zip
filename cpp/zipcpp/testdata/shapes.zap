# Generated from C++ declarations. Do not edit.
# Every struct and method below is derived from one service's member
# functions, and every offset from the layout the op-call plane encodes
# against.

package shapes

# Answers nothing anyone reads.
struct done {
    # Whether it worked.
    ok bool @0
}

# One of everything, laid out awkwardly on purpose.
struct shapes {
    # One byte, first, so everything after it has to be aligned.
    flag     bool           @0
    # Eight bytes, which must skip to 8 rather than sit at 1.
    wide     u64            @8
    # One byte again, before a four-byte value.
    small    u8             @16
    # Four bytes, which must skip to 20.
    ratio    f32            @20
    # Two bytes, which fit at 24.
    narrow   i16            @24
    # Three bytes inline, which align to 1 and so sit at 26.
    tag      bytes_fixed[3] @26
    # Eight bytes, which must skip past 29 to 32.
    exact    f64            @32
    # A run of narrow numbers.
    counts   list<i16>      @40
    # Text, which is an offset and a length.
    label    text           @48
    # Bytes, likewise.
    blob     bytes          @56
    # A nested value, which crosses as a message inside a slot.
    one      bytes          @64
    # A list of nested values.
    many     list<bytes>    @72
    # A signed byte, last, so the size has to round up.
    trailing i8             @80
}

# Carries one of everything.
interface shape {
    # Take one of everything and say it arrived.
    take(req: shapes) returns (rep: done)
}

# ---------------------------------------------------------------------
# 1 op(s) here. What follows is what this schema does not carry.
#
# opaque (2) — crosses, arrives without its name:
#   shapes.many  inner (list element)
#   shapes.one  inner
#
# coded (1) — needs a generated codec; the reflective one refuses:
#   shapes.tag  bytes_fixed[3]
