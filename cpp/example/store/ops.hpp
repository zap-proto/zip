// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: BSD-3-Clause-Eco
//
// A store, written to exercise the half of an op that a read-only service never
// reaches: a body, a path parameter, a header, a declared status and a declared
// id. It is the same service examples/store is in Go.

#pragma once

#include <cstdint>
#include <string>
#include <vector>

#include "zip/mark.hpp"
#include "zip/zip.hpp"

namespace store {

/// Item is one thing in the store.
struct Item {
    /// ID is what the store calls it.
    ZIP_JSON("id") std::string ID;
    /// Name is what a person calls it.
    ZIP_JSON("name") std::string Name;
    /// Count is how many there are.
    ZIP_JSON("count") std::uint32_t Count = 0;
    /// Tags are what it is filed under.
    ZIP_JSON("tags") std::vector<std::string> Tags;
};

/// NewItem is what a caller must say to put one in the store.
struct NewItem {
    /// Name is what to call it.
    ZIP_JSON("name") ZIP_REQUIRED std::string Name;
    /// Count is how many arrived.
    ZIP_JSON("count") std::uint32_t Count = 0;
    /// Tenant is who is asking, which the store reads from the request itself.
    ZIP_JSON("-") ZIP_HEADER("X-Tenant") ZIP_REQUIRED std::string Tenant;
};

/// ItemPatch is what a caller may change about one.
struct ItemPatch {
    /// ID is the item to change, which the address already names.
    ZIP_JSON("-") ZIP_URL("id") std::string ID;
    /// Name is what to call it now.
    ZIP_JSON("name") std::string Name;
    /// Count is how many there are now.
    ZIP_JSON("count") std::uint32_t Count = 0;
};

/// Ask names one item.
struct Ask {
    /// ID is the item to answer for.
    ZIP_JSON("id") ZIP_URL("id") ZIP_REQUIRED std::string ID;
};

/// Gone is what is left of an item that has been removed.
struct Gone {
    /// ID is what it was called.
    ZIP_JSON("id") std::string ID;
};

/// Store keeps items.
class Store {
  public:
    /// Add puts an item in the store and answers with what it became.
    ///
    /// Example: {"name": "anvil", "count": 3}
    /// Response: {"id": "item-1", "name": "anvil", "count": 3, "tags": []}
    Item add(const NewItem& in) const;

    /// One is the item that id names.
    ///
    /// Example: {"id": "item-1"}
    /// Response: {"id": "item-1", "name": "anvil", "count": 3, "tags": []}
    Item one(const Ask& in) const;

    /// Change edits an item and answers with what it is now.
    ///
    /// Example: {"name": "anvil", "count": 4}
    /// Response: {"id": "item-1", "name": "anvil", "count": 4, "tags": []}
    Item change(const ItemPatch& in) const;

    /// Drop removes an item, and says which one it was.
    ///
    /// Example: {"id": "item-1"}
    /// Response: {"id": "item-1"}
    Gone drop(const Ask& in) const;
};

}  // namespace store
