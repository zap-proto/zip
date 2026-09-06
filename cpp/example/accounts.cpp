// SPDX-License-Identifier: BSD-3-Clause-Eco
//
// The ledger's implementation. zipcpp never reads a body — a schema states what
// an operation takes and answers, never what it does — but the bodies are here
// because a service that does not run is not a service, and because the header
// they satisfy is the same header the projections come from.

#include "accounts.hpp"

#include <algorithm>
#include <cstdio>

namespace {

// seal is a stand-in for the ledger's real hash: it is a fixed 32-byte run, and
// what matters to the schema is that it crosses inline.
std::array<uint8_t, 32> seal(uint64_t seq, int64_t cents) {
  std::array<uint8_t, 32> out{};
  for (size_t i = 0; i < out.size(); i++)
    out[i] = static_cast<uint8_t>((seq * 31 + static_cast<uint64_t>(cents) * 17 + i) & 0xff);
  return out;
}

uint64_t sequence = 0;

}  // namespace

account accounts::read(lookup req) {
  for (const auto& a : held_)
    if (a.id == req.id) return a;
  return account{req.id, 0, "USD", standing::closed};
}

receipt accounts::post(entry req) {
  auto at = std::find_if(held_.begin(), held_.end(),
                         [&](const account& a) { return a.id == req.account; });
  if (at == held_.end()) {
    held_.push_back(account{req.account, 0, "USD", standing::open});
    at = held_.end() - 1;
  }
  at->cents += req.cents;
  std::sort(held_.begin(), held_.end(),
            [](const account& a, const account& b) { return a.id < b.id; });
  sequence++;
  return receipt{sequence, read(lookup{req.account}), seal(sequence, req.cents)};
}

page accounts::list(window req) {
  page out{};
  out.total = static_cast<uint32_t>(held_.size());
  for (uint32_t i = req.offset; i < held_.size() && out.accounts.size() < req.limit; i++)
    out.accounts.push_back(held_[req.descending ? held_.size() - 1 - i : i]);
  return out;
}

// A run of the ledger, so the service is a thing that executes and not only a
// thing that is described.
int main() {
  accounts ledger;
  ledger.post(entry{"acct_a", 1200, "invoice 1", {}});
  ledger.post(entry{"acct_b", 500, "invoice 2", {}});
  receipt r = ledger.post(entry{"acct_a", -200, "refund", {0xde, 0xad}});

  account a = ledger.read(lookup{"acct_a"});
  std::printf("%s %lld %s standing=%u\n", a.id.c_str(), static_cast<long long>(a.cents),
              a.currency.c_str(), static_cast<unsigned>(a.state));
  std::printf("seq=%llu seal[0]=%u\n", static_cast<unsigned long long>(r.seq), r.seal[0]);

  page p = ledger.list(window{0, 10, false});
  std::printf("accounts=%zu total=%u\n", p.accounts.size(), p.total);
  return 0;
}
