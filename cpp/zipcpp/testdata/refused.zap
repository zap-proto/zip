# Generated from C++ declarations. Do not edit.
# Every struct and method below is derived from one service's member
# functions, and every offset from the layout the op-call plane encodes
# against.

package refused

# One number, which crosses.
struct fine {
    # The number.
    n i64 @0
}

# Everything that cannot cross, and two things that can.
interface refused {
    # Takes nothing and answers a number.
    bare() returns (rep: fine)
    # Takes a number and answers nothing.
    sink(req: fine)
}

# ---------------------------------------------------------------------
# 2 op(s) here. What follows is what this schema does not carry.
#
# blocked (8) — the op is absent; the field has no wire form:
#   borrowed  borrowed.who  const char *  (const char * has no wire form)
#   derived  derived  base  (a base class has no wire form; declare it as a member)
#   keyed  keyed.by  std::map<std::string, int32_t>  (std::map<std::string, int32_t> has no wire form)
#   loop  loop.next  std::vector<loop>  (loop contains itself, so it has no fixed width)
#   nested  nested.rows  std::vector<std::vector<int32_t> >  (a list of lists has no wire form; wrap the inner one in a struct)
#   optional  maybe.when  std::optional<int32_t>  (std::optional<int32_t> may be absent, and the schema has no way to say so)
#   two  (parameters)  2 parameters  (a method carries at most one struct in each direction)
#   wide  wide.four  std::array<int32_t, 4>  (an array of int has no wire form; use a vector)
