// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: BSD-3-Clause-Eco

#include <cstdio>
#include <string>
#include <vector>

#include "store/ops.hpp"
#include "zip/zip.hpp"

extern zip::App app;
void ops(store::Store* s);

int main(int argc, char** argv) {
    store::Store store;
    ops(&store);
    std::vector<std::string> addrs;
    for (int i = 1; i < argc; ++i) addrs.emplace_back(argv[i]);
    if (addrs.empty()) addrs = {":9654", "http://:8081"};
    std::fprintf(stderr, "store: %zu ops\n", app.routes().size());
    app.listen(addrs);
    return 0;
}
