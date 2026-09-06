# SPDX-License-Identifier: BSD-3-Clause-Eco
"""The committed schema is the one the service declares.

example/orders.zap is read by the Go side of this repository, which derives the
layout again and refuses the file if it disagrees. That makes the file a fixture
in two languages at once, and a fixture that drifts from the service it describes
proves the wrong thing in both.
"""

import pathlib
import unittest

import zip
from example import orders

ZAP = pathlib.Path(__file__).resolve().parent.parent / "example" / "orders.zap"


class Committed(unittest.TestCase):
    def test_the_file_is_what_the_service_declares(self):
        self.assertEqual(
            ZAP.read_text(), str(zip.schema("orders", orders.app)),
            "example/orders.zap is stale: python3 -m zip example.orders:app -o example/orders.zap")

    def test_the_op_that_names_no_type_is_absent_and_accounted_for(self):
        s = zip.schema("orders", orders.app)
        self.assertEqual(len(orders.app.ops()), 5)
        self.assertEqual(s.ops(), 4)
        self.assertEqual([(g.op, g.field, g.cause) for g in s.gaps],
                         [("post_orders_note", "Note.body", "any")])

    def test_the_handlers_are_the_service_and_not_a_description_of_one(self):
        orders.placed.clear()
        first = orders.place(orders.Order(sku="cog", count=3))
        self.assertEqual((first.cents, first.filled), (3600, True))
        self.assertFalse(orders.place(orders.Order(sku="cog", count=101)).filled)
        orders.place(orders.Order(sku="pin", count=1))

        page = orders.find(orders.Query(sku="cog", limit=10))
        self.assertEqual(([o.sku for o in page.orders], page.total), (["cog"], 1))
        self.assertEqual(orders.find(orders.Query(sku="", limit=1)).total, 2)

        orders.cancel(orders.Cancel(id=first.id, reason="changed their mind"))
        self.assertEqual(orders.find(orders.Query(sku="cog", limit=10)).total, 0)
        self.assertTrue(orders.health().ready)

        orders.note(orders.Note(id=first.id, body={"seen": True}))
        self.assertEqual(orders.remarks[first.id], [{"seen": True}])


if __name__ == "__main__":
    unittest.main()
