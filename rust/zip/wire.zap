# The ZAP-HTTP frame: HTTP request and response semantics on the ZAP wire.
#
# These are the offsets zap-proto/http writes, so a Rust service and a Go one
# answer each other's frames. The accessors that read them are generated from
# this file; nothing in the runtime spells a byte offset by hand.
#
# Headers ride the frame as a count-prefixed run of length-prefixed name and
# value pairs — a repeated name is a repeated pair, which is what a multi-value
# header already is, so nothing has to model a list.

package wire

struct Request {
    Method  text  @0
    Target  text  @8
    Proto   text  @16
    Headers bytes @24
    Body    bytes @32
    Trailer bytes @40
}

struct Response {
    Status  u16   @0
    Reason  text  @8
    Proto   text  @16
    Headers bytes @24
    Body    bytes @32
    Trailer bytes @40
}
