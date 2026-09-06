// SPDX-License-Identifier: BSD-3-Clause-Eco
//
// The whole surface a C++ service declares against: one attribute.
//
// A service is a class whose member functions are its operations, and the
// attribute is what says so. Nothing else here is a zip type — the payloads are
// ordinary structs of ordinary members, because a payload that had to inherit
// from something would be a second declaration of the same fact.
//
// It is written where C++ puts a class attribute — after the class-key:
//
//	class ZIP_SERVICE accounts { ... };
//
// zipcpp reads it from the AST, so the marker and the thing it marks are one
// declaration. A compiler that does not know the attribute ignores it: an
// unknown attribute is ignorable by the standard, so a service header still
// compiles under gcc and under MSVC.

#ifndef ZIP_HPP
#define ZIP_HPP

#if defined(__clang__)
#define ZIP_SERVICE [[clang::annotate("zip.service")]]
#else
#define ZIP_SERVICE
#endif

#endif
