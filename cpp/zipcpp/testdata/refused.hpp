// SPDX-License-Identifier: BSD-3-Clause-Eco
//
// Every way a declaration can fail to cross, in one file.
//
// A projection that quietly narrowed any of these — an optional crossing as its
// own type and arriving stated as required, a map crossing as nothing — would
// publish a contract the wire does not keep. Each one is refused by name, and
// the operation that reaches it is absent from the schema rather than present
// and wrong. The two that DO cross are here to prove the refusals are not a
// blanket: a whole service is not lost to one bad member.

#ifndef REFUSED_HPP
#define REFUSED_HPP

#include "../../zip.hpp"

#include <array>
#include <cstdint>
#include <map>
#include <optional>
#include <string>
#include <vector>

/// A value that may be absent, which the schema has no way to say.
struct maybe {
  /// Present or not — and the schema can only state a type, never its absence.
  std::optional<int32_t> when;
};

/// A keyed collection, which this wire does not carry.
struct keyed {
  /// Keys to values, with no wire form.
  std::map<std::string, int32_t> by;
};

/// A borrowed address, which is a pointer into this process.
struct borrowed {
  /// Whatever it points at is not in the message.
  const char* who;
};

/// A sequence of sequences, which has nowhere to put the inner lengths.
struct nested {
  /// A list whose elements are themselves lists.
  std::vector<std::vector<int32_t>> rows;
};

/// A fixed run of something wider than a byte.
struct wide {
  /// Four integers inline, which this wire does not carry inline.
  std::array<int32_t, 4> four;
};

/// A struct that reaches itself, and so has no fixed width.
struct loop {
  /// The next one, forever.
  std::vector<loop> next;
};

/// A struct that inherits, so half its members are declared elsewhere.
struct base {
  /// A member of the base.
  int32_t n;
};
struct derived : base {
  /// A member of the derived.
  int32_t m;
};

/// One number, which crosses.
struct fine {
  /// The number.
  int64_t n;
};

/// Everything that cannot cross, and two things that can.
class ZIP_SERVICE refused {
 public:
  /// Takes an optional.
  fine optional(maybe req);
  /// Takes a map.
  fine keyed(struct keyed req);
  /// Takes a pointer.
  fine borrowed(struct borrowed req);
  /// Takes a list of lists.
  fine nested(struct nested req);
  /// Takes a fixed run of integers.
  fine wide(struct wide req);
  /// Takes a struct that contains itself.
  fine loop(struct loop req);
  /// Takes a struct with a base class.
  fine derived(struct derived req);
  /// Takes two payloads, which a method cannot.
  fine two(fine a, fine b);
  /// Takes nothing and answers a number.
  fine bare();
  /// Takes a number and answers nothing.
  void sink(fine req);
};

#endif
