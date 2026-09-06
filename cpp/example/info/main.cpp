// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: BSD-3-Clause-Eco
//
// The info service, served: ZAP on :9653 and HTTP on :8080, the same ops over
// both. There is no Go in this program.

#include <cstdio>
#include <string>
#include <vector>

#include "info/ops.hpp"
#include "zip/zip.hpp"

extern zip::App app;
void ops(info::Info* i);

int main(int argc, char** argv) {
    info::Info info;
    ops(&info);

    std::vector<std::string> addrs;
    for (int i = 1; i < argc; ++i) addrs.emplace_back(argv[i]);
    if (addrs.empty()) addrs = {":9653", "http://:8080"};
    std::fprintf(stderr, "info: %zu ops on", app.routes().size());
    for (const std::string& a : addrs) std::fprintf(stderr, " %s", a.c_str());
    std::fprintf(stderr, "\n");
    app.listen(addrs);
    return 0;
}
