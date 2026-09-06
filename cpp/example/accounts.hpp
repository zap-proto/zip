// SPDX-License-Identifier: BSD-3-Clause-Eco
//
// A ledger of accounts, declared once, in C++.
//
// This header is the whole source of the service's contract. The structs are
// ordinary structs; the service is an ordinary class; every sentence anyone
// reads about this API — in the OpenAPI document, in the MCP tool list, in the
// CLI's help — is a `///` comment on the declaration it is about, and is written
// exactly here.

#ifndef ACCOUNTS_HPP
#define ACCOUNTS_HPP

#include "../zip.hpp"

#include <array>
#include <cstdint>
#include <string>
#include <vector>

/// Where an account stands, which decides what may be posted against it.
enum class standing : uint8_t {
  /// Nothing may be posted.
  closed = 0,
  /// Anything may be posted.
  open = 1,
  /// Credits may be posted and debits may not.
  frozen = 2,
};

/// One account, as the ledger holds it.
struct account {
  /// The id the ledger issued. Stable for the life of the account.
  std::string id;
  /// Balance in the smallest unit of the account's currency.
  int64_t cents;
  /// The currency, as ISO 4217, upper case.
  std::string currency;
  /// Where the account stands.
  standing state;
};

/// Which account to read.
struct lookup {
  /// The id to read.
  std::string id;
};

/// A movement to post against one account.
struct entry {
  /// The account to post against.
  std::string account;
  /// How much to move, in the smallest unit. A debit is negative.
  int64_t cents;
  /// What the statement shows beside the amount.
  std::string memo;
  /// A reference the caller carries through untouched. The ledger reads none of it.
  std::vector<uint8_t> reference;
};

/// What posting an entry produced.
struct receipt {
  /// The ledger sequence the entry landed at. Rises by one per posted entry.
  uint64_t seq;
  /// The account as it stands after the entry applied.
  account moved;
  /// The hash the ledger sealed the entry under.
  std::array<uint8_t, 32> seal;
};

/// Where to start reading the account list, and how far to read.
struct window {
  /// Skip this many accounts.
  uint32_t offset;
  /// Read at most this many.
  uint16_t limit;
  /// Read from the highest id down instead of the lowest id up.
  bool descending;
};

/// One page of accounts.
struct page {
  /// The accounts on this page, in id order.
  std::vector<account> accounts;
  /// How many accounts exist in total, ignoring the window.
  uint32_t total;
};

/// The ledger's accounts: what they hold, and what moves them.
class ZIP_SERVICE accounts {
 public:
  /// Read one account by id.
  ///
  /// An id nobody opened reads as a closed account with a zero balance, so a
  /// caller never has to tell "absent" from "empty" by the shape of the answer.
  account read(lookup req);

  /// Post one movement and answer the account it moved.
  ///
  /// The sequence in the receipt is the ledger's own order, which is what a
  /// caller reconciles against; the seal is over the entry as it was written.
  receipt post(entry req);

  /// Read a page of accounts, lowest id first unless the window says otherwise.
  page list(window req);

 private:
  std::vector<account> held_;
};

#endif
