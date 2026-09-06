// SPDX-License-Identifier: BSD-3-Clause-Eco
//
// Every wire type, in the order most likely to expose a wrong offset.
//
// The layout rule is stated twice — once in Go, where the encoder speaks it, and
// once in C++, where this tool writes it down — so the only thing keeping the
// two together is a comparison. This is the shape that comparison is worth
// making over: a one-byte value between two eight-byte ones, a fixed run that
// aligns to 1 where everything else aligns to its width, and a list beside a
// narrow number.

#ifndef SHAPES_HPP
#define SHAPES_HPP

#include "../../zip.hpp"

#include <array>
#include <cstdint>
#include <string>
#include <vector>

/// A nested value, so the outer struct has one to carry.
struct inner {
  /// Anything at all.
  int32_t n;
};

/// One of everything, laid out awkwardly on purpose.
struct shapes {
  /// One byte, first, so everything after it has to be aligned.
  bool flag;
  /// Eight bytes, which must skip to 8 rather than sit at 1.
  uint64_t wide;
  /// One byte again, before a four-byte value.
  uint8_t small;
  /// Four bytes, which must skip to 20.
  float ratio;
  /// Two bytes, which fit at 24.
  int16_t narrow;
  /// Three bytes inline, which align to 1 and so sit at 26.
  std::array<uint8_t, 3> tag;
  /// Eight bytes, which must skip past 29 to 32.
  double exact;
  /// A run of narrow numbers.
  std::vector<int16_t> counts;
  /// Text, which is an offset and a length.
  std::string label;
  /// Bytes, likewise.
  std::vector<uint8_t> blob;
  /// A nested value, which crosses as a message inside a slot.
  inner one;
  /// A list of nested values.
  std::vector<inner> many;
  /// A signed byte, last, so the size has to round up.
  int8_t trailing;
};

/// Answers nothing anyone reads.
struct done {
  /// Whether it worked.
  bool ok;
};

/// Carries one of everything.
class ZIP_SERVICE shape {
 public:
  /// Take one of everything and say it arrived.
  done take(shapes req);
};

#endif
