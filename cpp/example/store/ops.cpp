// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: BSD-3-Clause-Eco

#include "store/ops.hpp"

#include "store.zip.hpp"

namespace store {

Item Store::add(const NewItem& in) const {
    if (in.Name.empty()) throw zip::Error(400, "argument 'name' not given");
    Item out;
    out.ID = "item-1";
    out.Name = in.Name;
    out.Count = in.Count;
    return out;
}

Item Store::one(const Ask& in) const {
    if (in.ID.empty()) throw zip::Error(400, "argument 'id' not given");
    Item out;
    out.ID = in.ID;
    out.Name = "anvil";
    out.Count = 3;
    return out;
}

Item Store::change(const ItemPatch& in) const {
    Item out;
    out.ID = in.ID;
    out.Name = in.Name;
    out.Count = in.Count;
    return out;
}

Gone Store::drop(const Ask& in) const { return Gone{in.ID}; }

}  // namespace store

/// A store
///
/// Four ops that between them use every half of a request: a body, an address, a header, and a status that is not 200.
zip::App app("store", "v1.0.0");

void ops(store::Store* s) {
    zip::post(app, "/v1/items", &store::Store::add, s, zip::status(201), zip::tags("items"));
    zip::get(app, "/v1/items/:id", &store::Store::one, s, zip::tags("items"));
    zip::patch(app, "/v1/items/:id", &store::Store::change, s, zip::tags("items"));
    zip::remove(app, "/v1/items/:id", &store::Store::drop, s, zip::id("forget"), zip::tags("items"));
}
