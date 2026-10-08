# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package stream

struct Count {
    n        u32 @0
    every_ms u64 @8
}

struct Text {
    text_ text @0
}

interface stream {
    # Count streams n ticks as server-sent events, then [DONE], and reports how
    # many it sent in the x-sent trailer.
    post_count(req: Count)
    # Echo answers the text it was sent.
    post_echo(req: Text) returns (rep: Text)
    # Relay answers the body it was sent, each chunk as it arrives, under the
    # content type it was sent with.
    post_relay()
}
